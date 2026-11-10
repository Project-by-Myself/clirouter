// Package responses translates the OpenAI Responses API wire format
// (POST /v1/responses) to and from the IR, both as a client format (Codex CLI)
// and as an upstream format (api.openai.com and the ChatGPT Codex backend).
package responses

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"

	"github.com/Project-by-Myself/clirouter/internal/ir"
	"github.com/Project-by-Myself/clirouter/internal/sse"
)

const envelopePrefix = "clr1."

// redactedSuffix marks an envelope that carries opaque redacted thinking.
const redactedSuffix = "-redacted"

type envelope struct {
	S string `json:"s"`
	G string `json:"g"`
	T string `json:"t"`
}

// EncodeEnvelope wraps a non-OpenAI reasoning signature so it survives a
// round trip through a Responses client as encrypted_content.
func EncodeEnvelope(source, sig, text string) string {
	b, _ := json.Marshal(envelope{S: source, G: sig, T: text})
	return envelopePrefix + base64.RawURLEncoding.EncodeToString(b)
}

// DecodeEnvelope reverses EncodeEnvelope. ok is false for anything else,
// including genuine OpenAI encrypted_content.
func DecodeEnvelope(s string) (source, sig, text string, ok bool) {
	if !strings.HasPrefix(s, envelopePrefix) {
		return "", "", "", false
	}
	payload := strings.TrimRight(s[len(envelopePrefix):], "=")
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return "", "", "", false
	}
	var e envelope
	if json.Unmarshal(raw, &e) != nil {
		return "", "", "", false
	}
	return e.S, e.G, e.T, true
}

func Ping() []byte { return []byte(": keepalive\n\n") }

func ErrorBody(status int, errType, msg string) []byte {
	if errType == "" {
		errType = defaultErrType(status)
	}
	b := append([]byte(nil), `{"error":{"message":`...)
	b = appendJSONString(b, msg)
	b = appendStrField(b, "type", errType)
	b = appendStrField(b, "code", errType)
	return append(b, '}', '}')
}

func defaultErrType(status int) string {
	switch {
	case status == 401:
		return "authentication_error"
	case status == 403:
		return "permission_error"
	case status == 404:
		return "not_found_error"
	case status == 429:
		return "rate_limit_exceeded"
	case status >= 500:
		return "server_error"
	default:
		return "invalid_request_error"
	}
}

func StreamErrorEvent(errType, msg string) []byte {
	b := append([]byte(nil), `{"type":"response.failed","sequence_number":0,"response":`...)
	b = appendResponseObject(b, respHeader{id: newID("resp_"), created: time.Now().Unix(), status: "failed", errCode: errType, errMsg: msg}, nil, nil)
	b = append(b, '}')
	return sse.AppendEvent(nil, "response.failed", b)
}

type upUsage struct {
	InputTokens        int64 `json:"input_tokens"`
	OutputTokens       int64 `json:"output_tokens"`
	InputTokensDetails struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"input_tokens_details"`
	OutputTokensDetails struct {
		ReasoningTokens int64 `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
}

func (u *upUsage) ir() *ir.Usage {
	return &ir.Usage{
		InputTokens:     u.InputTokens,
		OutputTokens:    u.OutputTokens,
		CacheReadTokens: u.InputTokensDetails.CachedTokens,
		ReasoningTokens: u.OutputTokensDetails.ReasoningTokens,
	}
}

type upError struct {
	Code    string `json:"code"`
	Type    string `json:"type"`
	Message string `json:"message"`
}

func (e *upError) kind() string {
	if e.Code != "" {
		return e.Code
	}
	return e.Type
}

type upResponse struct {
	ID                string   `json:"id"`
	Model             string   `json:"model"`
	Status            string   `json:"status"`
	Usage             *upUsage `json:"usage"`
	Error             *upError `json:"error"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
}

func UsageFromEvent(name, data []byte) (*ir.Usage, bool) {
	switch eventType(name, data) {
	case "response.completed", "response.incomplete":
	default:
		return nil, false
	}
	var ev struct {
		Response *upResponse `json:"response"`
	}
	if json.Unmarshal(data, &ev) != nil || ev.Response == nil || ev.Response.Usage == nil {
		return nil, false
	}
	return ev.Response.Usage.ir(), true
}

func IsErrorEvent(name, data []byte) (errType, msg string, ok bool) {
	t := eventType(name, data)
	if t != "error" && t != "response.failed" {
		return "", "", false
	}
	var ev upEvent
	_ = json.Unmarshal(data, &ev)
	errType, msg = ev.errorInfo(t)
	return errType, msg, true
}

func IsTerminal(name, data []byte) bool {
	switch eventType(name, data) {
	case "response.completed", "response.incomplete", "response.failed":
		return true
	}
	return false
}
