package responses

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"path"
	"strings"

	"github.com/Project-by-Myself/clirouter/internal/ir"
)

type wireTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
	Format      json.RawMessage `json:"format"`
}

type wireRequest struct {
	Model           string          `json:"model"`
	Instructions    string          `json:"instructions"`
	Input           json.RawMessage `json:"input"`
	Tools           []wireTool      `json:"tools"`
	ToolChoice      json.RawMessage `json:"tool_choice"`
	MaxOutputTokens *int            `json:"max_output_tokens"`
	Temperature     *float64        `json:"temperature"`
	TopP            *float64        `json:"top_p"`
	Reasoning       *struct {
		Effort  string `json:"effort"`
		Summary string `json:"summary"`
	} `json:"reasoning"`
	Stream            bool   `json:"stream"`
	ParallelToolCalls *bool  `json:"parallel_tool_calls"`
	PromptCacheKey    string `json:"prompt_cache_key"`
	Text              *struct {
		Format json.RawMessage `json:"format"`
	} `json:"text"`
	User             string `json:"user"`
	SafetyIdentifier string `json:"safety_identifier"`
}

type wirePart struct {
	Type     string          `json:"type"`
	Text     string          `json:"text"`
	Refusal  string          `json:"refusal"`
	ImageURL json.RawMessage `json:"image_url"`
	FileData string          `json:"file_data"`
	FileURL  string          `json:"file_url"`
	Filename string          `json:"filename"`
}

type wireItem struct {
	Type             string          `json:"type"`
	Role             string          `json:"role"`
	Content          json.RawMessage `json:"content"`
	CallID           string          `json:"call_id"`
	Name             string          `json:"name"`
	Arguments        string          `json:"arguments"`
	Input            string          `json:"input"`
	Output           json.RawMessage `json:"output"`
	Summary          []wirePart      `json:"summary"`
	EncryptedContent string          `json:"encrypted_content"`
}

func isJSONString(r json.RawMessage) bool {
	r = bytes.TrimLeft(r, " \t\r\n")
	return len(r) > 0 && r[0] == '"'
}

func isJSONNull(r json.RawMessage) bool {
	r = bytes.TrimSpace(r)
	return len(r) == 0 || string(r) == "null"
}

func parseDataURL(s string) (mediaType, data string, ok bool) {
	if !strings.HasPrefix(s, "data:") {
		return "", "", false
	}
	meta, data, found := strings.Cut(s[5:], ",")
	if !found || !strings.HasSuffix(meta, ";base64") {
		return "", "", false
	}
	mediaType, _, _ = strings.Cut(strings.TrimSuffix(meta, ";base64"), ";")
	return mediaType, data, true
}

func imagePart(raw json.RawMessage) (ir.Part, bool) {
	var url string
	if isJSONString(raw) {
		_ = json.Unmarshal(raw, &url)
	} else if !isJSONNull(raw) {
		var obj struct {
			URL string `json:"url"`
		}
		_ = json.Unmarshal(raw, &obj)
		url = obj.URL
	}
	if url == "" {
		return ir.Part{}, false
	}
	if mt, data, ok := parseDataURL(url); ok {
		return ir.Part{Type: ir.PartImage, MediaType: mt, Data: data}, true
	}
	return ir.Part{Type: ir.PartImage, URL: url}, true
}

// filePart converts an input_file part; the filename is carried in Text.
func filePart(p *wirePart) (ir.Part, bool) {
	if p.FileData != "" {
		if mt, data, ok := parseDataURL(p.FileData); ok {
			return ir.Part{Type: ir.PartFile, MediaType: mt, Data: data, Text: p.Filename}, true
		}
		mt := mime.TypeByExtension(path.Ext(p.Filename))
		if mt == "" {
			mt = "application/octet-stream"
		}
		mt, _, _ = strings.Cut(mt, ";")
		return ir.Part{Type: ir.PartFile, MediaType: mt, Data: p.FileData, Text: p.Filename}, true
	}
	if p.FileURL != "" {
		return ir.Part{Type: ir.PartFile, URL: p.FileURL, Text: p.Filename}, true
	}
	return ir.Part{}, false
}

