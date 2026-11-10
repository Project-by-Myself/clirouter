package upstream

import (
	"net/http"
	"testing"
	"time"
)

// OpenAI and Anthropic send rate-limit reset headers with every response; a
// single 5xx must not inherit them as a cooldown, while a 429 should.
func TestClassifyIgnoresQuotaResetHeadersOnServerErrors(t *testing.T) {
	h := http.Header{}
	h.Set("X-Ratelimit-Reset-Requests", "0s")
	h.Set("X-Ratelimit-Reset-Tokens", "6m0s")
	h.Set("Anthropic-Ratelimit-Tokens-Reset", time.Now().Add(time.Minute).UTC().Format(time.RFC3339))
	for _, st := range []int{500, 502, 503, 529} {
		if f := Classify(st, h, []byte(`{"error":{"message":"boom"}}`), false); f.RetryAfter != 0 {
			t.Errorf("%d: RetryAfter = %v, want 0", st, f.RetryAfter)
		}
	}
	if f := Classify(429, h, nil, false); f.RetryAfter < 30*time.Second {
		t.Errorf("429: RetryAfter = %v, want the reset hint", f.RetryAfter)
	}
	h.Set("Retry-After", "7")
	if f := Classify(503, h, nil, false); f.RetryAfter != 7*time.Second {
		t.Errorf("503 with Retry-After: %v", f.RetryAfter)
	}
}
