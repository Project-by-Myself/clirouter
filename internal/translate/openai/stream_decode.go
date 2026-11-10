package openai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/Project-by-Myself/clirouter/internal/ir"
)

type chunk struct {
	ID      string          `json:"id"`
	Model   string          `json:"model"`
	Choices []chunkChoice   `json:"choices"`
	Usage   *chatUsage      `json:"usage"`
	Error   json.RawMessage `json:"error"`
}

type chunkChoice struct {
	Index        int        `json:"index"`
	Delta        chunkDelta `json:"delta"`
	FinishReason *string    `json:"finish_reason"`
}

type chunkDelta struct {
	Content          json.RawMessage `json:"content"`
	ReasoningContent *string         `json:"reasoning_content"`
	Reasoning        json.RawMessage `json:"reasoning"`
	Refusal          *string         `json:"refusal"`
	ToolCalls        []toolCallDelta `json:"tool_calls"`
}

type toolCallDelta struct {
	Index    *int   `json:"index"`
	ID       string `json:"id"`
	Function struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"function"`
}

type chatUsage struct {
	PromptTokens         int64  `json:"prompt_tokens"`
	CompletionTokens     int64  `json:"completion_tokens"`
	CachedTokens         *int64 `json:"cached_tokens"`
	PromptCacheHitTokens *int64 `json:"prompt_cache_hit_tokens"`
	PromptTokensDetails  *struct {
		CachedTokens     int64 `json:"cached_tokens"`
		CacheWriteTokens int64 `json:"cache_write_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionTokensDetails *struct {
		ReasoningTokens int64 `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

func (u *chatUsage) toIR() *ir.Usage {
	out := &ir.Usage{InputTokens: u.PromptTokens, OutputTokens: u.CompletionTokens}
	if d := u.PromptTokensDetails; d != nil {
		out.CacheReadTokens = d.CachedTokens
		out.CacheWriteTokens = d.CacheWriteTokens
	}
	if u.CachedTokens != nil {
		out.CacheReadTokens = max(out.CacheReadTokens, *u.CachedTokens)
	}
	if u.PromptCacheHitTokens != nil {
		out.CacheReadTokens = max(out.CacheReadTokens, *u.PromptCacheHitTokens)
	}
	if d := u.CompletionTokensDetails; d != nil {
		out.ReasoningTokens = d.ReasoningTokens
	}
	return out
}

type decTool struct {
	id, name string
	started  bool
	pending  []byte
}

type streamDecoder struct {
	started, finished, done bool
	tools                   []*decTool
	byIndex                 map[int]*decTool
}

// NewStreamDecoder returns a decoder for upstream chat.completion.chunk SSE.
func NewStreamDecoder() ir.StreamDecoder {
	return &streamDecoder{byIndex: make(map[int]*decTool, 2)}
}

var doneMarker = []byte("[DONE]")

func (d *streamDecoder) Decode(name, data []byte, emit func(ir.Event)) error {
	if d.done {
		return nil
	}
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return nil
	}
	if bytes.Equal(data, doneMarker) {
		d.finish(emit, "")
		emit(ir.Event{Kind: ir.EvDone})
		d.done = true
		return nil
	}
	if data[0] != '{' {
		return nil
	}
	var c chunk
	if err := json.Unmarshal(data, &c); err != nil {
		return fmt.Errorf("openai: invalid stream chunk: %w", err)
	}
	if !isNull(c.Error) {
		typ, msg := parseErrorValue(c.Error)
		emit(ir.Event{Kind: ir.EvError, Text: msg, ErrType: typ})
		return nil
	}
	if !d.started {
		d.started = true
		emit(ir.Event{Kind: ir.EvStart, ID: c.ID, Model: c.Model})
	}
	finish := ""
	for i := range c.Choices {
		ch := &c.Choices[i]
		if ch.Index != 0 {
			continue
		}
		d.delta(&ch.Delta, emit)
		if ch.FinishReason != nil && *ch.FinishReason != "" {
			finish = *ch.FinishReason
		}
	}
	if c.Usage != nil {
		if u := c.Usage.toIR(); *u != (ir.Usage{}) {
			emit(ir.Event{Kind: ir.EvUsage, Usage: u})
		}
	}
	if finish != "" {
		d.finish(emit, finish)
	}
	return nil
}

func (d *streamDecoder) delta(dl *chunkDelta, emit func(ir.Event)) {
	if dl.ReasoningContent != nil && *dl.ReasoningContent != "" {
		emit(ir.Event{Kind: ir.EvThinking, Text: *dl.ReasoningContent})
	} else if s, ok := rawString(dl.Reasoning); ok && s != "" {
		emit(ir.Event{Kind: ir.EvThinking, Text: s})
	}
	if len(dl.Content) > 0 {
		if s, ok := rawString(dl.Content); ok {
			if s != "" {
				emit(ir.Event{Kind: ir.EvText, Text: s})
			}
		} else if dl.Content[0] == '[' {
			decodeContentArray(dl.Content, emit)
		}
	}
	if dl.Refusal != nil && *dl.Refusal != "" {
		emit(ir.Event{Kind: ir.EvText, Text: *dl.Refusal})
	}
	for i := range dl.ToolCalls {
		d.toolDelta(&dl.ToolCalls[i], emit)
	}
}

// decodeContentArray handles providers (e.g. Mistral Magistral) that stream
// content as typed parts, including nested thinking parts.
func decodeContentArray(raw json.RawMessage, emit func(ir.Event)) {
	var parts []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		Thinking []struct {
			Text string `json:"text"`
		} `json:"thinking"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return
	}
	for _, p := range parts {
		switch p.Type {
		case "text":
			if p.Text != "" {
				emit(ir.Event{Kind: ir.EvText, Text: p.Text})
			}
		case "thinking":
			for _, t := range p.Thinking {
				if t.Text != "" {
					emit(ir.Event{Kind: ir.EvThinking, Text: t.Text})
				}
			}
		}
	}
}

