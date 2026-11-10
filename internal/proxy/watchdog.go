package proxy

import (
	"context"
	"errors"
	"io"
	"time"
)

var (
	errFirstByte = errors.New("upstream sent no data before the first-byte timeout")
	errIdle      = errors.New("upstream stream stalled (idle timeout)")
)

// watchdog cancels the upstream request when its deadline elapses. The
// deadline is re-armed on every byte received, so it measures silence, not
// total duration.
type watchdog struct {
	t      *time.Timer
	cause  error
	cancel context.CancelCauseFunc
}

func newWatchdog(d time.Duration, cause error, cancel context.CancelCauseFunc) *watchdog {
	w := &watchdog{cause: cause, cancel: cancel}
	w.t = time.AfterFunc(d, func() { cancel(w.cause) })
	return w
}

func (w *watchdog) rearm(d time.Duration, cause error) {
	if w.t.Stop() {
		w.cause = cause
		w.t.Reset(d)
	}
}

func (w *watchdog) stop() { w.t.Stop() }

// watchedReader re-arms the watchdog with the idle timeout on every read.
type watchedReader struct {
	r     io.Reader
	wd    *watchdog
	idle  time.Duration
	first time.Time
}

func (w *watchedReader) Read(p []byte) (int, error) {
	n, err := w.r.Read(p)
	if n > 0 {
		if w.first.IsZero() {
			w.first = time.Now()
		}
		w.wd.rearm(w.idle, errIdle)
	}
	return n, err
}
