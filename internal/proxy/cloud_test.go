package proxy

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tidwall/gjson"

	"github.com/Project-by-Myself/clirouter/internal/cloudauth"
	"github.com/Project-by-Myself/clirouter/internal/cloudauth/cloudtest"
	"github.com/Project-by-Myself/clirouter/internal/config"
	"github.com/Project-by-Myself/clirouter/internal/upstream"
)

// End-to-end tests of the cloud provider types against local mock
// upstreams: Bedrock (event-stream, SigV4 and API keys), Vertex AI with a
// service account (Claude and Gemini), Azure OpenAI and Ollama.

const bedrockModel = "us.anthropic.claude-sonnet-4-5-20250929-v1:0"

const anthropicMsg = `{"id":"msg_1","type":"message","role":"assistant","model":"claude-x","content":[{"type":"text","text":"Hello"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":5}}`

const chatReq = `{"model":"claude-x","stream":true,"stream_options":{"include_usage":true},"messages":[{"role":"user","content":"hi"}]}`
const chatReqNoStream = `{"model":"claude-x","messages":[{"role":"user","content":"hi"}]}`

// bedrockFrames re-encodes an Anthropic SSE stream as Bedrock event-stream
// chunk frames.
func bedrockFrames(sse string) []byte {
	var out []byte
	for _, ev := range strings.SplitAfter(sse, "\n\n") {
		for _, line := range strings.Split(ev, "\n") {
			if data, ok := strings.CutPrefix(line, "data: "); ok {
				out = upstream.AppendBedrockChunk(out, []byte(data))
			}
		}
	}
	return out
}

func eventStreamWrite(w http.ResponseWriter, frames []byte) {
	w.Header().Set("Content-Type", upstream.EventStreamType)
	w.Write(frames)
	w.(http.Flusher).Flush()
}

func bedrockProvider(id, url string, keys ...string) config.Provider {
	return config.Provider{ID: id, Type: "bedrock", BaseURL: url, Region: "us-west-2", Keys: keys, ModelMap: map[string]string{"claude-x": bedrockModel}}
}