func (d *streamDecoder) toolDelta(tc *toolCallDelta, emit func(ir.Event)) {
	if d.finished {
		return
	}
	t := d.lookupTool(tc)
	if t.name == "" && tc.Function.Name != "" {
		t.name = tc.Function.Name
	}
	args, ok := rawString(tc.Function.Arguments)
	if !ok && !isNull(tc.Function.Arguments) {
		args = string(bytes.TrimSpace(tc.Function.Arguments))
	}
	if !t.started && t.name != "" {
		d.startTool(t, emit)
	}
	if args == "" {
		return
	}
	if t.started {
		emit(ir.Event{Kind: ir.EvToolArgs, ID: t.id, Text: args})
	} else {
		t.pending = append(t.pending, args...)
	}
}

func (d *streamDecoder) startTool(t *decTool, emit func(ir.Event)) {
	t.started = true
	emit(ir.Event{Kind: ir.EvToolStart, ID: t.id, Name: t.name, ToolKind: ir.ToolFunction})
	if len(t.pending) > 0 {
		emit(ir.Event{Kind: ir.EvToolArgs, ID: t.id, Text: string(t.pending)})
		t.pending = nil
	}
}

func (d *streamDecoder) newTool(id string) *decTool {
	if id == "" {
		id = newCallID()
	}
	t := &decTool{id: id}
	d.tools = append(d.tools, t)
	return t
}

func (d *streamDecoder) lookupTool(tc *toolCallDelta) *decTool {
	if tc.Index != nil {
		t := d.byIndex[*tc.Index]
		// A different id at an already-used index is a new call (some
		// providers send every call at index 0); a repeated id is the same.
		if t == nil || (tc.ID != "" && tc.ID != t.id && t.started) {
			t = d.newTool(tc.ID)
			d.byIndex[*tc.Index] = t
		}
		return t
	}
	if tc.ID != "" {
		for _, t := range d.tools {
			if t.id == tc.ID {
				return t
			}
		}
		return d.newTool(tc.ID)
	}
	if n := len(d.tools); n > 0 {
		return d.tools[n-1]
	}
	return d.newTool("")
}

func (d *streamDecoder) finish(emit func(ir.Event), reason string) {
	if d.finished {
		return
	}
	d.finished = true
	for _, t := range d.tools {
		if !t.started {
			d.startTool(t, emit)
		}
		emit(ir.Event{Kind: ir.EvToolEnd, ID: t.id})
	}
	var fr ir.FinishReason
	switch reason {
	case "length", "max_tokens":
		fr = ir.FinishLength
	case "tool_calls", "function_call":
		fr = ir.FinishToolCalls
	case "content_filter":
		fr = ir.FinishContentFilter
	case "error":
		fr = ir.FinishError
	default:
		fr = ir.FinishStop
		if len(d.tools) > 0 {
			fr = ir.FinishToolCalls
		}
	}
	emit(ir.Event{Kind: ir.EvFinish, Finish: fr})
}

// parseErrorValue accepts {"message","type","code"} objects or bare strings.
func parseErrorValue(raw json.RawMessage) (errType, msg string) {
	if s, ok := rawString(raw); ok {
		return "api_error", s
	}
	var e struct {
		Message string          `json:"message"`
		Type    string          `json:"type"`
		Code    json.RawMessage `json:"code"`
	}
	if json.Unmarshal(raw, &e) != nil {
		return "api_error", string(raw)
	}
	errType = e.Type
	if errType == "" && !isNull(e.Code) {
		if s, ok := rawString(e.Code); ok {
			errType = s
		} else if n, err := strconv.Atoi(string(bytes.TrimSpace(e.Code))); err == nil {
			errType = typeForStatus(n)
		}
	}
	if errType == "" {
		errType = "api_error"
	}
	msg = e.Message
	if msg == "" {
		msg = string(raw)
	}
	return errType, msg
}
