package anthropic

import (
	"bytes"
	"encoding/json"
	"errors"

	"github.com/Project-by-Myself/clirouter/internal/ir"
	"github.com/Project-by-Myself/clirouter/internal/sse"
)

type outUsage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
}

type outResponse struct {
	ID           string     `json:"id"`
	Type         string     `json:"type"`
	Role         string     `json:"role"`
	Model        string     `json:"model"`
	Content      []outBlock `json:"content"`
	StopReason   string     `json:"stop_reason"`
	StopSequence *string    `json:"stop_sequence"`
	Usage        outUsage   `json:"usage"`
}

// EncodeResponse renders an aggregated assistant turn as a non-streaming
// Messages API response.
func EncodeResponse(req *ir.Request, resp *ir.Response) ([]byte, error) {
	if resp == nil {
		return nil, errors.New("anthropic: nil response")
	}
	custom := map[string]bool{}
	model := resp.Model
	if req != nil {
		if req.Model != "" {
			model = req.Model
		}
		for _, t := range req.Tools {
			if t.Kind == ir.ToolCustom {
				custom[t.Name] = true
			}
		}
	}
	out := outResponse{
		ID:      resp.ID,
		Type:    "message",
		Role:    "assistant",
		Model:   model,
		Content: make([]outBlock, 0, len(resp.Content)),
	}
	if out.ID == "" {
		out.ID = newID("msg_")
	}
	sawTool := false
	for _, p := range resp.Content {
		switch p.Type {
		case ir.PartText:
			if p.Text != "" {
				out.Content = append(out.Content, outBlock{Type: "text", Text: strPtr(p.Text)})
			}
		case ir.PartThinking:
			out.Content = append(out.Content, outBlock{Type: "thinking", Thinking: strPtr(p.Text), Signature: strPtr(encodeSig(p.SigSource, p.Signature))})
		case ir.PartRedactedThinking:
			if p.Signature != "" {
				out.Content = append(out.Content, outBlock{Type: "redacted_thinking", Data: encodeSig(p.SigSource, p.Signature)})
			}
		case ir.PartToolCall:
			sawTool = true
			id := p.ToolID
			if id == "" {
				id = newID("toolu_")
			}
			out.Content = append(out.Content, outBlock{
				Type:  "tool_use",
				ID:    id,
				Name:  p.ToolName,
				Input: toolInput(p.Args, p.ToolKind == ir.ToolCustom || custom[p.ToolName]),
			})
		}
	}
	out.StopReason = stopReason(resp.Finish, sawTool)
	u := resp.Usage
	out.Usage = outUsage{
		InputTokens:              max(u.InputTokens-u.CacheReadTokens-u.CacheWriteTokens, 0),
		OutputTokens:             u.OutputTokens,
		CacheReadInputTokens:     u.CacheReadTokens,
		CacheCreationInputTokens: u.CacheWriteTokens,
	}
	return marshalNoEscape(&out)
}

// Ping is the keepalive frame clients of the Messages API understand.
func Ping() []byte {
	return []byte("event: ping\ndata: {\"type\":\"ping\"}\n\n")
}

func errTypeForStatus(status int) string {
	switch status {
	case 400:
		return "invalid_request_error"
	case 401:
		return "authentication_error"
	case 403:
		return "permission_error"
	case 404:
		return "not_found_error"
	case 413:
		return "request_too_large"
	case 429:
		return "rate_limit_error"
	case 529:
		return "overloaded_error"
	}
	return "api_error"
}

func appendErrorJSON(b []byte, errType, msg string) []byte {
	b = append(b, `{"type":"error","error":{"type":`...)
	b = appendJSONString(b, errType)
	b = append(b, `,"message":`...)
	b = appendJSONString(b, msg)
	return append(b, '}', '}')
}

// ErrorBody is a non-streaming Messages API error body.
func ErrorBody(status int, errType, msg string) []byte {
	if errType == "" {
		errType = errTypeForStatus(status)
	}
	return appendErrorJSON(make([]byte, 0, 64+len(msg)), errType, msg)
}

// StreamErrorEvent is an in-band SSE error frame.
func StreamErrorEvent(errType, msg string) []byte {
	if errType == "" {
		errType = "api_error"
	}
	data := appendErrorJSON(make([]byte, 0, 64+len(msg)), errType, msg)
	return sse.AppendEvent(make([]byte, 0, len(data)+16), "error", data)
}

// eventType returns the SSE event name, or the data's top-level "type" when
// the name is absent.
func eventType(name, data []byte) string {
	if len(name) > 0 {
		return string(name)
	}
	var t struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(data, &t) != nil {
		return ""
	}
	return t.Type
}

// UsageFromEvent extracts usage from message_start / message_delta events
// (passthrough streams) or from a whole non-streaming message.
func UsageFromEvent(name, data []byte) (*ir.Usage, bool) {
	if len(name) > 0 && string(name) != "message_start" && string(name) != "message_delta" {
		return nil, false
	}
	if !bytes.Contains(data, []byte(`"usage"`)) {
		return nil, false
	}
	var ev struct {
		Type    string `json:"type"`
		Message *struct {
			Usage *wireUsage `json:"usage"`
		} `json:"message"`
		Usage *wireUsage `json:"usage"`
	}
	if json.Unmarshal(data, &ev) != nil {
		return nil, false
	}
	switch ev.Type {
	case "message_start":
		if ev.Message != nil && ev.Message.Usage != nil {
			return ev.Message.Usage.toIR(), true
		}
	case "message_delta", "message": // "message" is a whole non-streaming response
		if ev.Usage != nil {
			return ev.Usage.toIR(), true
		}
	}
	return nil, false
}

// IsErrorEvent detects an in-band "event: error" frame.
func IsErrorEvent(name, data []byte) (errType, msg string, ok bool) {
	if len(name) > 0 && string(name) != "error" {
		return "", "", false
	}
	if len(name) == 0 && !bytes.Contains(data, []byte(`"error"`)) {
		return "", "", false
	}
	var ev struct {
		Type  string     `json:"type"`
		Error *wireError `json:"error"`
	}
	if json.Unmarshal(data, &ev) != nil {
		if len(name) > 0 {
			return "api_error", string(data), true
		}
		return "", "", false
	}
	if len(name) == 0 && ev.Type != "error" {
		return "", "", false
	}
	errType, msg = "api_error", "upstream error"
	if ev.Error != nil {
		if ev.Error.Type != "" {
			errType = ev.Error.Type
		}
		if ev.Error.Message != "" {
			msg = ev.Error.Message
		}
	}
	return errType, msg, true
}

// IsTerminal reports whether the event is message_stop.
func IsTerminal(name, data []byte) bool {
	if len(name) > 0 {
		return string(name) == "message_stop"
	}
	return bytes.Contains(data, []byte(`"message_stop"`)) && eventType(name, data) == "message_stop"
}