var testAWS = cloudauth.AWSCredentials{AccessKeyID: "AKIDEXAMPLE", SecretAccessKey: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY", SessionToken: "tok"}

const testAWSKey = "aws:AKIDEXAMPLE:wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY:tok"

// checkSigV4 recomputes the SigV4 signature of a received request.
func checkSigV4(r *http.Request, body []byte) string {
	ts, err := time.Parse("20060102T150405Z", r.Header.Get("X-Amz-Date"))
	if err != nil {
		return "bad X-Amz-Date"
	}
	chk, _ := http.NewRequest(r.Method, "http://"+r.Host+r.URL.EscapedPath(), nil)
	chk.Header = http.Header{"Content-Type": r.Header.Values("Content-Type")}
	cloudauth.SignV4(chk, body, testAWS, "us-west-2", "bedrock", ts)
	if got, want := r.Header.Get("Authorization"), chk.Header.Get("Authorization"); got != want {
		return "signature mismatch: " + got + " != " + want
	}
	if r.Header.Get("X-Amz-Security-Token") != "tok" {
		return "missing session token"
	}
	return ""
}

// checkBedrockBody verifies the Anthropic body was adapted for Bedrock.
func checkBedrockBody(body []byte) string {
	if gjson.GetBytes(body, "anthropic_version").Str != "bedrock-2023-05-31" || gjson.GetBytes(body, "model").Exists() || gjson.GetBytes(body, "stream").Exists() {
		return "bad body: " + string(body)
	}
	return ""
}

func TestBedrockSigV4Streaming(t *testing.T) {
	up := newMock(t, func(_ int64, w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.URL.EscapedPath() != "/model/us.anthropic.claude-sonnet-4-5-20250929-v1%3A0/invoke-with-response-stream" {
			http.Error(w, `{"message":"bad path `+r.URL.EscapedPath()+`"}`, 404)
			return
		}
		if msg := checkSigV4(r, body) + checkBedrockBody(body); msg != "" {
			w.Header().Set("X-Amzn-Errortype", "InvalidSignatureException")
			http.Error(w, `{"message":"`+strings.ReplaceAll(msg, `"`, `'`)+`"}`, 403)
			return
		}
		eventStreamWrite(w, bedrockFrames(anthropicOK))
	})
	s, h := front(t, testCfg(bedrockProvider("br", up.URL, testAWSKey)))

	// Anthropic client: the converted stream equals what the Anthropic API
	// would have sent (passthrough-equivalent).
	resp, body := post(t, s.URL+"/v1/messages", anthropicReq)
	if resp.StatusCode != 200 || body != anthropicOK {
		t.Fatalf("status %d body:\n%q", resp.StatusCode, body)
	}
	if snap := h.Metrics.Snapshot(); snap.InputTokens != 10 || snap.OutputTokens != 5 {
		t.Fatalf("usage not recorded: %+v", snap)
	}

	// OpenAI chat client: translated, streaming and non-streaming.
	resp, body = post(t, s.URL+"/v1/chat/completions", chatReq)
	if resp.StatusCode != 200 || !strings.Contains(body, `"content":"Hello"`) || !strings.HasSuffix(body, "data: [DONE]\n\n") || !strings.Contains(body, `"completion_tokens":5`) {
		t.Fatalf("chat stream: status %d body %q", resp.StatusCode, body)
	}
	resp, body = post(t, s.URL+"/v1/chat/completions", chatReqNoStream)
	if resp.StatusCode != 200 || gjson.Get(body, "choices.0.message.content").Str != "Hello" || gjson.Get(body, "usage.prompt_tokens").Int() != 10 {
		t.Fatalf("chat non-stream: status %d body %q", resp.StatusCode, body)
	}

	// Responses client.
	resp, body = post(t, s.URL+"/v1/responses", `{"model":"claude-x","stream":true,"input":"hi"}`)
	if resp.StatusCode != 200 || !strings.Contains(body, "response.output_text.delta") || !strings.Contains(body, "event: response.completed") {
		t.Fatalf("responses: status %d body %q", resp.StatusCode, body)
	}
}

func TestBedrockNonStreamingPassthrough(t *testing.T) {
	up := newMock(t, func(_ int64, w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.URL.EscapedPath() != "/model/us.anthropic.claude-sonnet-4-5-20250929-v1%3A0/invoke" || r.Header.Get("Authorization") != "Bearer ABSK-test-key-0123456789" {
			http.Error(w, `{"message":"bad request line or auth"}`, 400)
			return
		}
		if msg := checkBedrockBody(body); msg != "" {
			http.Error(w, `{"message":"bad body"}`, 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, anthropicMsg)
	})
	s, h := front(t, testCfg(bedrockProvider("br", up.URL, "ABSK-test-key-0123456789")))
	resp, body := post(t, s.URL+"/v1/messages", strings.Replace(anthropicReq, `"stream":true`, `"stream":false`, 1))
	if resp.StatusCode != 200 || body != anthropicMsg {
		t.Fatalf("status %d body %q", resp.StatusCode, body)
	}
	if snap := h.Metrics.Snapshot(); snap.OutputTokens != 5 {
		t.Fatalf("usage not recorded: %+v", snap)
	}
}

func TestBedrockErrors(t *testing.T) {
	// 429 ThrottlingException on the first key fails over to the second.
	up := newMock(t, func(_ int64, w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer key-one-0123456789" {
			w.Header().Set("X-Amzn-Errortype", "ThrottlingException:http://internal.amazon.com/coral/com.amazon.bedrock/")
			http.Error(w, `{"message":"Too many requests, please wait before trying again."}`, 429)
			return
		}
		eventStreamWrite(w, bedrockFrames(anthropicOK))
	})
	cfg := testCfg(bedrockProvider("br", up.URL, "key-one-0123456789", "key-two-0123456789"))
	cfg.Routing.Strategy = "fill-first"
	s, h := front(t, cfg)
	resp, body := post(t, s.URL+"/v1/messages", anthropicReq)
	if resp.StatusCode != 200 || body != anthropicOK || up.hits.Load() != 2 {
		t.Fatalf("status %d hits %d body %q", resp.StatusCode, up.hits.Load(), body)
	}
	if st := h.Runtime().Router.Providers()[0].Accounts[0].Status(); st.ModelCooldown[bedrockModel] == "" {
		t.Fatalf("throttled key not cooling down: %+v", st)
	}

	// 400 ValidationException is the request's fault: returned at once.
	bad := newMock(t, func(_ int64, w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Amzn-Errortype", "ValidationException")
		http.Error(w, `{"message":"messages.0.content: field required"}`, 400)
	})
	s, _ = front(t, testCfg(bedrockProvider("br", bad.URL, "key-one-0123456789", "key-two-0123456789")))
	resp, body = post(t, s.URL+"/v1/chat/completions", chatReq)
	if resp.StatusCode != 400 || !strings.Contains(body, "field required") || bad.hits.Load() != 1 {
		t.Fatalf("status %d hits %d body %q", resp.StatusCode, bad.hits.Load(), body)
	}

	// 403 AccessDeniedException benches the key for every model.
	denied := newMock(t, func(_ int64, w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Amzn-Errortype", "AccessDeniedException")
		http.Error(w, `{"message":"You don't have access to the model with the specified model ID."}`, 403)
	})
	s, h = front(t, testCfg(bedrockProvider("br", denied.URL, "key-one-0123456789")))
	resp, _ = post(t, s.URL+"/v1/messages", anthropicReq)
	if resp.StatusCode != 403 {
		t.Fatalf("access denied: status %d", resp.StatusCode)
	}
	if st := h.Runtime().Router.Providers()[0].Accounts[0].Status(); st.CooldownUntil == nil {
		t.Fatalf("denied key not benched: %+v", st)
	}
}

func TestBedrockStreamFailuresBeforeContentFailOver(t *testing.T) {
	cases := map[string][]byte{
		// An exception frame right after message_start.
		"exception": upstream.AppendBedrockException(bedrockFrames(strings.SplitAfter(anthropicOK, "\n\n")[0]), "serviceUnavailableException", "Bedrock is unable to process your request."),
		// A corrupt frame (bad CRC) before any content.
		"crc": func() []byte {
			b := bedrockFrames(anthropicOK)
			b[30] ^= 0xff
			return b
		}(),
	}
	for name, frames := range cases {
		t.Run(name, func(t *testing.T) {
			bad := newMock(t, func(_ int64, w http.ResponseWriter, r *http.Request) { eventStreamWrite(w, frames) })
			good := newMock(t, func(_ int64, w http.ResponseWriter, r *http.Request) { eventStreamWrite(w, bedrockFrames(anthropicOK)) })
			p1, p2 := bedrockProvider("bad", bad.URL, "k-bad"), bedrockProvider("good", good.URL, "k-good")
			p2.Priority = 1
			s, _ := front(t, testCfg(p1, p2))
			resp, body := post(t, s.URL+"/v1/messages", anthropicReq)
			if resp.StatusCode != 200 || body != anthropicOK || bad.hits.Load() != 1 || good.hits.Load() != 1 {
				t.Fatalf("status %d hits %d/%d body %q", resp.StatusCode, bad.hits.Load(), good.hits.Load(), body)
			}
		})
	}
}

func TestBedrockExceptionAfterContentIsInband(t *testing.T) {
	parts := strings.SplitAfter(anthropicOK, "\n\n")
	frames := upstream.AppendBedrockException(bedrockFrames(strings.Join(parts[:3], "")), "modelStreamErrorException", "The model stopped")
	up := newMock(t, func(_ int64, w http.ResponseWriter, r *http.Request) { eventStreamWrite(w, frames) })
	s, _ := front(t, testCfg(bedrockProvider("br", up.URL, "k")))
	resp, body := post(t, s.URL+"/v1/messages", anthropicReq)
	if resp.StatusCode != 200 || !strings.Contains(body, "Hello") || !strings.Contains(body, "event: error") || !strings.Contains(body, "modelStreamErrorException: The model stopped") {
		t.Fatalf("status %d body %q", resp.StatusCode, body)
	}
}

const geminiOK = `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"Hello"}]},"index":0}],"modelVersion":"gemini-2.5-pro"}` + "\n\n" +
	`data: {"candidates":[{"content":{"role":"model","parts":[{"text":""}]},"finishReason":"STOP","index":0}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":1,"totalTokenCount":4},"modelVersion":"gemini-2.5-pro"}` + "\n\n"

func TestVertexServiceAccount(t *testing.T) {
	ts := cloudtest.NewTokenServer(t)
	sa := cloudtest.WriteServiceAccount(t, "proj-e2e", ts.TokenURL())
	const prefix = "/v1/projects/proj-e2e/locations/us-east5/publishers/"
	var fail401 atomic.Bool
	up := newMock(t, func(_ int64, w http.ResponseWriter, r *http.Request) {
		if fail401.Swap(false) {
			http.Error(w, `{"error":{"code":401,"message":"Request had invalid authentication credentials.","status":"UNAUTHENTICATED"}}`, 401)
			return
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ya29.test-") {
			http.Error(w, `{"error":{"code":401,"message":"no token"}}`, 401)
			return
		}
		body, _ := io.ReadAll(r.Body)
		switch r.URL.Path {
		case prefix + "anthropic/models/claude-sonnet-4-5@20250929:streamRawPredict":
			if gjson.GetBytes(body, "anthropic_version").Str != "vertex-2023-10-16" || gjson.GetBytes(body, "model").Exists() || !gjson.GetBytes(body, "stream").Bool() {
				http.Error(w, `{"error":{"code":400,"message":"bad body"}}`, 400)
				return
			}
			sseWrite(w, anthropicOK)
		case prefix + "anthropic/models/claude-sonnet-4-5@20250929:rawPredict":
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, anthropicMsg)
		case prefix + "google/models/gemini-2.5-pro:streamGenerateContent":
			if r.URL.RawQuery != "alt=sse" {
				http.Error(w, `{"error":{"code":400,"message":"want alt=sse"}}`, 400)
				return
			}
			sseWrite(w, geminiOK)
		default:
			http.Error(w, `{"error":{"code":404,"message":"unexpected path `+r.URL.Path+`"}}`, 404)
		}
	})
	p := config.Provider{ID: "vx", Type: "vertex", BaseURL: up.URL, Region: "us-east5", Keys: []string{"sa:" + sa},
		ModelMap: map[string]string{"claude-x": "claude-sonnet-4-5@20250929", "gem": "gemini-2.5-pro"}}
	s, h := front(t, testCfg(p))

	// Claude on Vertex: Anthropic client streams byte-exact.
	resp, body := post(t, s.URL+"/v1/messages", anthropicReq)
	if resp.StatusCode != 200 || body != anthropicOK {
		t.Fatalf("claude stream: status %d body %q", resp.StatusCode, body)
	}
	resp, body = post(t, s.URL+"/v1/messages", strings.Replace(anthropicReq, `"stream":true`, `"stream":false`, 1))
	if resp.StatusCode != 200 || body != anthropicMsg {
		t.Fatalf("claude non-stream: status %d body %q", resp.StatusCode, body)
	}
	// Chat client to Claude on Vertex (translated, upstream streams).
	resp, body = post(t, s.URL+"/v1/chat/completions", chatReqNoStream)
	if resp.StatusCode != 200 || gjson.Get(body, "choices.0.message.content").Str != "Hello" {
		t.Fatalf("claude via chat: status %d body %q", resp.StatusCode, body)
	}
	// Gemini on Vertex through the chat and anthropic client formats.
	resp, body = post(t, s.URL+"/v1/chat/completions", strings.Replace(chatReq, "claude-x", "gem", 1))
	if resp.StatusCode != 200 || !strings.Contains(body, `"content":"Hello"`) || !strings.HasSuffix(body, "data: [DONE]\n\n") {
		t.Fatalf("gemini via chat: status %d body %q", resp.StatusCode, body)
	}
	resp, body = post(t, s.URL+"/v1/messages", strings.Replace(anthropicReq, "claude-x", "gem", 1))
	if resp.StatusCode != 200 || !strings.Contains(body, `"text":"Hello"`) || !strings.Contains(body, "event: message_stop") {
		t.Fatalf("gemini via anthropic: status %d body %q", resp.StatusCode, body)
	}
	if ts.Hits.Load() != 1 {
		t.Fatalf("token endpoint hit %d times, want 1 (cached)", ts.Hits.Load())
	}
	if st := h.Runtime().Router.Providers()[0].Accounts[0].Status(); st.Kind != "service_account" || st.Meta["project"] != "proj-e2e" {
		t.Fatalf("account status %+v", st)
	}

	// A 401 refreshes the token once and retries the same account.
	fail401.Store(true)
	resp, body = post(t, s.URL+"/v1/messages", anthropicReq)
	if resp.StatusCode != 200 || body != anthropicOK || ts.Hits.Load() != 2 {
		t.Fatalf("after 401: status %d token hits %d body %q", resp.StatusCode, ts.Hits.Load(), body)
	}
}