func decodeContent(raw json.RawMessage) ([]ir.Part, error) {
	if isJSONNull(raw) {
		return nil, nil
	}
	if isJSONString(raw) {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, err
		}
		if s == "" {
			return nil, nil
		}
		return []ir.Part{{Type: ir.PartText, Text: s}}, nil
	}
	var parts []wirePart
	if err := json.Unmarshal(raw, &parts); err != nil {
		return nil, err
	}
	out := make([]ir.Part, 0, len(parts))
	for i := range parts {
		p := &parts[i]
		switch p.Type {
		case "input_text", "output_text", "text", "summary_text", "reasoning_text":
			if p.Text != "" {
				out = append(out, ir.Part{Type: ir.PartText, Text: p.Text})
			}
		case "refusal":
			if p.Refusal != "" {
				out = append(out, ir.Part{Type: ir.PartText, Text: p.Refusal})
			}
		case "input_image":
			if ip, ok := imagePart(p.ImageURL); ok {
				out = append(out, ip)
			}
		case "input_file":
			if fp, ok := filePart(p); ok {
				out = append(out, fp)
			}
		}
	}
	return out, nil
}

type reqBuilder struct {
	req   *ir.Request
	calls map[string]ir.Part
}

func (b *reqBuilder) assistant() *ir.Message {
	if n := len(b.req.Messages); n > 0 && b.req.Messages[n-1].Role == ir.RoleAssistant {
		return &b.req.Messages[n-1]
	}
	b.req.Messages = append(b.req.Messages, ir.Message{Role: ir.RoleAssistant})
	return &b.req.Messages[len(b.req.Messages)-1]
}

func (b *reqBuilder) item(it *wireItem) error {
	typ := it.Type
	if typ == "" && it.Role != "" {
		typ = "message"
	}
	switch typ {
	case "message":
		parts, err := decodeContent(it.Content)
		if err != nil {
			return fmt.Errorf("responses: message content: %w", err)
		}
		switch it.Role {
		case "system", "developer":
			for _, p := range parts {
				if p.Type == ir.PartText {
					b.req.System = append(b.req.System, p)
				}
			}
		case "assistant":
			if len(parts) > 0 {
				m := b.assistant()
				m.Parts = append(m.Parts, parts...)
			}
		case "user", "":
			if len(parts) > 0 {
				b.req.Messages = append(b.req.Messages, ir.Message{Role: ir.RoleUser, Parts: parts})
			}
		default:
			return fmt.Errorf("responses: unsupported message role %q", it.Role)
		}
	case "function_call", "custom_tool_call":
		p := ir.Part{Type: ir.PartToolCall, ToolID: it.CallID, ToolName: it.Name, Args: it.Arguments}
		if typ == "custom_tool_call" {
			p.ToolKind, p.Args = ir.ToolCustom, it.Input
		}
		b.calls[it.CallID] = p
		m := b.assistant()
		m.Parts = append(m.Parts, p)
	case "function_call_output", "custom_tool_call_output":
		p := ir.Part{Type: ir.PartToolResult, ToolID: it.CallID}
		if c, ok := b.calls[it.CallID]; ok {
			p.ToolName, p.ToolKind = c.ToolName, c.ToolKind
		}
		if typ == "custom_tool_call_output" {
			p.ToolKind = ir.ToolCustom
		}
		res, err := decodeContent(it.Output)
		if err != nil {
			return fmt.Errorf("responses: tool output: %w", err)
		}
		p.Result = res
		b.req.Messages = append(b.req.Messages, ir.Message{Role: ir.RoleTool, Parts: []ir.Part{p}})
	case "reasoning":
		texts := make([]string, 0, len(it.Summary))
		for _, s := range it.Summary {
			if s.Text != "" {
				texts = append(texts, s.Text)
			}
		}
		p := ir.Part{Type: ir.PartThinking, Text: strings.Join(texts, "\n\n")}
		if src, sig, text, ok := DecodeEnvelope(it.EncryptedContent); ok {
			if base, red := strings.CutSuffix(src, redactedSuffix); red {
				p = ir.Part{Type: ir.PartRedactedThinking, Signature: sig, SigSource: base}
			} else {
				p.Text, p.Signature, p.SigSource = text, sig, src
			}
		} else if it.EncryptedContent != "" {
			p.Signature, p.SigSource = it.EncryptedContent, ir.SigOpenAI
		}
		if p.Text == "" && p.Signature == "" {
			return nil
		}
		m := b.assistant()
		m.Parts = append(m.Parts, p)
	}
	return nil
}

