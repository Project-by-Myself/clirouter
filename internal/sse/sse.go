// Package sse is a low-allocation Server-Sent Events framer. It yields whole
// events (raw bytes included) so the proxy can pass them through untouched,
// inspect them cheaply, and inject keepalives only on event boundaries.
package sse

import (
	"bufio"
	"bytes"
	"errors"
	"io"
)

var ErrEventTooLarge = errors.New("sse: event exceeds size limit")

type Event struct {
	Name []byte // value of the last "event:" field, nil if absent
	Data []byte // "data:" fields joined by '\n'
	Raw  []byte // exact upstream bytes including the terminating blank line
}

type Reader struct {
	br       *bufio.Reader
	maxEvent int
	raw      []byte
	data     []byte
	name     []byte
}

func NewReader(r io.Reader, maxEvent int) *Reader {
	if maxEvent <= 0 {
		maxEvent = 64 << 20
	}
	return &Reader{br: bufio.NewReaderSize(r, 32<<10), maxEvent: maxEvent}
}

// Buffered reports whether more bytes are already available without blocking;
// the caller uses it to batch writes into a single flush.
func (r *Reader) Buffered() int { return r.br.Buffered() }

// Next returns the next event. The returned slices are only valid until the
// following call. A trailing event without a blank line is returned at EOF.
func (r *Reader) Next() (Event, error) {
	r.raw = r.raw[:0]
	r.data = r.data[:0]
	r.name = r.name[:0]
	hasName, hasData, hasAny := false, false, false
	for {
		line, err := r.br.ReadSlice('\n')
		if err == bufio.ErrBufferFull {
			// Long line: accumulate across buffer fills.
			buf := append([]byte(nil), line...)
			for err == bufio.ErrBufferFull {
				if len(buf)+len(r.raw) > r.maxEvent {
					return Event{}, ErrEventTooLarge
				}
				line, err = r.br.ReadSlice('\n')
				buf = append(buf, line...)
			}
			line = buf
		}
		if len(line) > 0 {
			r.raw = append(r.raw, line...)
			if len(r.raw) > r.maxEvent {
				return Event{}, ErrEventTooLarge
			}
		}
		if err != nil {
			if err == io.EOF {
				if l := trimEOL(line); len(l) > 0 && l[0] != ':' {
					r.field(l, &hasName, &hasData)
					hasAny = true
				}
				if hasAny {
					return r.event(hasName), nil
				}
			}
			return Event{}, err
		}
		l := trimEOL(line)
		if len(l) == 0 {
			if hasAny {
				return r.event(hasName), nil
			}
			// Blank line after only comments (upstream keepalives such as
			// ": OPENROUTER PROCESSING"): drop it; we send our own pings.
			r.raw = r.raw[:0]
			continue
		}
		if l[0] == ':' {
			continue
		}
		hasAny = true
		r.field(l, &hasName, &hasData)
	}
}

func (r *Reader) event(hasName bool) Event {
	ev := Event{Data: r.data, Raw: r.raw}
	if hasName {
		ev.Name = r.name
	}
	return ev
}

func (r *Reader) field(l []byte, hasName, hasData *bool) {
	if len(l) == 0 || l[0] == ':' {
		return
	}
	var key, val []byte
	if i := bytes.IndexByte(l, ':'); i >= 0 {
		key, val = l[:i], l[i+1:]
		if len(val) > 0 && val[0] == ' ' {
			val = val[1:]
		}
	} else {
		key = l
	}
	switch string(key) {
	case "data":
		if *hasData {
			r.data = append(r.data, '\n')
		}
		r.data = append(r.data, val...)
		*hasData = true
	case "event":
		r.name = append(r.name[:0], val...)
		*hasName = true
	}
}

func trimEOL(b []byte) []byte {
	if n := len(b); n > 0 && b[n-1] == '\n' {
		b = b[:n-1]
	}
	if n := len(b); n > 0 && b[n-1] == '\r' {
		b = b[:n-1]
	}
	return b
}

// AppendEvent appends a framed SSE event. name may be empty.
func AppendEvent(dst []byte, name string, data []byte) []byte {
	if name != "" {
		dst = append(dst, "event: "...)
		dst = append(dst, name...)
		dst = append(dst, '\n')
	}
	dst = append(dst, "data: "...)
	dst = append(dst, data...)
	return append(dst, '\n', '\n')
}

// AppendData appends a data-only event.
func AppendData(dst []byte, data []byte) []byte { return AppendEvent(dst, "", data) }