func TestVertexServiceAccountTokenFailure(t *testing.T) {
	ts := cloudtest.NewTokenServer(t)
	path := cloudtest.WriteServiceAccount(t, "proj-x", ts.TokenURL())
	ts.Close() // token endpoint down
	up := newMock(t, func(_ int64, w http.ResponseWriter, r *http.Request) { sseWrite(w, anthropicOK) })
	p := config.Provider{ID: "vx", Type: "vertex", BaseURL: up.URL, Keys: []string{"sa:" + path}, Models: []string{"claude-x"}}
	s, _ := front(t, testCfg(p))
	resp, body := post(t, s.URL+"/v1/messages", anthropicReq)
	if resp.StatusCode != 401 || up.hits.Load() != 0 || strings.Contains(body, "PRIVATE KEY") {
		t.Fatalf("status %d hits %d body %q", resp.StatusCode, up.hits.Load(), body)
	}
}

const chatOK = `data: {"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"Hello"},"finish_reason":null}]}` + "\n\n" +
	`data: {"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}` + "\n\n" +
	"data: [DONE]\n\n"

func TestAzureOpenAI(t *testing.T) {
	up := newMock(t, func(_ int64, w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.Header.Get("Api-Key") != "azure-key" || r.Header.Get("Authorization") != "" {
			http.Error(w, `{"error":{"code":"401","message":"Access denied due to invalid subscription key."}}`, 401)
			return
		}
		if gjson.GetBytes(body, "model").Str != "my-gpt-deploy" {
			http.Error(w, `{"error":{"code":"DeploymentNotFound","message":"The API deployment for this resource does not exist."}}`, 404)
			return
		}
		switch r.URL.Path {
		case "/openai/v1/chat/completions":
			if !gjson.GetBytes(body, "stream").Bool() {
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, `{"id":"c","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"Hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`)
				return
			}
			sseWrite(w, chatOK)
		case "/openai/v1/responses":
			sseWrite(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":3,\"output_tokens\":1}}}\n\n")
		default:
			http.Error(w, `{"error":{"code":"404","message":"Resource not found"}}`, 404)
		}
	})
	p := config.Provider{ID: "az", Type: "azure-openai", BaseURL: up.URL, Keys: []string{"azure-key"}, ModelMap: map[string]string{"gpt-x": "my-gpt-deploy"}}
	s, _ := front(t, testCfg(p))
	req := strings.Replace(chatReq, "claude-x", "gpt-x", 1)

	resp, body := post(t, s.URL+"/v1/chat/completions", req)
	if resp.StatusCode != 200 || body != chatOK {
		t.Fatalf("chat passthrough: status %d body %q", resp.StatusCode, body)
	}
	resp, body = post(t, s.URL+"/v1/chat/completions", strings.Replace(chatReqNoStream, "claude-x", "gpt-x", 1))
	if resp.StatusCode != 200 || gjson.Get(body, "choices.0.message.content").Str != "Hello" {
		t.Fatalf("chat non-stream: status %d body %q", resp.StatusCode, body)
	}
	resp, body = post(t, s.URL+"/v1/responses", `{"model":"gpt-x","stream":true,"input":"hi"}`)
	if resp.StatusCode != 200 || !strings.Contains(body, "event: response.completed") {
		t.Fatalf("responses passthrough: status %d body %q", resp.StatusCode, body)
	}
	// Anthropic client, translated to chat.
	resp, body = post(t, s.URL+"/v1/messages", strings.Replace(anthropicReq, "claude-x", "gpt-x", 1))
	if resp.StatusCode != 200 || !strings.Contains(body, `"text":"Hello"`) || !strings.Contains(body, "event: message_stop") {
		t.Fatalf("anthropic via azure: status %d body %q", resp.StatusCode, body)
	}
	resp, body = post(t, s.URL+"/v1/messages", strings.Replace(strings.Replace(anthropicReq, "claude-x", "gpt-x", 1), `"stream":true`, `"stream":false`, 1))
	var msg struct {
		Content []struct{ Text string } `json:"content"`
	}
	if resp.StatusCode != 200 || json.Unmarshal([]byte(body), &msg) != nil || len(msg.Content) == 0 || msg.Content[0].Text != "Hello" {
		t.Fatalf("anthropic non-stream via azure: status %d body %q", resp.StatusCode, body)
	}
}

