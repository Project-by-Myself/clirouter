package openai

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"strings"
	"time"

	"github.com/Project-by-Myself/clirouter/internal/ir"
)

type respMessage struct {
	Role             string        `json:"role"`
	Content          *string       `json:"content"`
	ReasoningContent string        `json:"reasoning_content,omitempty"`
	ToolCalls        []outToolCall `json:"tool_calls,omitempty"`
}

type respChoice struct {
	Index        int         `json:"index"`
	Message      respMessage `json:"message"`
	FinishReason string      `json:"finish_reason"`
}

type respUsage struct {
	PromptTokens        int64 `json:"prompt_tokens"`
	CompletionTokens    int64 `json:"completion_tokens"`
	TotalTokens         int64 `json:"total_tokens"`
	PromptTokensDetails struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionTokensDetails struct {
		ReasoningTokens int64 `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

type respBody struct {
	ID      string       `json:"id"`
	Object  string       `json:"object"`
	Created int64        `json:"created"`
	Model   string       `json:"model"`
	Choices []respChoice `json:"choices"`
	Usage   respUsage    `json:"usage"`
}

// EncodeResponse renders an aggregated turn as a chat.completion object.
func EncodeResponse(req *ir.Request, resp *ir.Response) ([]byte, error) {
	out := respBody{ID: resp.ID, Object: "chat.completion", Created: time.Now().Unix(), Model: resp.Model}
	if req != nil && req.Model != "" {
		out.Model = req.Model
	}
	if out.ID == "" {
		out.ID = "chatcmpl-" + rand.Text()
	}
	var text, thinking strings.Builder
	msg := respMessage{Role: "assistant"}
	for _, p := range resp.Content {
		switch p.Type {
		case ir.PartText:
			text.WriteString(p.Text)
		case ir.PartThinking:
			thinking.WriteString(p.Text)
		case ir.PartToolCall:
			args := p.Args
			if p.ToolKind == ir.ToolCustom {
				args = customArgsJSON(p.Args)
			} else if strings.TrimSpace(args) == "" {
				args = "{}"
			}
			msg.ToolCalls = append(msg.ToolCalls, outToolCall{ID: p.ToolID, Type: "function", Function: outFunc{Name: p.ToolName, Arguments: args}})
		}
	}
	if text.Len() > 0 {
		s := text.String()
		msg.Content = &s
	}
	msg.ReasoningContent = thinking.String()
	finish := string(resp.Finish)
	switch resp.Finish {
	case ir.FinishLength, ir.FinishToolCalls, ir.FinishContentFilter:
	default:
		finish = "stop"
		if len(msg.ToolCalls) > 0 {
			finish = "tool_calls"
		}
	}
	out.Choices = []respChoice{{Message: msg, FinishReason: finish}}
	u := &resp.Usage
	out.Usage.PromptTokens = u.InputTokens
	out.Usage.CompletionTokens = u.OutputTokens
	out.Usage.TotalTokens = u.InputTokens + u.OutputTokens
	out.Usage.PromptTokensDetails.CachedTokens = u.CacheReadTokens
	out.Usage.CompletionTokensDetails.ReasoningTokens = u.ReasoningTokens
	return marshalNoEscape(&out)
}

// Ping is an SSE comment used as a keepalive.
func Ping() []byte { return []byte(": ping\n\n") }

func typeForStatus(status int) string {
	switch {
	case status == 400 || status == 413 || status == 422:
		return "invalid_request_error"
	case status == 401:
		return "authentication_error"
	case status == 403:
		return "permission_error"
	case status == 404:
		return "not_found_error"
	case status == 429:
		return "rate_limit_error"
	case status == 529 || status == 503:
		return "overloaded_error"
	case status >= 500:
		return "server_error"
	}
	return "api_error"
}

func appendErrorObject(dst []byte, errType, msg string) []byte {
	dst = append(dst, `{"error":{"message":`...)
	dst = appendJSONString(dst, msg)
	dst = append(dst, `,"type":`...)
	dst = appendJSONString(dst, errType)
	dst = append(dst, `,"code":`...)
	dst = appendJSONString(dst, errType)
	return append(dst, "}}"...)
}

// ErrorBody renders a non-streaming OpenAI-style error body.
func ErrorBody(status int, errType, msg string) []byte {
	if errType == "" {
		errType = typeForStatus(status)
	}
	return appendErrorObject(make([]byte, 0, 64+len(msg)), errType, msg)
}

func appendStreamError(dst []byte, errType, msg string) []byte {
	if errType == "" {
		errType = "api_error"
	}
	dst = append(dst, "data: "...)
	dst = appendErrorObject(dst, errType, msg)
	return append(dst, "\n\ndata: [DONE]\n\n"...)
}

// StreamErrorEvent renders an in-stream error frame followed by [DONE].
func StreamErrorEvent(errType, msg string) []byte {
	return appendStreamError(make([]byte, 0, 96+len(msg)), errType, msg)
}

var (
	usageKey = []byte(`"usage"`)
	errorKey = []byte(`"error"`)
)

// UsageFromEvent extracts usage from a raw upstream chunk, for passthrough.
func UsageFromEvent(name, data []byte) (*ir.Usage, bool) {
	if !bytes.Contains(data, usageKey) {
		return nil, false
	}
	var c struct {
		Usage *chatUsage `json:"usage"`
	}
	if json.Unmarshal(data, &c) != nil || c.Usage == nil {
		return nil, false
	}
	return c.Usage.toIR(), true
}

// IsErrorEvent reports whether a raw upstream chunk is an error frame.
func IsErrorEvent(name, data []byte) (errType, msg string, ok bool) {
	if !bytes.Contains(data, errorKey) {
		return "", "", false
	}
	var c struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(data, &c) != nil || isNull(c.Error) {
		return "", "", false
	}
	errType, msg = parseErrorValue(c.Error)
	return errType, msg, true
}

// IsTerminal reports whether data is the [DONE] sentinel.
func IsTerminal(name, data []byte) bool {
	return bytes.Equal(bytes.TrimSpace(data), doneMarker)
}
