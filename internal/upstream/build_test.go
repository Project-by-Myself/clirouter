package upstream

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/tidwall/gjson"

	"github.com/Project-by-Myself/clirouter/internal/cloudauth"
	"github.com/Project-by-Myself/clirouter/internal/cloudauth/cloudtest"
	"github.com/Project-by-Myself/clirouter/internal/config"
	"github.com/Project-by-Myself/clirouter/internal/ir"
	"github.com/Project-by-Myself/clirouter/internal/router"
)

// account returns the first account of a single validated provider.
func account(t *testing.T, p config.Provider) *router.Account {
	t.Helper()
	cfg := config.Default()
	cfg.Providers = []config.Provider{p}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	r := router.New(cfg, nil, nil)
	if len(r.Providers()) != 1 || len(r.Providers()[0].Accounts) == 0 {
		t.Fatalf("no account for %+v", p)
	}
	return r.Providers()[0].Accounts[0]
}

func build(t *testing.T, a *router.Account, f ir.Format, model, body string, stream bool, in http.Header) (*http.Request, string) {
	t.Helper()
	if in == nil {
		in = http.Header{}
	}
	req, err := Build(context.Background(), a, f, model, []byte(body), stream, in)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(req.Body)
	if req.ContentLength != int64(len(b)) {
		t.Fatalf("content length %d, body %d", req.ContentLength, len(b))
	}
	return req, string(b)
}

const chatBody = `{"model":"my-deploy","stream":true,"messages":[{"role":"user","content":"hi"}]}`

func TestBuildAzureOpenAI(t *testing.T) {
	for _, base := range []string{"https://res.openai.azure.com", "https://res.openai.azure.com/openai", "https://res.openai.azure.com/openai/v1/"} {
		a := account(t, config.Provider{ID: "az", Type: "azure-openai", BaseURL: base, Keys: []string{"azure-key-0123456789"}})
		req, body := build(t, a, ir.FormatChat, "my-deploy", chatBody, true, nil)
		if req.URL.String() != "https://res.openai.azure.com/openai/v1/chat/completions" {
			t.Fatalf("%s: url %s", base, req.URL)
		}
		if req.Header.Get("Api-Key") != "azure-key-0123456789" || req.Header.Get("Authorization") != "" {
			t.Fatalf("auth headers %v", req.Header)
		}
		if body != chatBody {
			t.Fatalf("body changed: %s", body)
		}
		req, _ = build(t, a, ir.FormatResponses, "my-deploy", chatBody, true, nil)
		if req.URL.String() != "https://res.openai.azure.com/openai/v1/responses" {
			t.Fatalf("responses url %s", req.URL)
		}
	}
	a := account(t, config.Provider{ID: "az", Type: "azure-openai", BaseURL: "https://res.cognitiveservices.azure.com", Keys: []string{"k"}})
	if req, _ := build(t, a, ir.FormatChat, "d", chatBody, false, nil); req.URL.String() != "https://res.cognitiveservices.azure.com/openai/v1/chat/completions" {
		t.Fatalf("url %s", req.URL)
	}
	if f, pass := Choose("azure-openai", "d", ir.FormatResponses); f != ir.FormatResponses || !pass {
		t.Fatal("azure should pass responses through")
	}
	if f, pass := Choose("azure-openai", "d", ir.FormatAnthropic); f != ir.FormatChat || pass {
		t.Fatal("azure should translate anthropic to chat")
	}
}

func TestBuildOllama(t *testing.T) {
	a := account(t, config.Provider{ID: "local", Type: "ollama"})
	if !strings.HasSuffix(a.ID, ":local") {
		t.Fatalf("keyless account id %s", a.ID)
	}
	req, _ := build(t, a, ir.FormatChat, "qwen3-coder", chatBody, true, nil)
	if req.URL.String() != "http://127.0.0.1:11434/v1/chat/completions" {
		t.Fatalf("url %s", req.URL)
	}
	if _, ok := req.Header["Authorization"]; ok {
		t.Fatal("no key must mean no Authorization header")
	}
	a = account(t, config.Provider{ID: "local", Type: "ollama", Keys: []string{"ollama-key"}})
	if req, _ := build(t, a, ir.FormatChat, "m", chatBody, true, nil); req.Header.Get("Authorization") != "Bearer ollama-key" {
		t.Fatalf("auth %q", req.Header.Get("Authorization"))
	}
	if f, pass := Choose("ollama", "m", ir.FormatResponses); f != ir.FormatChat || pass {
		t.Fatal("ollama speaks chat")
	}
}