func TestOllamaWithoutKey(t *testing.T) {
	up := newMock(t, func(_ int64, w http.ResponseWriter, r *http.Request) {
		if _, ok := r.Header["Authorization"]; ok || r.URL.Path != "/v1/chat/completions" {
			http.Error(w, `{"error":{"message":"unexpected auth or path"}}`, 400)
			return
		}
		sseWrite(w, chatOK)
	})
	p := config.Provider{ID: "local", Type: "ollama", BaseURL: up.URL + "/v1"}
	s, _ := front(t, testCfg(p))
	// "qwen3-coder" is in no known family, so the model-less ollama
	// provider serves it.
	resp, body := post(t, s.URL+"/v1/messages", strings.Replace(anthropicReq, "claude-x", "qwen3-coder", 1))
	if resp.StatusCode != 200 || !strings.Contains(body, `"text":"Hello"`) || !strings.Contains(body, "event: message_stop") {
		t.Fatalf("status %d body %q", resp.StatusCode, body)
	}
	resp, body = post(t, s.URL+"/v1/chat/completions", strings.Replace(chatReq, "claude-x", "qwen3-coder", 1))
	if resp.StatusCode != 200 || body != chatOK {
		t.Fatalf("chat passthrough: status %d body %q", resp.StatusCode, body)
	}
}
