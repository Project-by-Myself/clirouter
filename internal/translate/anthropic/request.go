package anthropic

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Project-by-Myself/clirouter/internal/ir"
)

type wireRequest struct {
	Model         string          `json:"model"`
	System        json.RawMessage `json:"system"`
	Messages      []wireMessage   `json:"messages"`
	MaxTokens     int             `json:"max_tokens"`
	Temperature   *float64        `json:"temperature"`
	TopP          *float64        `json:"top_p"`
	TopK          *int            `json:"top_k"`
	StopSequences []string        `json:"stop_sequences"`
	Stream        bool            `json:"stream"`
	Tools         []wireTool      `json:"tools"`
	ToolChoice    *wireToolChoice `json:"tool_choice"`
	Thinking      *wireThinking   `json:"thinking"`
	Metadata      *wireMetadata   `json:"metadata"`
	OutputConfig  *struct {
		Effort string `json:"effort"`
	} `json:"output_config"`
}

type wireMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type wireTool struct {
	Type         string          `json:"type"`
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	InputSchema  json.RawMessage `json:"input_schema"`
	CacheControl json.RawMessage `json:"cache_control"`
}

type wireToolChoice struct {
	Type                   string `json:"type"`
	Name                   string `json:"name"`
	DisableParallelToolUse bool   `json:"disable_parallel_tool_use"`
}

type wireThinking struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens"`
}

type wireMetadata struct {
	UserID string `json:"user_id"`
}

type wireSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
	URL       string `json:"url"`
}

type wireBlock struct {
	Type         string          `json:"type"`
	Text         string          `json:"text"`
	Source       *wireSource     `json:"source"`
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	Input        json.RawMessage `json:"input"`
	ToolUseID    string          `json:"tool_use_id"`
	Content      json.RawMessage `json:"content"`
	IsError      bool            `json:"is_error"`
	Thinking     string          `json:"thinking"`
	Signature    string          `json:"signature"`
	Data         string          `json:"data"`
	CacheControl json.RawMessage `json:"cache_control"`
}

func decodeBlocks(raw json.RawMessage) ([]wireBlock, error) {
	switch firstNonSpace(raw) {
	case 0, 'n':
		return nil, nil
	case '"':
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, err
		}
		return []wireBlock{{Type: "text", Text: s}}, nil
	}
	var blocks []wireBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil, err
	}
	return blocks, nil
}