func DecodeRequest(body []byte) (*ir.Request, error) {
	var w wireRequest
	if err := json.Unmarshal(body, &w); err != nil {
		return nil, fmt.Errorf("responses: invalid request body: %w", err)
	}
	req := &ir.Request{
		Model:             w.Model,
		Temperature:       w.Temperature,
		TopP:              w.TopP,
		Stream:            w.Stream,
		ParallelToolCalls: w.ParallelToolCalls,
		PromptCacheKey:    w.PromptCacheKey,
		User:              w.User,
		IncludeUsage:      true,
	}
	if req.User == "" {
		req.User = w.SafetyIdentifier
	}
	if w.MaxOutputTokens != nil {
		req.MaxTokens = *w.MaxOutputTokens
	}
	if w.Instructions != "" {
		req.System = append(req.System, ir.Part{Type: ir.PartText, Text: w.Instructions})
	}
	if r := w.Reasoning; r != nil && (r.Effort != "" || r.Summary != "") {
		req.Reasoning = &ir.Reasoning{Effort: r.Effort, Summary: r.Summary, Disabled: r.Effort == "none"}
	}
	b := reqBuilder{req: req, calls: map[string]ir.Part{}}
	switch {
	case isJSONNull(w.Input):
	case isJSONString(w.Input):
		var s string
		if err := json.Unmarshal(w.Input, &s); err != nil {
			return nil, fmt.Errorf("responses: input: %w", err)
		}
		req.Messages = append(req.Messages, ir.Message{Role: ir.RoleUser, Parts: []ir.Part{{Type: ir.PartText, Text: s}}})
	default:
		var raws []json.RawMessage
		if err := json.Unmarshal(w.Input, &raws); err != nil {
			return nil, fmt.Errorf("responses: input must be a string or array: %w", err)
		}
		for _, raw := range raws {
			var it wireItem
			if err := json.Unmarshal(raw, &it); err != nil {
				var t struct {
					Type string `json:"type"`
				}
				_ = json.Unmarshal(raw, &t)
				switch t.Type {
				case "message", "function_call", "custom_tool_call", "function_call_output", "custom_tool_call_output", "reasoning", "":
					return nil, fmt.Errorf("responses: input item %q: %w", t.Type, err)
				}
				continue
			}
			if err := b.item(&it); err != nil {
				return nil, err
			}
		}
	}
	for _, t := range w.Tools {
		switch t.Type {
		case "function":
			req.Tools = append(req.Tools, ir.Tool{Name: t.Name, Description: t.Description, Schema: t.Parameters})
		case "custom":
			tool := ir.Tool{Name: t.Name, Description: t.Description, Kind: ir.ToolCustom}
			if !isJSONNull(t.Format) {
				tool.Format = t.Format
			}
			req.Tools = append(req.Tools, tool)
		}
	}
	if tc, err := decodeToolChoice(w.ToolChoice); err != nil {
		return nil, err
	} else {
		req.ToolChoice = tc
	}
	if w.Text != nil && !isJSONNull(w.Text.Format) {
		rf, err := responseFormatToChat(w.Text.Format)
		if err != nil {
			return nil, err
		}
		req.ResponseFormat = rf
	}
	return req, nil
}

func decodeToolChoice(raw json.RawMessage) (*ir.ToolChoice, error) {
	if isJSONNull(raw) {
		return nil, nil
	}
	if isJSONString(raw) {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, err
		}
		return &ir.ToolChoice{Mode: s}, nil
	}
	var o struct {
		Type string `json:"type"`
		Name string `json:"name"`
		Mode string `json:"mode"`
	}
	if err := json.Unmarshal(raw, &o); err != nil {
		return nil, fmt.Errorf("responses: tool_choice: %w", err)
	}
	switch o.Type {
	case "function", "custom":
		return &ir.ToolChoice{Mode: "tool", Name: o.Name}, nil
	case "allowed_tools":
		if o.Mode == "required" {
			return &ir.ToolChoice{Mode: "required"}, nil
		}
	}
	return &ir.ToolChoice{Mode: "auto"}, nil
}

