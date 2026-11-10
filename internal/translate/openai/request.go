// Package openai translates the OpenAI Chat Completions wire format
// (POST /v1/chat/completions) to and from the canonical IR.
package openai

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Project-by-Myself/clirouter/internal/ir"
)

type chatRequest struct {
	Model               string          `json:"model"`
	Messages            []chatMessage   `json:"messages"`
	Tools               []chatTool      `json:"tools"`
	ToolChoice          json.RawMessage `json:"tool_choice"`
	MaxTokens           int             `json:"max_tokens"`
	MaxCompletionTokens int             `json:"max_completion_tokens"`
	Temperature         *float64        `json:"temperature"`
	TopP                *float64        `json:"top_p"`
	Stop                json.RawMessage `json:"stop"`
	Stream              bool            `json:"stream"`
	StreamOptions       *struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options"`
	ReasoningEffort string `json:"reasoning_effort"`
	Reasoning       *struct {
		Effort    string `json:"effort"`
		MaxTokens int    `json:"max_tokens"`
		Enabled   *bool  `json:"enabled"`
	} `json:"reasoning"`
	ResponseFormat    json.RawMessage `json:"response_format"`
	ParallelToolCalls *bool           `json:"parallel_tool_calls"`
	User              string          `json:"user"`
	PromptCacheKey    string          `json:"prompt_cache_key"`
}

type chatMessage struct {
	Role             string          `json:"role"`
	Content          json.RawMessage `json:"content"`
	ReasoningContent json.RawMessage `json:"reasoning_content"`
	Reasoning        json.RawMessage `json:"reasoning"`
	ToolCalls        []chatToolCall  `json:"tool_calls"`
	ToolCallID       string          `json:"tool_call_id"`
}

type chatToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"function"`
}

type chatTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

type chatContentPart struct {
	Type     string          `json:"type"`
	Text     string          `json:"text"`
	Refusal  string          `json:"refusal"`
	ImageURL json.RawMessage `json:"image_url"`
	File     *struct {
		FileData string `json:"file_data"`
		Filename string `json:"filename"`
		FileID   string `json:"file_id"`
	} `json:"file"`
}

// DecodeRequest parses a Chat Completions request body into IR.
func DecodeRequest(body []byte) (*ir.Request, error) {
	var cr chatRequest
	if err := json.Unmarshal(body, &cr); err != nil {
		return nil, fmt.Errorf("openai: invalid request body: %w", err)
	}
	req := &ir.Request{
		Model:             cr.Model,
		Temperature:       cr.Temperature,
		TopP:              cr.TopP,
		Stream:            cr.Stream,
		ParallelToolCalls: cr.ParallelToolCalls,
		User:              cr.User,
		PromptCacheKey:    cr.PromptCacheKey,
		Messages:          make([]ir.Message, 0, len(cr.Messages)),
	}
	if !isNull(cr.ResponseFormat) {
		req.ResponseFormat = cr.ResponseFormat
	}
	if cr.StreamOptions != nil {
		req.IncludeUsage = cr.StreamOptions.IncludeUsage
	}
	req.MaxTokens = cr.MaxTokens
	if cr.MaxCompletionTokens > 0 {
		req.MaxTokens = cr.MaxCompletionTokens
	}
	if !isNull(cr.Stop) {
		if s, ok := rawString(cr.Stop); ok {
			if s != "" {
				req.Stop = []string{s}
			}
		} else if err := json.Unmarshal(cr.Stop, &req.Stop); err != nil {
			return nil, fmt.Errorf("openai: invalid stop: %w", err)
		}
	}
	switch {
	case cr.ReasoningEffort == "none":
		req.Reasoning = &ir.Reasoning{Disabled: true}
	case cr.ReasoningEffort != "":
		req.Reasoning = &ir.Reasoning{Effort: cr.ReasoningEffort}
	case cr.Reasoning != nil:
		r := &ir.Reasoning{Effort: cr.Reasoning.Effort, BudgetTokens: cr.Reasoning.MaxTokens}
		if r.Effort == "none" || (cr.Reasoning.Enabled != nil && !*cr.Reasoning.Enabled) {
			r = &ir.Reasoning{Disabled: true}
		}
		if r.Effort != "" || r.BudgetTokens > 0 || r.Disabled {
			req.Reasoning = r
		}
	}

	for _, t := range cr.Tools {
		if t.Type != "function" && t.Type != "" {
			continue
		}
		if t.Function.Name == "" {
			continue
		}
		tool := ir.Tool{Name: t.Function.Name, Description: t.Function.Description}
		if !isNull(t.Function.Parameters) {
			tool.Schema = t.Function.Parameters
		}
		req.Tools = append(req.Tools, tool)
	}
	if !isNull(cr.ToolChoice) {
		tc, err := decodeToolChoice(cr.ToolChoice)
		if err != nil {
			return nil, err
		}
		req.ToolChoice = tc
	}

	for i := range cr.Messages {
		m := &cr.Messages[i]
		switch m.Role {
		case "system", "developer":
			parts, err := decodeContent(m.Content)
			if err != nil {
				return nil, err
			}
			for _, p := range parts {
				if p.Type == ir.PartText && p.Text != "" {
					req.System = append(req.System, p)
				}
			}
		case "user":
			parts, err := decodeContent(m.Content)
			if err != nil {
				return nil, err
			}
			req.Messages = append(req.Messages, ir.Message{Role: ir.RoleUser, Parts: parts})
		case "assistant":
			var parts []ir.Part
			if s, ok := rawString(m.ReasoningContent); ok && s != "" {
				parts = append(parts, ir.Part{Type: ir.PartThinking, Text: s})
			} else if s, ok := rawString(m.Reasoning); ok && s != "" {
				parts = append(parts, ir.Part{Type: ir.PartThinking, Text: s})
			}
			content, err := decodeContent(m.Content)
			if err != nil {
				return nil, err
			}
			parts = append(parts, content...)
			for _, tc := range m.ToolCalls {
				if tc.Type != "" && tc.Type != "function" {
					continue
				}
				args, ok := rawString(tc.Function.Arguments)
				if !ok && !isNull(tc.Function.Arguments) {
					args = string(tc.Function.Arguments)
				}
				id := tc.ID
				if id == "" {
					id = newCallID()
				}
				parts = append(parts, ir.Part{Type: ir.PartToolCall, ToolID: id, ToolName: tc.Function.Name, Args: args})
			}
			req.Messages = append(req.Messages, ir.Message{Role: ir.RoleAssistant, Parts: parts})
		case "tool":
			result, err := decodeContent(m.Content)
			if err != nil {
				return nil, err
			}
			req.Messages = append(req.Messages, ir.Message{Role: ir.RoleTool, Parts: []ir.Part{{
				Type: ir.PartToolResult, ToolID: m.ToolCallID, Result: result,
			}}})
		}
	}
	return req, nil
}

func decodeToolChoice(raw json.RawMessage) (*ir.ToolChoice, error) {
	if s, ok := rawString(raw); ok {
		switch s {
		case "auto", "none", "required":
			return &ir.ToolChoice{Mode: s}, nil
		case "any":
			return &ir.ToolChoice{Mode: "required"}, nil
		}
		return nil, fmt.Errorf("openai: invalid tool_choice %q", s)
	}
	var obj struct {
		Type     string `json:"type"`
		Name     string `json:"name"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, fmt.Errorf("openai: invalid tool_choice: %w", err)
	}
	name := obj.Function.Name
	if name == "" {
		name = obj.Name
	}
	if name == "" {
		return nil, errors.New("openai: tool_choice function name missing")
	}
	return &ir.ToolChoice{Mode: "tool", Name: name}, nil
}

func decodeContent(raw json.RawMessage) ([]ir.Part, error) {
	if isNull(raw) {
		return nil, nil
	}
	if s, ok := rawString(raw); ok {
		if s == "" {
			return nil, nil
		}
		return []ir.Part{{Type: ir.PartText, Text: s}}, nil
	}
	var items []chatContentPart
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("openai: invalid message content: %w", err)
	}
	parts := make([]ir.Part, 0, len(items))
	for i := range items {
		it := &items[i]
		switch it.Type {
		case "text", "input_text", "output_text":
			parts = append(parts, ir.Part{Type: ir.PartText, Text: it.Text})
		case "refusal":
			parts = append(parts, ir.Part{Type: ir.PartText, Text: it.Refusal})
		case "image_url":
			url, ok := rawString(it.ImageURL)
			if !ok {
				var obj struct {
					URL string `json:"url"`
				}
				if err := json.Unmarshal(it.ImageURL, &obj); err != nil {
					return nil, fmt.Errorf("openai: invalid image_url: %w", err)
				}
				url = obj.URL
			}
			if url == "" {
				continue
			}
			p := ir.Part{Type: ir.PartImage}
			if mt, data, ok := parseDataURL(url); ok {
				p.MediaType, p.Data = mt, data
			} else {
				p.URL = url
			}
			parts = append(parts, p)
		case "file":
			if it.File == nil || it.File.FileData == "" {
				continue
			}
			p := ir.Part{Type: ir.PartFile}
			if mt, data, ok := parseDataURL(it.File.FileData); ok {
				p.MediaType, p.Data = mt, data
			} else {
				p.Data = it.File.FileData
				p.MediaType = mediaTypeFromName(it.File.Filename)
			}
			parts = append(parts, p)
		}
	}
	return parts, nil
}

