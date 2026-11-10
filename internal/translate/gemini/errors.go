package gemini

import (
	"bytes"
	"encoding/json"

	"github.com/Project-by-Myself/clirouter/internal/ir"
)

// Ping returns nil: Gemini clients (google-genai SDKs, Gemini CLI) parse
// every SSE frame as JSON and do not reliably tolerate comment frames, so no
// keepalive is injected for this format.
func Ping() []byte { return nil }

func statusFromCode(code int) string {
	switch code {
	case 400, 409, 412, 413, 422:
		return "INVALID_ARGUMENT"
	case 401:
		return "UNAUTHENTICATED"
	case 403:
		return "PERMISSION_DENIED"
	case 404:
		return "NOT_FOUND"
	case 429:
		return "RESOURCE_EXHAUSTED"
	case 502, 503, 504, 529:
		return "UNAVAILABLE"
	}
	if code >= 400 && code < 500 {
		return "INVALID_ARGUMENT"
	}
	return "INTERNAL"
}

func codeFromErrType(t string) int {
	switch t {
	case "invalid_request_error", "invalid_request", "content_filter", "bad_request":
		return 400
	case "authentication_error":
		return 401
	case "permission_error":
		return 403
	case "not_found_error":
		return 404
	case "rate_limit_error", "rate_limit_exceeded", "insufficient_quota":
		return 429
	case "overloaded_error", "service_unavailable":
		return 503
	}
	return 500
}

func errTypeFromStatus(status string, code int) string {
	switch status {
	case "INVALID_ARGUMENT", "FAILED_PRECONDITION", "OUT_OF_RANGE":
		return "invalid_request_error"
	case "UNAUTHENTICATED":
		return "authentication_error"
	case "PERMISSION_DENIED":
		return "permission_error"
	case "NOT_FOUND":
		return "not_found_error"
	case "RESOURCE_EXHAUSTED":
		return "rate_limit_error"
	case "UNAVAILABLE", "DEADLINE_EXCEEDED":
		return "overloaded_error"
	case "":
		if code != 0 {
			return errTypeFromStatus(statusFromCode(code), 0)
		}
	}
	return "api_error"
}

// ErrorBody renders a Google API error envelope.
func ErrorBody(status int, errType, msg string) []byte {
	_ = errType
	dst := make([]byte, 0, len(msg)+80)
	dst = append(dst, `{"error":{"code":`...)
	dst = appendInt(dst, int64(status))
	dst = append(dst, `,"message":`...)
	dst = appendString(dst, msg)
	dst = append(dst, `,"status":"`...)
	dst = append(dst, statusFromCode(status)...)
	return append(dst, `"}}`...)
}

// StreamErrorEvent renders a mid-stream error as a data-only SSE frame.
func StreamErrorEvent(errType, msg string) []byte {
	body := ErrorBody(codeFromErrType(errType), errType, msg)
	out := make([]byte, 0, len(body)+8)
	out = append(out, "data: "...)
	out = append(out, body...)
	return append(out, '\n', '\n')
}

// UsageFromEvent extracts usageMetadata from an upstream chunk.
func UsageFromEvent(_, data []byte) (*ir.Usage, bool) {
	if !bytes.Contains(data, []byte(`"usageMetadata"`)) {
		return nil, false
	}
	var r gResponse
	if json.Unmarshal(data, &r) != nil {
		return nil, false
	}
	if u := r.unwrap().UsageMetadata; u != nil {
		return usageFromG(u), true
	}
	return nil, false
}

// IsErrorEvent reports an error envelope or a blocked prompt.
func IsErrorEvent(_, data []byte) (string, string, bool) {
	if !bytes.Contains(data, []byte(`"error"`)) && !bytes.Contains(data, []byte(`"blockReason"`)) {
		return "", "", false
	}
	var r gResponse
	if json.Unmarshal(data, &r) != nil {
		return "", "", false
	}
	x := r.unwrap()
	if x.Error != nil {
		return errTypeFromStatus(x.Error.Status, x.Error.Code), x.Error.Message, true
	}
	if len(x.Candidates) == 0 && x.PromptFeedback != nil && x.PromptFeedback.BlockReason != "" {
		return "content_filter", "prompt blocked: " + x.PromptFeedback.BlockReason, true
	}
	return "", "", false
}

// IsTerminal reports whether the chunk carries a candidate finishReason.
func IsTerminal(_, data []byte) bool {
	if !bytes.Contains(data, []byte(`"finishReason"`)) {
		return false
	}
	var r gResponse
	if json.Unmarshal(data, &r) != nil {
		return false
	}
	for _, c := range r.unwrap().Candidates {
		if c.FinishReason != "" && c.FinishReason != "FINISH_REASON_UNSPECIFIED" {
			return true
		}
	}
	return false
}
