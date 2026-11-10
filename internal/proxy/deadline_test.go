package proxy

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Project-by-Myself/clirouter/internal/config"
)

// A client trickling its body is cut off after bodyReadTimeout.
func TestSlowBodyTimesOut(t *testing.T) {
	old := bodyReadTimeout
	bodyReadTimeout = 200 * time.Millisecond
	t.Cleanup(func() { bodyReadTimeout = old }) // runs after the server closes
	s, _ := front(t, testCfg(config.Provider{ID: "ant", Type: "anthropic", BaseURL: "http://127.0.0.1:1", Keys: []string{"k"}, Models: []string{"claude-x"}}))
	conn, err := net.Dial("tcp", strings.TrimPrefix(s.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "POST /v1/messages HTTP/1.1\r\nHost: x\r\nContent-Length: 1000\r\n\r\n{")
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	start := time.Now()
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil || time.Since(start) > 3*time.Second {
		t.Fatalf("slow body was not cut off (err=%v)", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

// The per-write deadline must not leak into the next request on a
// kept-alive connection.
func TestWriteDeadlineClearedForKeepAlive(t *testing.T) {
	old := clientWriteTimeout
	clientWriteTimeout = 100 * time.Millisecond
	t.Cleanup(func() { clientWriteTimeout = old })
	up := newMock(t, func(_ int64, w http.ResponseWriter, _ *http.Request) { sseWrite(w, anthropicOK) })
	s, _ := front(t, testCfg(config.Provider{ID: "ant", Type: "anthropic", BaseURL: up.URL, Keys: []string{"k"}, Models: []string{"claude-x"}}))
	conn, err := net.Dial("tcp", strings.TrimPrefix(s.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	br := bufio.NewReader(conn)
	for i := 0; i < 2; i++ {
		fmt.Fprintf(conn, "POST /v1/messages HTTP/1.1\r\nHost: x\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s", len(anthropicReq), anthropicReq)
		resp, err := http.ReadResponse(br, nil)
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		var sb strings.Builder
		bufio.NewReader(resp.Body).WriteTo(&sb)
		resp.Body.Close()
		if resp.StatusCode != 200 || !strings.Contains(sb.String(), "message_stop") {
			t.Fatalf("request %d: %d %q", i, resp.StatusCode, sb.String())
		}
		time.Sleep(300 * time.Millisecond) // longer than clientWriteTimeout
	}
}
