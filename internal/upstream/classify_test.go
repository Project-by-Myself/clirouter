package upstream

import (
	"net/http"
	"testing"
	"time"

	"github.com/Project-by-Myself/clirouter/internal/router"
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

func TestClassifyAWSErrors(t *testing.T) {
	cases := []struct {
		status int
		typ    string
		body   string
		kind   router.FailureKind
		action Action
	}{
		{429, "ThrottlingException:http://internal.amazon.com/coral/com.amazon.bedrock/", `{"message":"Too many requests, please wait before trying again."}`, router.FailRateLimit, NextAccount},
		{400, "ServiceQuotaExceededException", `{"message":"Too many tokens per day"}`, router.FailRateLimit, NextAccount},
		{403, "AccessDeniedException", `{"message":"You do not have access to the model with the specified model ID."}`, router.FailAuth, NextAccount},
		{403, "UnrecognizedClientException", `{"Message":"The security token included in the request is invalid."}`, router.FailAuth, NextAccount},
		{400, "ValidationException", `{"message":"messages: field required"}`, 0, Fatal},
		{400, "ValidationException", `{"message":"The provided model identifier is invalid."}`, router.FailModel, NextTarget},
		{404, "ResourceNotFoundException", `{"message":"Model not found"}`, router.FailModel, NextTarget},
		{424, "ModelErrorException", `{"message":"model failed"}`, router.FailServer, NextAccount},
		{503, "ServiceUnavailableException", `{"message":"Service unavailable"}`, router.FailServer, NextTarget},
	}
	for _, c := range cases {
		h := http.Header{"X-Amzn-Errortype": {c.typ}}
		f := Classify(c.status, h, []byte(c.body), false)
		if f.Action != c.action || (c.action != Fatal && f.Kind != c.kind) {
			t.Errorf("%d %s: got kind %d action %d (%s)", c.status, c.typ, f.Kind, f.Action, f.Message)
		}
		if f.Message == "" || f.Message == http.StatusText(c.status) {
			t.Errorf("%s: message not extracted: %q", c.typ, f.Message)
		}
	}
	// Without the header (e.g. behind a proxy) the status still classifies sensibly.
	if f := Classify(429, http.Header{}, []byte(`{"message":"Too many requests"}`), false); f.Kind != router.FailRateLimit {
		t.Errorf("plain 429: %+v", f)
	}
	if f := Classify(400, http.Header{}, []byte(`{"message":"messages: field required"}`), false); f.Action != Fatal {
		t.Errorf("plain 400: %+v", f)
	}
	// Azure: a missing deployment is a model problem, not a fatal request error.
	f := Classify(404, http.Header{}, []byte(`{"error":{"code":"DeploymentNotFound","message":"The API deployment for this resource does not exist."}}`), false)
	if f.Kind != router.FailModel || f.Action != NextTarget {
		t.Errorf("azure deployment: %+v", f)
	}
}
