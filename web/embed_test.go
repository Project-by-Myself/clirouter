package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandlerNoTraversalNoListing(t *testing.T) {
	hs := httptest.NewServer(Handler())
	defer hs.Close()
	index := fetch(t, hs.URL+"/")
	if !strings.Contains(index, `<div id="root">`) {
		t.Fatalf("index: %q", index)
	}
	for _, p := range []string{"/assets/", "/assets", "/../embed.go", "/%2e%2e/embed.go", "/..%2fembed.go", "/settings/../../go.mod", "/no/such/page"} {
		if got := fetch(t, hs.URL+p); got != index {
			t.Errorf("%s: did not fall back to index (%.80q)", p, got)
		}
	}
	if !strings.Contains(fetch(t, hs.URL+"/theme.js"), "clr.theme") {
		t.Errorf("theme.js not served")
	}
}

func fetch(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}