const bedrockModel = "us.anthropic.claude-sonnet-4-5-20250929-v1:0"
const anthBody = `{"model":"claude-x","max_tokens":100,"stream":true,"anthropic_version":"old","messages":[{"role":"user","content":"hi"}]}`

func TestBuildBedrockBearer(t *testing.T) {
	a := account(t, config.Provider{ID: "br", Type: "bedrock", BaseURL: "https://bedrock-runtime.us-east-1.amazonaws.com", Keys: []string{"ABSKbedrockapikey0123456789"}})
	in := http.Header{"Anthropic-Beta": {"claude-code-20250219"}, "Anthropic-Version": {"2023-06-01"}}
	req, body := build(t, a, ir.FormatAnthropic, bedrockModel, anthBody, true, in)
	if req.URL.EscapedPath() != "/model/us.anthropic.claude-sonnet-4-5-20250929-v1%3A0/invoke-with-response-stream" {
		t.Fatalf("path %s", req.URL.EscapedPath())
	}
	if req.Header.Get("Authorization") != "Bearer ABSKbedrockapikey0123456789" || req.Header.Get("Accept") != EventStreamType {
		t.Fatalf("headers %v", req.Header)
	}
	if req.Header.Get("Anthropic-Beta") != "" || req.Header.Get("Anthropic-Version") != "" {
		t.Fatalf("anthropic headers must not reach bedrock: %v", req.Header)
	}
	if body != `{"anthropic_version":"bedrock-2023-05-31","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}` {
		t.Fatalf("body %s", body)
	}
	req, _ = build(t, a, ir.FormatAnthropic, bedrockModel, anthBody, false, nil)
	if req.URL.EscapedPath() != "/model/us.anthropic.claude-sonnet-4-5-20250929-v1%3A0/invoke" || req.Header.Get("Accept") != "application/json" {
		t.Fatalf("non-stream %s %v", req.URL, req.Header)
	}
}