type wireFormat struct {
	Type        string          `json:"type"`
	Name        string          `json:"name,omitempty"`
	Description string          `json:"description,omitempty"`
	Schema      json.RawMessage `json:"schema,omitempty"`
	Strict      *bool           `json:"strict,omitempty"`
}

func responseFormatToChat(raw json.RawMessage) (json.RawMessage, error) {
	var f wireFormat
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("responses: text.format: %w", err)
	}
	switch f.Type {
	case "json_schema":
		js := wireFormat{Name: f.Name, Description: f.Description, Schema: f.Schema, Strict: f.Strict}
		return marshal(struct {
			Type       string `json:"type"`
			JSONSchema any    `json:"json_schema"`
		}{"json_schema", struct {
			Name        string          `json:"name"`
			Description string          `json:"description,omitempty"`
			Schema      json.RawMessage `json:"schema,omitempty"`
			Strict      *bool           `json:"strict,omitempty"`
		}{js.Name, js.Description, js.Schema, js.Strict}})
	case "json_object":
		return json.RawMessage(`{"type":"json_object"}`), nil
	}
	return nil, nil
}

func chatFormatToText(raw json.RawMessage) (json.RawMessage, error) {
	var c struct {
		Type       string `json:"type"`
		JSONSchema *struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			Schema      json.RawMessage `json:"schema"`
			Strict      *bool           `json:"strict"`
		} `json:"json_schema"`
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("responses: response_format: %w", err)
	}
	switch c.Type {
	case "json_schema":
		if c.JSONSchema == nil {
			return nil, errors.New("responses: response_format json_schema missing schema")
		}
		name := c.JSONSchema.Name
		if name == "" {
			name = "response"
		}
		return marshal(wireFormat{Type: "json_schema", Name: name, Description: c.JSONSchema.Description, Schema: c.JSONSchema.Schema, Strict: c.JSONSchema.Strict})
	case "json_object":
		return json.RawMessage(`{"type":"json_object"}`), nil
	}
	return nil, nil
}

func marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

type outPart struct {
	Type     string  `json:"type"`
	Text     *string `json:"text,omitempty"`
	ImageURL string  `json:"image_url,omitempty"`
	FileData string  `json:"file_data,omitempty"`
	FileURL  string  `json:"file_url,omitempty"`
	Filename string  `json:"filename,omitempty"`
}

type outMessage struct {
	Type    string    `json:"type"`
	Role    string    `json:"role"`
	Content []outPart `json:"content"`
}

type outSummary struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type outReasoning struct {
	Type             string       `json:"type"`
	Summary          []outSummary `json:"summary"`
	EncryptedContent string       `json:"encrypted_content,omitempty"`
}