// DecodeRequest parses a client Messages API request into the IR.
func DecodeRequest(body []byte) (*ir.Request, error) {
	var w wireRequest
	if err := json.Unmarshal(body, &w); err != nil {
		return nil, fmt.Errorf("anthropic: decode request: %w", err)
	}
	req := &ir.Request{
		Model:       w.Model,
		MaxTokens:   w.MaxTokens,
		Temperature: w.Temperature,
		TopP:        w.TopP,
		TopK:        w.TopK,
		Stop:        w.StopSequences,
		Stream:      w.Stream,
		Messages:    make([]ir.Message, 0, len(w.Messages)),
	}
	if w.Metadata != nil {
		req.User = w.Metadata.UserID
	}

	sys, err := decodeBlocks(w.System)
	if err != nil {
		return nil, fmt.Errorf("anthropic: decode system: %w", err)
	}
	for _, b := range sys {
		if b.Type == "text" {
			req.System = append(req.System, ir.Part{Type: ir.PartText, Text: b.Text, CacheControl: b.CacheControl})
		}
	}

	for i, m := range w.Messages {
		blocks, err := decodeBlocks(m.Content)
		if err != nil {
			return nil, fmt.Errorf("anthropic: decode messages[%d].content: %w", i, err)
		}
		switch m.Role {
		case "user":
			var results, rest []ir.Part
			for _, b := range blocks {
				if b.Type == "tool_result" {
					p, err := toolResultPart(b)
					if err != nil {
						return nil, fmt.Errorf("anthropic: decode messages[%d] tool_result: %w", i, err)
					}
					results = append(results, p)
					continue
				}
				if p, ok := blockToPart(b); ok {
					rest = append(rest, p)
				}
			}
			if len(results) > 0 {
				req.Messages = append(req.Messages, ir.Message{Role: ir.RoleTool, Parts: results})
			}
			if len(rest) > 0 || len(results) == 0 {
				req.Messages = append(req.Messages, ir.Message{Role: ir.RoleUser, Parts: rest})
			}
		case "assistant":
			parts := make([]ir.Part, 0, len(blocks))
			for _, b := range blocks {
				if p, ok := blockToPart(b); ok {
					parts = append(parts, p)
				}
			}
			req.Messages = append(req.Messages, ir.Message{Role: ir.RoleAssistant, Parts: parts})
		default:
			return nil, fmt.Errorf("anthropic: messages[%d]: invalid role %q", i, m.Role)
		}
	}

	for _, t := range w.Tools {
		if t.Type != "" && t.Type != "custom" {
			continue // server tools (web_search_*, bash_*, ...) are not portable
		}
		req.Tools = append(req.Tools, ir.Tool{
			Name:         t.Name,
			Description:  t.Description,
			Schema:       t.InputSchema,
			CacheControl: t.CacheControl,
		})
	}

	if tc := w.ToolChoice; tc != nil {
		switch tc.Type {
		case "auto", "":
			req.ToolChoice = &ir.ToolChoice{Mode: "auto"}
		case "any":
			req.ToolChoice = &ir.ToolChoice{Mode: "required"}
		case "none":
			req.ToolChoice = &ir.ToolChoice{Mode: "none"}
		case "tool":
			req.ToolChoice = &ir.ToolChoice{Mode: "tool", Name: tc.Name}
		}
		if tc.DisableParallelToolUse {
			f := false
			req.ParallelToolCalls = &f
		}
	}

	if th := w.Thinking; th != nil {
		switch th.Type {
		case "enabled":
			req.Reasoning = &ir.Reasoning{BudgetTokens: th.BudgetTokens}
		case "disabled":
			req.Reasoning = &ir.Reasoning{Disabled: true}
		case "adaptive":
			effort := "high"
			if w.OutputConfig != nil && w.OutputConfig.Effort != "" {
				effort = w.OutputConfig.Effort
			}
			req.Reasoning = &ir.Reasoning{Effort: effort}
		}
	}
	return req, nil
}

func blockToPart(b wireBlock) (ir.Part, bool) {
	switch b.Type {
	case "text":
		return ir.Part{Type: ir.PartText, Text: b.Text, CacheControl: b.CacheControl}, true
	case "image":
		if b.Source == nil {
			return ir.Part{}, false
		}
		p := ir.Part{Type: ir.PartImage, CacheControl: b.CacheControl}
		switch b.Source.Type {
		case "base64":
			p.MediaType, p.Data = b.Source.MediaType, b.Source.Data
		case "url":
			p.URL = b.Source.URL
		default:
			return ir.Part{}, false
		}
		return p, true
	case "document":
		if b.Source == nil {
			return ir.Part{}, false
		}
		switch b.Source.Type {
		case "base64":
			mt := b.Source.MediaType
			if mt == "" {
				mt = "application/pdf"
			}
			return ir.Part{Type: ir.PartFile, MediaType: mt, Data: b.Source.Data, CacheControl: b.CacheControl}, true
		case "url":
			return ir.Part{Type: ir.PartFile, MediaType: "application/pdf", URL: b.Source.URL, CacheControl: b.CacheControl}, true
		case "text":
			return ir.Part{Type: ir.PartText, Text: b.Source.Data, CacheControl: b.CacheControl}, true
		}
		return ir.Part{}, false
	case "tool_use":
		args := string(bytes.TrimSpace(b.Input))
		if args == "" || args == "null" {
			args = "{}"
		}
		return ir.Part{Type: ir.PartToolCall, ToolID: b.ID, ToolName: b.Name, Args: args, CacheControl: b.CacheControl}, true
	case "thinking":
		sig, src := decodeSig(b.Signature)
		return ir.Part{Type: ir.PartThinking, Text: b.Thinking, Signature: sig, SigSource: src}, true
	case "redacted_thinking":
		sig, src := decodeSig(b.Data)
		return ir.Part{Type: ir.PartRedactedThinking, Signature: sig, SigSource: src}, true
	}
	return ir.Part{}, false
}