func TestBuildBedrockSigV4(t *testing.T) {
	defer func(f func() time.Time) { now = f }(now)
	now = func() time.Time { return time.Date(2025, 9, 30, 10, 0, 0, 0, time.UTC) }
	a := account(t, config.Provider{ID: "br", Type: "bedrock", BaseURL: "https://bedrock-runtime.eu-west-3.amazonaws.com", Keys: []string{"aws:AKIDEXAMPLE:wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY:sessiontok"}})
	req, body := build(t, a, ir.FormatAnthropic, bedrockModel, anthBody, true, nil)
	auth := req.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20250930/eu-west-3/bedrock/aws4_request, SignedHeaders=content-type;host;x-amz-date;x-amz-security-token, Signature=") {
		t.Fatalf("auth %s", auth)
	}
	if req.Header.Get("X-Amz-Security-Token") != "sessiontok" || req.Header.Get("X-Amz-Date") != "20250930T100000Z" {
		t.Fatalf("headers %v", req.Header)
	}
	// Re-signing the same request reproduces the signature.
	chk, _ := http.NewRequest("POST", req.URL.String(), nil)
	chk.Header = http.Header{"Content-Type": {"application/json"}}
	cloudauth.SignV4(chk, []byte(body), cloudauth.AWSCredentials{AccessKeyID: "AKIDEXAMPLE", SecretAccessKey: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY", SessionToken: "sessiontok"}, "eu-west-3", "bedrock", now())
	if chk.Header.Get("Authorization") != auth {
		t.Fatalf("signature not reproducible:\n%s\n%s", chk.Header.Get("Authorization"), auth)
	}
	if strings.Contains(auth, "wJalrXUtnFEMI") {
		t.Fatal("secret in header")
	}
	// A region override (VPC endpoint / proxy) is honored.
	a = account(t, config.Provider{ID: "br", Type: "bedrock", BaseURL: "https://vpce-123.bedrock-runtime.example.internal", Region: "ap-southeast-1", Keys: []string{"aws:AKID:SECRET"}})
	if req, _ := build(t, a, ir.FormatAnthropic, bedrockModel, anthBody, false, nil); !strings.Contains(req.Header.Get("Authorization"), "/ap-southeast-1/bedrock/aws4_request") {
		t.Fatalf("auth %s", req.Header.Get("Authorization"))
	}
}

func TestBuildVertexServiceAccount(t *testing.T) {
	ts := cloudtest.NewTokenServer(t)
	sa := cloudtest.WriteServiceAccount(t, "proj-1", ts.TokenURL())

	// Gemini, regional location: the default global host becomes regional.
	a := account(t, config.Provider{ID: "vx", Type: "vertex", Region: "us-east5", Keys: []string{"sa:" + sa}})
	if st := a.Status(); st.Kind != "service_account" || st.Meta["project"] != "proj-1" || st.Label != "sa:"+sa {
		t.Fatalf("status %+v", st)
	}
	req, _ := build(t, a, ir.FormatGemini, "gemini-2.5-pro", `{"contents":[]}`, true, nil)
	if req.URL.String() != "https://us-east5-aiplatform.googleapis.com/v1/projects/proj-1/locations/us-east5/publishers/google/models/gemini-2.5-pro:streamGenerateContent?alt=sse" {
		t.Fatalf("url %s", req.URL)
	}
	if req.Header.Get("Authorization") != "Bearer ya29.test-1" || req.Header.Get("X-Goog-Api-Key") != "" {
		t.Fatalf("auth %v", req.Header)
	}

	// Claude, location from the base_url host, project override.
	a = account(t, config.Provider{ID: "vx", Type: "vertex", BaseURL: "https://europe-west1-aiplatform.googleapis.com", Project: "other-proj", Keys: []string{"sa:" + sa}})
	in := http.Header{"Anthropic-Beta": {"context-1m-2025-08-07"}, "Anthropic-Version": {"2023-06-01"}}
	req, body := build(t, a, ir.FormatAnthropic, "claude-sonnet-4-5@20250929", anthBody, true, in)
	if req.URL.String() != "https://europe-west1-aiplatform.googleapis.com/v1/projects/other-proj/locations/europe-west1/publishers/anthropic/models/claude-sonnet-4-5@20250929:streamRawPredict" {
		t.Fatalf("url %s", req.URL)
	}
	if body != `{"anthropic_version":"vertex-2023-10-16","max_tokens":100,"stream":true,"messages":[{"role":"user","content":"hi"}]}` {
		t.Fatalf("body %s", body)
	}
	if req.Header.Get("Anthropic-Beta") != "context-1m-2025-08-07" || req.Header.Get("Anthropic-Version") != "" {
		t.Fatalf("headers %v", req.Header)
	}
	if ts.Hits.Load() != 1 {
		t.Fatalf("token fetched %d times, want 1 (cached)", ts.Hits.Load())
	}
	req, _ = build(t, a, ir.FormatAnthropic, "claude-sonnet-4-5@20250929", anthBody, false, nil)
	if !strings.HasSuffix(req.URL.Path, ":rawPredict") {
		t.Fatalf("non-stream url %s", req.URL)
	}

	// Default host, no region: the global location.
	a = account(t, config.Provider{ID: "vx", Type: "vertex", Keys: []string{"sa:" + sa}})
	req, _ = build(t, a, ir.FormatGemini, "gemini-2.5-flash", `{}`, false, nil)
	if req.URL.String() != "https://aiplatform.googleapis.com/v1/projects/proj-1/locations/global/publishers/google/models/gemini-2.5-flash:generateContent" {
		t.Fatalf("url %s", req.URL)
	}

	if f, pass := Choose("vertex", "claude-opus-4-1", ir.FormatAnthropic); f != ir.FormatAnthropic || !pass {
		t.Fatal("vertex claude should pass anthropic through")
	}
	if f, pass := Choose("vertex", "gemini-2.5-pro", ir.FormatAnthropic); f != ir.FormatGemini || pass {
		t.Fatal("vertex gemini should translate")
	}
}

func TestBuildVertexAPIKey(t *testing.T) {
	a := account(t, config.Provider{ID: "vx", Type: "vertex", Keys: []string{"AIzaTestKey"}})
	req, _ := build(t, a, ir.FormatGemini, "gemini-2.5-flash", `{}`, false, nil)
	if req.URL.String() != "https://aiplatform.googleapis.com/v1/publishers/google/models/gemini-2.5-flash:generateContent" || req.Header.Get("X-Goog-Api-Key") != "AIzaTestKey" {
		t.Fatalf("express mode changed: %s %v", req.URL, req.Header)
	}
	if _, err := Build(context.Background(), a, ir.FormatAnthropic, "claude-x", []byte(anthBody), true, http.Header{}); err == nil || !strings.Contains(err.Error(), "project") {
		t.Fatalf("claude without project: %v", err)
	}
}

func TestCloudAnthropicBody(t *testing.T) {
	got := string(cloudAnthropicBody([]byte(`{"model":"m","stream":false,"system":[{"type":"text","text":"s"}],"max_tokens":5}`), "v", false))
	if got != `{"anthropic_version":"v","system":[{"type":"text","text":"s"}],"max_tokens":5}` {
		t.Fatal(got)
	}
	if !gjson.Valid(got) {
		t.Fatal("invalid JSON")
	}
}