type outFunctionCall struct {
	Type      string `json:"type"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type outCustomCall struct {
	Type   string `json:"type"`
	CallID string `json:"call_id"`
	Name   string `json:"name"`
	Input  string `json:"input"`
}

type outCallOutput struct {
	Type   string `json:"type"`
	CallID string `json:"call_id"`
	Output any    `json:"output"`
}

type outTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Strict      *bool           `json:"strict,omitempty"`
	Format      json.RawMessage `json:"format,omitempty"`
}

type outRequest struct {
	Model             string          `json:"model"`
	Instructions      *string         `json:"instructions,omitempty"`
	Input             []any           `json:"input"`
	Tools             []outTool       `json:"tools,omitempty"`
	ToolChoice        any             `json:"tool_choice,omitempty"`
	ParallelToolCalls *bool           `json:"parallel_tool_calls,omitempty"`
	Reasoning         *outReasoningP  `json:"reasoning,omitempty"`
	Store             bool            `json:"store"`
	Stream            bool            `json:"stream"`
	Include           []string        `json:"include,omitempty"`
	MaxOutputTokens   int             `json:"max_output_tokens,omitempty"`
	Temperature       *float64        `json:"temperature,omitempty"`
	TopP              *float64        `json:"top_p,omitempty"`
	PromptCacheKey    string          `json:"prompt_cache_key,omitempty"`
	Text              *outTextOptions `json:"text,omitempty"`
	User              string          `json:"user,omitempty"`
}

type outReasoningP struct {
	Effort  string `json:"effort,omitempty"`
	Summary string `json:"summary,omitempty"`
}

type outTextOptions struct {
	Format json.RawMessage `json:"format"`
}

func strPtr(s string) *string { return &s }

func dataURL(mediaType, data string) string {
	return "data:" + mediaType + ";base64," + data
}

func userPart(p *ir.Part) (outPart, bool) {
	switch p.Type {
	case ir.PartText:
		if p.Text == "" {
			return outPart{}, false
		}
		return outPart{Type: "input_text", Text: strPtr(p.Text)}, true
	case ir.PartImage:
		url := p.URL
		if p.Data != "" {
			url = dataURL(p.MediaType, p.Data)
		}
		if url == "" {
			return outPart{}, false
		}
		return outPart{Type: "input_image", ImageURL: url}, true
	case ir.PartFile:
		op := outPart{Type: "input_file", Filename: p.Text}
		switch {
		case p.Data != "":
			op.FileData = dataURL(p.MediaType, p.Data)
			if op.Filename == "" {
				op.Filename = "file"
				if exts, _ := mime.ExtensionsByType(p.MediaType); len(exts) > 0 {
					op.Filename += exts[0]
				}
			}
		case p.URL != "":
			op.FileURL = p.URL
		default:
			return outPart{}, false
		}
		return op, true
	}
	return outPart{}, false
}

func toolOutput(res []ir.Part) any {
	hasMedia := false
	for i := range res {
		if res[i].Type == ir.PartImage || res[i].Type == ir.PartFile {
			hasMedia = true
			break
		}
	}
	if !hasMedia {
		var sb strings.Builder
		for i := range res {
			if res[i].Type == ir.PartText {
				if sb.Len() > 0 {
					sb.WriteString("\n")
				}
				sb.WriteString(res[i].Text)
			}
		}
		return sb.String()
	}
	parts := make([]outPart, 0, len(res))
	for i := range res {
		if op, ok := userPart(&res[i]); ok {
			parts = append(parts, op)
		}
	}
	return parts
}

func effortForBudget(b int) string {
	switch {
	case b <= 4096:
		return "low"
	case b <= 16384:
		return "medium"
	default:
		return "high"
	}
}

func EncodeRequest(req *ir.Request, opts ir.EncodeOptions) ([]byte, error) {
	codex := opts.ProviderType == "codex"
	out := outRequest{
		Model:          opts.Model,
		Stream:         true,
		PromptCacheKey: req.PromptCacheKey,
		Input:          make([]any, 0, len(req.Messages)+4),
	}
	if out.Model == "" {
		out.Model = req.Model
	}
	// Non-reasoning models (gpt-4.1, gpt-4o…) reject this include.
	if codex || isReasoningModel(out.Model) || (req.Reasoning != nil && !req.Reasoning.Disabled) {
		out.Include = []string{"reasoning.encrypted_content"}
	}
	var sys []string
	for _, p := range req.System {
		if p.Type == ir.PartText && p.Text != "" {
			sys = append(sys, p.Text)
		}
	}
	if len(sys) > 0 || codex {
		out.Instructions = strPtr(strings.Join(sys, "\n\n"))
	}

	customTools := map[string]bool{}
	for _, t := range req.Tools {
		if t.Kind == ir.ToolCustom {
			customTools[t.Name] = true
			out.Tools = append(out.Tools, outTool{Type: "custom", Name: t.Name, Description: t.Description, Format: t.Format})
			continue
		}
		params := t.Schema
		if isJSONNull(params) {
			params = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		f := false
		out.Tools = append(out.Tools, outTool{Type: "function", Name: t.Name, Description: t.Description, Parameters: params, Strict: &f})
	}
	if tc := req.ToolChoice; tc != nil {
		switch tc.Mode {
		case "auto", "none", "required":
			out.ToolChoice = tc.Mode
		case "any":
			out.ToolChoice = "required"
		case "tool":
			typ := "function"
			if customTools[tc.Name] {
				typ = "custom"
			}
			out.ToolChoice = map[string]string{"type": typ, "name": tc.Name}
		}
	}
	out.ParallelToolCalls = req.ParallelToolCalls
	if codex && out.ParallelToolCalls == nil {
		f := false
		out.ParallelToolCalls = &f
	}
	if r := req.Reasoning; r != nil {
		effort := r.Effort
		if effort == "" && r.BudgetTokens > 0 {
			effort = effortForBudget(r.BudgetTokens)
		}
		if r.Disabled && effort != "none" {
			effort = ""
		}
		if effort != "" {
			summary := r.Summary
			if summary == "" {
				summary = "auto"
			}
			out.Reasoning = &outReasoningP{Effort: effort, Summary: summary}
		}
	}
	if !codex {
		if req.MaxTokens > 0 {
			out.MaxOutputTokens = req.MaxTokens
		}
		out.Temperature, out.TopP = req.Temperature, req.TopP
		out.User = req.User
	}
	if !isJSONNull(req.ResponseFormat) {
		f, err := chatFormatToText(req.ResponseFormat)
		if err != nil {
			return nil, err
		}
		if f != nil {
			out.Text = &outTextOptions{Format: f}
		}
	}

	callKinds := map[string]ir.ToolKind{}
	for _, m := range req.Messages {
		for _, p := range m.Parts {
			if p.Type == ir.PartToolCall {
				kind := p.ToolKind
				if customTools[p.ToolName] {
					kind = ir.ToolCustom
				}
				callKinds[p.ToolID] = kind
			}
		}
	}

	for _, m := range req.Messages {
		var cur *outMessage
		flush := func() {
			if cur != nil && len(cur.Content) > 0 {
				out.Input = append(out.Input, *cur)
			}
			cur = nil
		}
		for i := range m.Parts {
			p := &m.Parts[i]
			switch p.Type {
			case ir.PartToolResult:
				flush()
				typ := "function_call_output"
				var output any
				if p.ToolKind == ir.ToolCustom || callKinds[p.ToolID] == ir.ToolCustom {
					typ = "custom_tool_call_output"
				}
				output = toolOutput(p.Result)
				if p.IsError {
					if s, ok := output.(string); ok && s == "" {
						output = "error"
					}
				}
				out.Input = append(out.Input, outCallOutput{Type: typ, CallID: p.ToolID, Output: output})
			case ir.PartToolCall:
				flush()
				if p.ToolKind == ir.ToolCustom || customTools[p.ToolName] {
					out.Input = append(out.Input, outCustomCall{Type: "custom_tool_call", CallID: p.ToolID, Name: p.ToolName, Input: p.Args})
				} else {
					args := p.Args
					if args == "" {
						args = "{}"
					}
					out.Input = append(out.Input, outFunctionCall{Type: "function_call", CallID: p.ToolID, Name: p.ToolName, Arguments: args})
				}
			case ir.PartThinking:
				if p.SigSource != ir.SigOpenAI || p.Signature == "" {
					continue
				}
				flush()
				r := outReasoning{Type: "reasoning", Summary: []outSummary{}, EncryptedContent: p.Signature}
				if p.Text != "" {
					r.Summary = append(r.Summary, outSummary{Type: "summary_text", Text: p.Text})
				}
				out.Input = append(out.Input, r)
			case ir.PartRedactedThinking:
			default:
				if m.Role == ir.RoleAssistant {
					if p.Type != ir.PartText || p.Text == "" {
						continue
					}
					if cur == nil {
						cur = &outMessage{Type: "message", Role: "assistant"}
					}
					cur.Content = append(cur.Content, outPart{Type: "output_text", Text: strPtr(p.Text)})
					continue
				}
				op, ok := userPart(p)
				if !ok {
					continue
				}
				if cur == nil {
					cur = &outMessage{Type: "message", Role: "user"}
				}
				cur.Content = append(cur.Content, op)
			}
		}
		flush()
	}
	return marshal(out)
}

func isReasoningModel(m string) bool {
	m = strings.ToLower(m)
	if i := strings.LastIndexByte(m, '/'); i >= 0 {
		m = m[i+1:]
	}
	for _, p := range []string{"o1", "o3", "o4", "gpt-5", "codex"} {
		if strings.HasPrefix(m, p) {
			return true
		}
	}
	return false
}
