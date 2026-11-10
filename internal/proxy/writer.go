package proxy

import (
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/Project-by-Myself/clirouter/internal/translate"
)

// clientWriter serializes writes to the client between the request goroutine
// and the keepalive goroutine. Writes always contain whole SSE events, so a
// ping can never land in the middle of one.
type clientWriter struct {
	mu          sync.Mutex
	w           http.ResponseWriter
	rc          *http.ResponseController
	codec       *translate.Codec
	stream      bool
	headerSent  bool
	contentSent bool
	broken      bool
	status      int
	lastWrite   time.Time
	firstByte   time.Time
	stopc       chan struct{}
	stopOnce    sync.Once
}

// clientWriteTimeout bounds a single write to the client. A client that
// stops reading would otherwise block the handler (and its upstream
// connection) forever, since a full TCP window never errors.
var clientWriteTimeout = 2 * time.Minute

// armLocked sets the write deadline before a write; must hold c.mu.
func (c *clientWriter) armLocked() {
	_ = c.rc.SetWriteDeadline(time.Now().Add(clientWriteTimeout))
}

func newClientWriter(w http.ResponseWriter, codec *translate.Codec, stream bool) *clientWriter {
	return &clientWriter{w: w, rc: http.NewResponseController(w), codec: codec, stream: stream, stopc: make(chan struct{})}
}

func (c *clientWriter) sendStreamHeadersLocked() {
	h := c.w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	c.w.WriteHeader(http.StatusOK)
	c.headerSent = true
	c.status = http.StatusOK
}

// writeEvents writes stream bytes; flush=false lets the caller batch several
// events that arrived together into one flush.
func (c *clientWriter) writeEvents(b []byte, flush bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.broken {
		return http.ErrHandlerTimeout
	}
	if !c.headerSent {
		c.sendStreamHeadersLocked()
	}
	c.armLocked()
	if len(b) > 0 {
		if _, err := c.w.Write(b); err != nil {
			c.broken = true
			return err
		}
		if !c.contentSent {
			c.contentSent = true
			c.firstByte = time.Now()
		}
	}
	if flush {
		if err := c.rc.Flush(); err != nil {
			c.broken = true
			return err
		}
	}
	c.lastWrite = time.Now()
	return nil
}

func (c *clientWriter) flush() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.broken && c.headerSent {
		c.armLocked()
		if err := c.rc.Flush(); err != nil {
			c.broken = true
		}
	}
}

// writeJSON sends a complete non-streaming response.
func (c *clientWriter) writeJSON(status int, contentType string, body []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.headerSent || c.broken {
		return
	}
	if contentType == "" {
		contentType = "application/json"
	}
	c.w.Header().Set("Content-Type", contentType)
	c.w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	c.w.WriteHeader(status)
	c.headerSent = true
	c.status = status
	c.armLocked()
	if _, err := c.w.Write(body); err != nil {
		c.broken = true
	}
	c.contentSent = true
	if c.firstByte.IsZero() {
		c.firstByte = time.Now()
	}
}

// fail reports an error in whatever form the client can still receive: a
// proper HTTP status if nothing was sent yet, otherwise an in-band error.
func (c *clientWriter) fail(status int, errType, msg string, retryAfter time.Duration, raw []byte) {
	c.mu.Lock()
	if c.headerSent {
		defer c.mu.Unlock()
		if c.broken || !c.stream {
			return
		}
		c.armLocked()
		if _, err := c.w.Write(c.codec.StreamErrorEvent(errType, msg)); err == nil {
			_ = c.rc.Flush()
		}
		return
	}
	c.mu.Unlock()
	body := raw
	if body == nil {
		body = c.codec.ErrorBody(status, errType, msg)
	}
	if retryAfter > 0 {
		c.w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter.Seconds()+0.999)))
	}
	c.writeJSON(status, "application/json", body)
}

// keepalive commits stream headers after `early` with a format-native ping
// (so clients never sit on a silent socket while upstream thinks) and keeps
// pinging whenever the client has seen nothing for `interval`.
func (c *clientWriter) keepalive(early, interval time.Duration) {
	ping := c.codec.Ping()
	if ping == nil || early <= 0 || interval <= 0 {
		return
	}
	go func() {
		t := time.NewTimer(early)
		defer t.Stop()
		for {
			select {
			case <-c.stopc:
				return
			case <-t.C:
			}
			c.mu.Lock()
			if c.broken {
				c.mu.Unlock()
				return
			}
			next := interval
			if !c.headerSent {
				c.sendStreamHeadersLocked()
				c.writePingLocked(ping)
			} else if idle := time.Since(c.lastWrite); idle >= interval-50*time.Millisecond {
				c.writePingLocked(ping)
			} else {
				next = interval - idle
			}
			c.mu.Unlock()
			t.Reset(next)
		}
	}()
}

func (c *clientWriter) writePingLocked(ping []byte) {
	c.armLocked()
	if _, err := c.w.Write(ping); err != nil {
		c.broken = true
		return
	}
	if err := c.rc.Flush(); err != nil {
		c.broken = true
		return
	}
	c.lastWrite = time.Now()
}

// stop ends the keepalive goroutine and clears the write deadline, which is
// per connection: a stale one would break the next request on a kept-alive
// connection.
func (c *clientWriter) stop() {
	c.stopOnce.Do(func() {
		close(c.stopc)
		c.mu.Lock()
		_ = c.rc.SetWriteDeadline(time.Time{})
		c.mu.Unlock()
	})
}

func (c *clientWriter) state() (headerSent, contentSent bool, status int, first time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.headerSent, c.contentSent, c.status, c.firstByte
}