func toolResultPart(b wireBlock) (ir.Part, error) {
	p := ir.Part{Type: ir.PartToolResult, ToolID: b.ToolUseID, IsError: b.IsError, CacheControl: b.CacheControl}
	inner, err := decodeBlocks(b.Content)
	if err != nil {
		return p, err
	}
	for _, ib := range inner {
		if ip, ok := blockToPart(ib); ok && (ip.Type == ir.PartText || ip.Type == ir.PartImage || ip.Type == ir.PartFile) {
			p.Result = append(p.Result, ip)
		}
	}
	return p, nil
}

// ---- encoding (IR -> upstream request) ----

type outRequest struct {
	Model         string         `json:"model"`
	MaxTokens     int            `json:"max_tokens"`
	System        []outBlock     `json:"system,omitempty"`
	Messages      []outMessage   `json:"messages"`
	Tools         []outTool      `json:"tools,omitempty"`
	ToolChoice    *outToolChoice `json:"tool_choice,omitempty"`
	Temperature   *float64       `json:"temperature,omitempty"`
	TopP          *float64       `json:"top_p,omitempty"`
	TopK          *int           `json:"top_k,omitempty"`
	StopSequences []string       `json:"stop_sequences,omitempty"`
	Stream        bool           `json:"stream"`
	Thinking      *outThinking   `json:"thinking,omitempty"`
	Metadata      *wireMetadata  `json:"metadata,omitempty"`
}

type outMessage struct {
	Role    string     `json:"role"`
	Content []outBlock `json:"content"`
}

type outSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`
	URL       string `json:"url,omitempty"`
}

type outBlock struct {
	Type         string          `json:"type"`
	Text         *string         `json:"text,omitempty"`
	Source       *outSource      `json:"source,omitempty"`
	ID           string          `json:"id,omitempty"`
	Name         string          `json:"name,omitempty"`
	Input        json.RawMessage `json:"input,omitempty"`
	ToolUseID    string          `json:"tool_use_id,omitempty"`
	Content      []outBlock      `json:"content,omitempty"`
	IsError      bool            `json:"is_error,omitempty"`
	Thinking     *string         `json:"thinking,omitempty"`
	Signature    *string         `json:"signature,omitempty"`
	Data         string          `json:"data,omitempty"`
	CacheControl json.RawMessage `json:"cache_control,omitempty"`
}

type outTool struct {
	Name         string          `json:"name"`
	Description  string          `json:"description,omitempty"`
	InputSchema  json.RawMessage `json:"input_schema"`
	CacheControl json.RawMessage `json:"cache_control,omitempty"`
}

type outToolChoice struct {
	Type                   string `json:"type"`
	Name                   string `json:"name,omitempty"`
	DisableParallelToolUse bool   `json:"disable_parallel_tool_use,omitempty"`
}

type outThinking struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens"`
}

var (
	customToolSchema = json.RawMessage(`{"type":"object","properties":{"input":{"type":"string"}},"required":["input"]}`)
	emptyObject      = json.RawMessage(`{}`)
	emptyToolSchema  = json.RawMessage(`{"type":"object","properties":{}}`)
)

func effortBudget(effort string) int {
	switch effort {
	case "minimal", "low":
		return 2048
	case "medium":
		return 8192
	case "high":
		return 16384
	case "xhigh", "max":
		return 32000
	}
	return 0
}