func parseDataURL(u string) (mediaType, data string, ok bool) {
	rest, found := strings.CutPrefix(u, "data:")
	if !found {
		return "", "", false
	}
	meta, data, found := strings.Cut(rest, ",")
	if !found {
		return "", "", false
	}
	meta, isB64 := strings.CutSuffix(meta, ";base64")
	if !isB64 {
		return "", "", false
	}
	if i := strings.IndexByte(meta, ';'); i >= 0 {
		meta = meta[:i]
	}
	return meta, data, true
}

func mediaTypeFromName(name string) string {
	n := strings.ToLower(name)
	switch {
	case strings.HasSuffix(n, ".txt"):
		return "text/plain"
	case strings.HasSuffix(n, ".png"):
		return "image/png"
	case strings.HasSuffix(n, ".jpg"), strings.HasSuffix(n, ".jpeg"):
		return "image/jpeg"
	}
	return "application/pdf"
}

func fileNameFor(mediaType string) string {
	switch mediaType {
	case "text/plain":
		return "file.txt"
	case "image/png":
		return "file.png"
	case "image/jpeg":
		return "file.jpg"
	}
	return "file.pdf"
}

// ---- Encode ----

type outRequest struct {
	Model               string           `json:"model"`
	Messages            []outMessage     `json:"messages"`
	Tools               []outTool        `json:"tools,omitempty"`
	ToolChoice          any              `json:"tool_choice,omitempty"`
	MaxTokens           int              `json:"max_tokens,omitempty"`
	MaxCompletionTokens int              `json:"max_completion_tokens,omitempty"`
	Temperature         *float64         `json:"temperature,omitempty"`
	TopP                *float64         `json:"top_p,omitempty"`
	Stop                []string         `json:"stop,omitempty"`
	Stream              bool             `json:"stream"`
	StreamOptions       outStreamOptions `json:"stream_options"`
	ReasoningEffort     string           `json:"reasoning_effort,omitempty"`
	ParallelToolCalls   *bool            `json:"parallel_tool_calls,omitempty"`
	ResponseFormat      json.RawMessage  `json:"response_format,omitempty"`
	User                string           `json:"user,omitempty"`
	PromptCacheKey      string           `json:"prompt_cache_key,omitempty"`
}

type outStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type outMessage struct {
	Role       string        `json:"role"`
	Content    any           `json:"content"`
	ToolCalls  []outToolCall `json:"tool_calls,omitempty"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
}

type outToolCall struct {
	ID       string  `json:"id"`
	Type     string  `json:"type"`
	Function outFunc `json:"function"`
}

type outFunc struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type outContentPart struct {
	Type     string       `json:"type"`
	Text     *string      `json:"text,omitempty"`
	ImageURL *outImageURL `json:"image_url,omitempty"`
	File     *outFile     `json:"file,omitempty"`
}

type outImageURL struct {
	URL string `json:"url"`
}

type outFile struct {
	FileData string `json:"file_data"`
	Filename string `json:"filename,omitempty"`
}

type outTool struct {
	Type     string     `json:"type"`
	Function outToolDef `json:"function"`
}

type outToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

var (
	customToolSchema = json.RawMessage(`{"type":"object","properties":{"input":{"type":"string"}},"required":["input"]}`)
	emptySchema      = json.RawMessage(`{"type":"object","properties":{}}`)
)

// EncodeRequest renders IR as an upstream Chat Completions request. The
// upstream is always streamed with usage reporting enabled.
func EncodeRequest(req *ir.Request, opts ir.EncodeOptions) ([]byte, error) {
	out := outRequest{
		Model:         opts.Model,
		Stream:        true,
		StreamOptions: outStreamOptions{IncludeUsage: true},
		Temperature:   req.Temperature,
		TopP:          req.TopP,
		Stop:          req.Stop,
		User:          req.User,
	}
	if out.Model == "" {
		out.Model = req.Model
	}
	if len(req.ResponseFormat) > 0 && !isNull(req.ResponseFormat) {
		out.ResponseFormat = req.ResponseFormat
	}
	if opts.ProviderType == "openai" {
		out.MaxCompletionTokens = req.MaxTokens
		out.PromptCacheKey = req.PromptCacheKey
	} else {
		out.MaxTokens = req.MaxTokens
	}
	if r := req.Reasoning; r != nil && !r.Disabled && (opts.ProviderType == "openai" || opts.ProviderType == "openai-compatible") {
		switch {
		case r.Effort != "":
			out.ReasoningEffort = r.Effort
		case r.BudgetTokens > 0 && r.BudgetTokens <= 4096:
			out.ReasoningEffort = "low"
		case r.BudgetTokens > 4096 && r.BudgetTokens <= 16384:
			out.ReasoningEffort = "medium"
		case r.BudgetTokens > 16384:
			out.ReasoningEffort = "high"
		}
	}

	for _, t := range req.Tools {
		def := outToolDef{Name: t.Name, Description: t.Description, Parameters: t.Schema}
		if t.Kind == ir.ToolCustom {
			def.Parameters = customToolSchema
		} else if isNull(def.Parameters) {
			def.Parameters = emptySchema
		}
		out.Tools = append(out.Tools, outTool{Type: "function", Function: def})
	}
	if len(out.Tools) > 0 {
		out.ParallelToolCalls = req.ParallelToolCalls
		if tc := req.ToolChoice; tc != nil {
			switch tc.Mode {
			case "auto", "none", "required":
				out.ToolChoice = tc.Mode
			case "tool":
				out.ToolChoice = map[string]any{"type": "function", "function": map[string]string{"name": tc.Name}}
			}
		}
	}

	out.Messages = make([]outMessage, 0, len(req.Messages)+1)
	if len(req.System) > 0 {
		texts := make([]string, 0, len(req.System))
		for _, p := range req.System {
			if p.Type == ir.PartText && p.Text != "" {
				texts = append(texts, p.Text)
			}
		}
		if len(texts) > 0 {
			out.Messages = append(out.Messages, outMessage{Role: "system", Content: strings.Join(texts, "\n\n")})
		}
	}
	for i := range req.Messages {
		out.Messages = appendMessage(out.Messages, &req.Messages[i])
	}
	return marshalNoEscape(&out)
}

func appendMessage(dst []outMessage, m *ir.Message) []outMessage {
	// Tool results always become standalone tool messages, whatever role
	// carried them, and must directly follow the assistant tool_calls.
	for _, p := range m.Parts {
		if p.Type == ir.PartToolResult {
			dst = append(dst, outMessage{Role: "tool", ToolCallID: p.ToolID, Content: toolResultText(p.Result)})
		}
	}
	switch m.Role {
	case ir.RoleAssistant:
		var texts []string
		var calls []outToolCall
		for _, p := range m.Parts {
			switch p.Type {
			case ir.PartText:
				if p.Text != "" {
					texts = append(texts, p.Text)
				}
			case ir.PartToolCall:
				args := p.Args
				if p.ToolKind == ir.ToolCustom {
					args = customArgsJSON(p.Args)
				} else if strings.TrimSpace(args) == "" {
					args = "{}"
				}
				calls = append(calls, outToolCall{ID: p.ToolID, Type: "function", Function: outFunc{Name: p.ToolName, Arguments: args}})
			}
		}
		if len(texts) == 0 && len(calls) == 0 {
			return dst
		}
		msg := outMessage{Role: "assistant", ToolCalls: calls}
		if len(texts) > 0 {
			msg.Content = strings.Join(texts, "\n\n")
		}
		return append(dst, msg)
	case ir.RoleUser:
		var parts []outContentPart
		allText := true
		var texts []string
		for _, p := range m.Parts {
			switch p.Type {
			case ir.PartText:
				t := p.Text
				texts = append(texts, t)
				parts = append(parts, outContentPart{Type: "text", Text: &t})
			case ir.PartImage:
				allText = false
				u := p.URL
				if u == "" {
					u = "data:" + p.MediaType + ";base64," + p.Data
				}
				parts = append(parts, outContentPart{Type: "image_url", ImageURL: &outImageURL{URL: u}})
			case ir.PartFile:
				allText = false
				if p.Data == "" {
					t := p.URL
					parts = append(parts, outContentPart{Type: "text", Text: &t})
					continue
				}
				mt := p.MediaType
				if mt == "" {
					mt = "application/pdf"
				}
				parts = append(parts, outContentPart{Type: "file", File: &outFile{
					FileData: "data:" + mt + ";base64," + p.Data, Filename: fileNameFor(mt),
				}})
			}
		}
		if len(parts) == 0 {
			return dst
		}
		if allText {
			return append(dst, outMessage{Role: "user", Content: strings.Join(texts, "\n\n")})
		}
		return append(dst, outMessage{Role: "user", Content: parts})
	}
	return dst
}

func toolResultText(parts []ir.Part) string {
	if len(parts) == 1 && parts[0].Type == ir.PartText {
		return parts[0].Text
	}
	var b strings.Builder
	for i, p := range parts {
		if i > 0 {
			b.WriteByte('\n')
		}
		switch p.Type {
		case ir.PartText:
			b.WriteString(p.Text)
		case ir.PartImage:
			b.WriteString("[image omitted]")
		default:
			b.WriteString("[file omitted]")
		}
	}
	return b.String()
}
