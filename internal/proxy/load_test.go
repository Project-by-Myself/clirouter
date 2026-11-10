package proxy

import (
	"io"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestConcurrentStreamsNoLeak runs many concurrent streaming requests (some
// of which the client abandons mid-stream) and checks that goroutines return
// to baseline, i.e. nothing is left blocked on an upstream or a keepalive.
func TestConcurrentStreamsNoLeak(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	parts := strings.SplitAfter(anthropicOK, "\n\n")
	up := newMock(t, func(_ int64, w http.ResponseWriter, r *http.Request) {
		sseWrite(w, strings.Join(parts[:3], ""))
		for i := 0; i < 20; i++ {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(5 * time.Millisecond):
			}
			sseWrite(w, parts[2])
		}
		sseWrite(w, strings.Join(parts[3:], ""))
	})
	s, h := front(t, testCfg(anthropicProvider("a", up.URL, "k1", "k2", "k3")))
	base := runtime.NumGoroutine()
	const n = 300
	var wg sync.WaitGroup
	errs := make(chan string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			resp, err := http.Post(s.URL+"/v1/messages", "application/json", strings.NewReader(anthropicReq))
			if err != nil {
				errs <- err.Error()
				return
			}
			defer resp.Body.Close()
			if i%5 == 0 { // abandon mid-stream
				buf := make([]byte, 64)
				resp.Body.Read(buf)
				return
			}
			b, _ := io.ReadAll(resp.Body)
			if !strings.HasSuffix(string(b), "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n") {
				errs <- "truncated stream"
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatal(e)
	}
	http.DefaultClient.CloseIdleConnections()
	h.Runtime().Clients.Close() // pooled keep-alive conns are not leaks
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if h.Metrics.Inflight() == 0 && runtime.NumGoroutine() <= base+10 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("leak: inflight=%d goroutines=%d base=%d", h.Metrics.Inflight(), runtime.NumGoroutine(), base)
}