func isValidIDChar(c byte) bool {
	return c == '_' || c == '-' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func sanitizeID(id string) string {
	for i := 0; i < len(id); i++ {
		if !isValidIDChar(id[i]) {
			b := []byte(id)
			for j := i; j < len(b); j++ {
				if !isValidIDChar(b[j]) {
					b[j] = '_'
				}
			}
			return string(b)
		}
	}
	return id
}

// idMapper assigns ids to tool calls that lack one and pairs results that
// lack one with them in order.
type idMapper struct {
	pending []struct{ name, id string }
}

func (m *idMapper) call(id, name string) string {
	if id != "" {
		return sanitizeID(id)
	}
	id = newID("toolu_")
	m.pending = append(m.pending, struct{ name, id string }{name, id})
	return id
}

func (m *idMapper) result(id, name string) string {
	if id != "" {
		return sanitizeID(id)
	}
	for i, p := range m.pending {
		if name == "" || p.name == name {
			m.pending = append(m.pending[:i], m.pending[i+1:]...)
			return p.id
		}
	}
	return newID("toolu_")
}

func validObjectJSON(s string) bool {
	return firstNonSpace([]byte(s)) == '{' && json.Valid([]byte(s))
}

func toolInput(args string, custom bool) json.RawMessage {
	if custom {
		return wrapString(`{"input":`, args)
	}
	if strings.TrimSpace(args) == "" {
		return emptyObject
	}
	if validObjectJSON(args) {
		return json.RawMessage(args)
	}
	return wrapString(`{"_raw":`, args)
}

func wrapString(prefix, s string) json.RawMessage {
	b := make([]byte, 0, len(prefix)+len(s)+8)
	b = append(b, prefix...)
	b = appendJSONString(b, s)
	return append(b, '}')
}

func strPtr(s string) *string { return &s }

func imageSource(p ir.Part) *outSource {
	if p.Data != "" {
		mt := p.MediaType
		if mt == "" {
			mt = "image/png"
		}
		return &outSource{Type: "base64", MediaType: mt, Data: p.Data}
	}
	if mt, data, ok := parseDataURL(p.URL); ok {
		return &outSource{Type: "base64", MediaType: mt, Data: data}
	}
	if p.URL != "" {
		return &outSource{Type: "url", URL: p.URL}
	}
	return nil
}

func parseDataURL(u string) (mediaType, data string, ok bool) {
	rest, found := strings.CutPrefix(u, "data:")
	if !found {
		return "", "", false
	}
	meta, payload, found := strings.Cut(rest, ",")
	if !found {
		return "", "", false
	}
	mt, isB64 := strings.CutSuffix(meta, ";base64")
	if !isB64 {
		return "", "", false
	}
	return mt, payload, true
}

func fileBlock(p ir.Part) (outBlock, bool) {
	b := outBlock{Type: "document", CacheControl: p.CacheControl}
	mt := p.MediaType
	switch {
	case strings.HasPrefix(mt, "text/") && p.Data != "":
		raw, err := base64.StdEncoding.DecodeString(p.Data)
		if err != nil {
			return b, false
		}
		b.Source = &outSource{Type: "text", MediaType: "text/plain", Data: string(raw)}
	case p.Data != "":
		if mt == "" {
			mt = "application/pdf"
		}
		b.Source = &outSource{Type: "base64", MediaType: mt, Data: p.Data}
	case p.URL != "":
		if dmt, data, ok := parseDataURL(p.URL); ok {
			b.Source = &outSource{Type: "base64", MediaType: dmt, Data: data}
		} else {
			b.Source = &outSource{Type: "url", URL: p.URL}
		}
	default:
		return b, false
	}
	return b, true
}

func resultContent(p ir.Part) []outBlock {
	var out []outBlock
	for _, r := range p.Result {
		switch r.Type {
		case ir.PartText:
			if r.Text != "" {
				out = append(out, outBlock{Type: "text", Text: strPtr(r.Text), CacheControl: r.CacheControl})
			}
		case ir.PartImage:
			if src := imageSource(r); src != nil {
				out = append(out, outBlock{Type: "image", Source: src, CacheControl: r.CacheControl})
			}
		case ir.PartFile:
			if b, ok := fileBlock(r); ok {
				out = append(out, b)
			}
		}
	}
	if len(out) == 0 && p.Text != "" {
		out = append(out, outBlock{Type: "text", Text: strPtr(p.Text)})
	}
	return out
}

// lastToolTurnSigned reports whether the most recent assistant message that
// calls a tool carries an Anthropic-signed thinking block (or there is none).
func lastToolTurnSigned(msgs []ir.Message) bool {
	for i := len(msgs) - 1; i >= 0; i-- {
		m := &msgs[i]
		if m.Role != ir.RoleAssistant {
			continue
		}
		hasCall, signed := false, false
		for j := range m.Parts {
			p := &m.Parts[j]
			switch p.Type {
			case ir.PartToolCall:
				hasCall = true
			case ir.PartThinking, ir.PartRedactedThinking:
				if p.SigSource == ir.SigAnthropic && p.Signature != "" {
					signed = true
				}
			}
		}
		if hasCall {
			return signed
		}
	}
	return true
}

// EncodeRequest renders an IR request as an upstream Messages API request.
// The upstream call is always streamed.
func EncodeRequest(req *ir.Request, opts ir.EncodeOptions) ([]byte, error) {
	if req == nil {
		return nil, errors.New("anthropic: nil request")
	}
	model := opts.Model
	if model == "" {
		model = req.Model
	}
	out := outRequest{Model: model, Stream: true, StopSequences: req.Stop, Messages: make([]outMessage, 0, len(req.Messages))}
	if req.User != "" {
		out.Metadata = &wireMetadata{UserID: req.User}
	}

	for _, p := range req.System {
		if p.Type == ir.PartText && p.Text != "" {
			out.System = append(out.System, outBlock{Type: "text", Text: strPtr(p.Text), CacheControl: p.CacheControl})
		}
	}

	custom := map[string]bool{}
	for _, t := range req.Tools {
		if t.Kind == ir.ToolCustom {
			custom[t.Name] = true
		}
	}

	var ids idMapper
	usedTools := map[string]bool{}
	var usedOrder []string
	for _, m := range req.Messages {
		role := "user"
		if m.Role == ir.RoleAssistant {
			role = "assistant"
		}
		var blocks []outBlock
		for _, p := range m.Parts {
			switch p.Type {
			case ir.PartText:
				if p.Text != "" {
					blocks = append(blocks, outBlock{Type: "text", Text: strPtr(p.Text), CacheControl: p.CacheControl})
				}
			case ir.PartImage:
				if src := imageSource(p); src != nil {
					blocks = append(blocks, outBlock{Type: "image", Source: src, CacheControl: p.CacheControl})
				}
			case ir.PartFile:
				if b, ok := fileBlock(p); ok {
					blocks = append(blocks, b)
				}
			case ir.PartToolCall:
				if role != "assistant" {
					continue
				}
				isCustom := p.ToolKind == ir.ToolCustom || custom[p.ToolName]
				blocks = append(blocks, outBlock{
					Type:         "tool_use",
					ID:           ids.call(p.ToolID, p.ToolName),
					Name:         p.ToolName,
					Input:        toolInput(p.Args, isCustom),
					CacheControl: p.CacheControl,
				})
				if !usedTools[p.ToolName] {
					usedTools[p.ToolName] = true
					usedOrder = append(usedOrder, p.ToolName)
				}
			case ir.PartToolResult:
				if role != "user" {
					continue
				}
				blocks = append(blocks, outBlock{
					Type:         "tool_result",
					ToolUseID:    ids.result(p.ToolID, p.ToolName),
					Content:      resultContent(p),
					IsError:      p.IsError,
					CacheControl: p.CacheControl,
				})
			case ir.PartThinking:
				if role == "assistant" && p.SigSource == ir.SigAnthropic && p.Signature != "" {
					blocks = append(blocks, outBlock{Type: "thinking", Thinking: strPtr(p.Text), Signature: strPtr(p.Signature)})
				}
			case ir.PartRedactedThinking:
				if role == "assistant" && p.SigSource == ir.SigAnthropic && p.Signature != "" {
					blocks = append(blocks, outBlock{Type: "redacted_thinking", Data: p.Signature})
				}
			}
		}
		if len(blocks) == 0 {
			continue
		}
		if n := len(out.Messages); n > 0 && out.Messages[n-1].Role == role {
			out.Messages[n-1].Content = append(out.Messages[n-1].Content, blocks...)
		} else {
			out.Messages = append(out.Messages, outMessage{Role: role, Content: blocks})
		}
	}
	for i := range out.Messages {
		m := &out.Messages[i]
		if m.Role == "user" {
			stablePartition(m.Content, func(b *outBlock) bool { return b.Type == "tool_result" })
		} else {
			stablePartition(m.Content, func(b *outBlock) bool { return b.Type == "thinking" || b.Type == "redacted_thinking" })
		}
	}

	for _, t := range req.Tools {
		ot := outTool{Name: t.Name, Description: t.Description, InputSchema: t.Schema, CacheControl: t.CacheControl}
		if t.Kind == ir.ToolCustom {
			ot.InputSchema = customToolSchema
		} else if len(bytes.TrimSpace(t.Schema)) == 0 || string(bytes.TrimSpace(t.Schema)) == "null" {
			ot.InputSchema = emptyToolSchema
		}
		out.Tools = append(out.Tools, ot)
	}

	if len(out.Tools) > 0 {
		var tc *outToolChoice
		if c := req.ToolChoice; c != nil {
			switch c.Mode {
			case "auto":
				tc = &outToolChoice{Type: "auto"}
			case "required", "any":
				tc = &outToolChoice{Type: "any"}
			case "none":
				tc = &outToolChoice{Type: "none"}
			case "tool":
				if c.Name != "" {
					tc = &outToolChoice{Type: "tool", Name: c.Name}
				} else {
					tc = &outToolChoice{Type: "any"}
				}
			}
		}
		if req.ParallelToolCalls != nil && !*req.ParallelToolCalls {
			if tc == nil {
				tc = &outToolChoice{Type: "auto"}
			}
			if tc.Type != "none" {
				tc.DisableParallelToolUse = true
			}
		}
		out.ToolChoice = tc
	} else if len(usedOrder) > 0 {
		// The API rejects tool_use history without tool definitions.
		for _, name := range usedOrder {
			out.Tools = append(out.Tools, outTool{Name: name, InputSchema: emptyToolSchema})
		}
		out.ToolChoice = &outToolChoice{Type: "none"}
	}

	budget := 0
	if r := req.Reasoning; r != nil && !r.Disabled {
		budget = r.BudgetTokens
		if budget <= 0 {
			budget = effortBudget(r.Effort)
		}
		if budget > 0 && budget < 1024 {
			budget = 1024
		}
	}
	if budget > 0 && out.ToolChoice != nil && (out.ToolChoice.Type == "any" || out.ToolChoice.Type == "tool") {
		budget = 0 // forced tool use is incompatible with extended thinking
	}
	if budget > 0 && !lastToolTurnSigned(req.Messages) {
		budget = 0
	}

	out.MaxTokens = req.MaxTokens
	if out.MaxTokens <= 0 {
		out.MaxTokens = 8192
		if budget > 0 {
			out.MaxTokens = 32000
		}
	}
	if budget > 0 {
		if out.MaxTokens <= budget {
			out.MaxTokens = budget + 8192
		}
		out.Thinking = &outThinking{Type: "enabled", BudgetTokens: budget}
	} else {
		out.Temperature, out.TopP, out.TopK = req.Temperature, req.TopP, req.TopK
	}
	return marshalNoEscape(&out)
}

func stablePartition(bs []outBlock, first func(*outBlock) bool) {
	n := 0
	for i := range bs {
		if first(&bs[i]) {
			if i != n {
				b := bs[i]
				copy(bs[n+1:i+1], bs[n:i])
				bs[n] = b
			}
			n++
		}
	}
}

func marshalNoEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("anthropic: encode: %w", err)
	}
	b := buf.Bytes()
	return b[:len(b)-1], nil
}
