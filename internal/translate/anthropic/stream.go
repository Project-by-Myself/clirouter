package anthropic

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/Project-by-Myself/clirouter/internal/ir"
	"github.com/Project-by-Myself/clirouter/internal/sse"
)

type wireUsage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
}

func (u *wireUsage) toIR() *ir.Usage {
	return &ir.Usage{
		InputTokens:      u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens,
		OutputTokens:     u.OutputTokens,
		CacheReadTokens:  u.CacheReadInputTokens,
		CacheWriteTokens: u.CacheCreationInputTokens,
	}
}

type wireError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type streamEvent struct {
	Type    string `json:"type"`
	Message *struct {
		ID    string     `json:"id"`
		Model string     `json:"model"`
		Usage *wireUsage `json:"usage"`
	} `json:"message"`
	Index        int `json:"index"`
	ContentBlock *struct {
		Type     string `json:"type"`
		ID       string `json:"id"`
		Name     string `json:"name"`
		Text     string `json:"text"`
		Thinking string `json:"thinking"`
		Data     string `json:"data"`
	} `json:"content_block"`
	Delta *struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		Thinking    string `json:"thinking"`
		Signature   string `json:"signature"`
		PartialJSON string `json:"partial_json"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
	Usage *wireUsage `json:"usage"`
	Error *wireError `json:"error"`
}

type blockKind uint8

const (
	bNone blockKind = iota
	bText
	bThinking
	bTool
	bOther
)

type decBlock struct {
	kind   blockKind
	id     string
	custom bool
	buf    []byte
}

type streamDecoder struct {
	blocks []decBlock
	custom map[string]bool
}

// NewStreamDecoder decodes upstream Messages API SSE events into IR events.
func NewStreamDecoder() ir.StreamDecoder { return &streamDecoder{} }

// NewStreamDecoderFor is NewStreamDecoder that also knows the request's
// custom (freeform) tools: their {"input": "..."} wrapper is unwrapped so the
// emitted tool args are the raw text and EvToolStart carries ToolCustom.
func NewStreamDecoderFor(req *ir.Request) ir.StreamDecoder {
	d := &streamDecoder{}
	if req != nil {
		for _, t := range req.Tools {
			if t.Kind == ir.ToolCustom {
				if d.custom == nil {
					d.custom = map[string]bool{}
				}
				d.custom[t.Name] = true
			}
		}
	}
	return d
}

func (d *streamDecoder) block(i int) *decBlock {
	if i < 0 {
		return nil
	}
	for len(d.blocks) <= i {
		d.blocks = append(d.blocks, decBlock{})
	}
	return &d.blocks[i]
}

func mapStopReason(s string) ir.FinishReason {
	switch s {
	case "max_tokens", "model_context_window_exceeded":
		return ir.FinishLength
	case "tool_use":
		return ir.FinishToolCalls
	case "refusal":
		return ir.FinishContentFilter
	}
	return ir.FinishStop
}

func (d *streamDecoder) Decode(name, data []byte, emit func(ir.Event)) error {
	if len(data) == 0 {
		return nil
	}
	var ev streamEvent
	if err := json.Unmarshal(data, &ev); err != nil {
		return fmt.Errorf("anthropic: decode stream event %q: %w", name, err)
	}
	typ := ev.Type
	if typ == "" {
		typ = string(name)
	}
	switch typ {
	case "message_start":
		if ev.Message == nil {
			return nil
		}
		emit(ir.Event{Kind: ir.EvStart, ID: ev.Message.ID, Model: ev.Message.Model})
		if ev.Message.Usage != nil {
			emit(ir.Event{Kind: ir.EvUsage, Usage: ev.Message.Usage.toIR()})
		}
	case "content_block_start":
		cb := ev.ContentBlock
		b := d.block(ev.Index)
		if cb == nil || b == nil {
			return nil
		}
		*b = decBlock{kind: bOther}
		switch cb.Type {
		case "text":
			b.kind = bText
			if cb.Text != "" {
				emit(ir.Event{Kind: ir.EvText, Text: cb.Text})
			}
		case "thinking":
			b.kind = bThinking
			if cb.Thinking != "" {
				emit(ir.Event{Kind: ir.EvThinking, Text: cb.Thinking})
			}
		case "redacted_thinking":
			emit(ir.Event{Kind: ir.EvRedactedThinking, Signature: cb.Data, SigSource: ir.SigAnthropic})
		case "tool_use":
			b.kind, b.id, b.custom = bTool, cb.ID, d.custom[cb.Name]
			kind := ir.ToolFunction
			if b.custom {
				kind = ir.ToolCustom
			}
			emit(ir.Event{Kind: ir.EvToolStart, ID: cb.ID, Name: cb.Name, ToolKind: kind})
		}
	case "content_block_delta":
		dl := ev.Delta
		if dl == nil {
			return nil
		}
		switch dl.Type {
		case "text_delta":
			if dl.Text != "" {
				emit(ir.Event{Kind: ir.EvText, Text: dl.Text})
			}
		case "thinking_delta":
			if dl.Thinking != "" {
				emit(ir.Event{Kind: ir.EvThinking, Text: dl.Thinking})
			}
		case "signature_delta":
			emit(ir.Event{Kind: ir.EvThinkingSignature, Signature: dl.Signature, SigSource: ir.SigAnthropic})
		case "input_json_delta":
			if ev.Index >= len(d.blocks) || d.blocks[ev.Index].kind != bTool || dl.PartialJSON == "" {
				return nil
			}
			b := &d.blocks[ev.Index]
			if b.custom {
				b.buf = append(b.buf, dl.PartialJSON...)
				return nil
			}
			emit(ir.Event{Kind: ir.EvToolArgs, ID: b.id, Text: dl.PartialJSON})
		}
	case "content_block_stop":
		if ev.Index >= len(d.blocks) {
			return nil
		}
		b := &d.blocks[ev.Index]
		if b.kind == bTool {
			if b.custom && len(b.buf) > 0 {
				var w struct {
					Input *string `json:"input"`
				}
				text := string(b.buf)
				if json.Unmarshal(b.buf, &w) == nil && w.Input != nil {
					text = *w.Input
				}
				emit(ir.Event{Kind: ir.EvToolArgs, ID: b.id, Text: text})
			}
			emit(ir.Event{Kind: ir.EvToolEnd, ID: b.id})
		}
		*b = decBlock{}
	case "message_delta":
		if ev.Usage != nil {
			emit(ir.Event{Kind: ir.EvUsage, Usage: ev.Usage.toIR()})
		}
		if ev.Delta != nil && ev.Delta.StopReason != "" {
			emit(ir.Event{Kind: ir.EvFinish, Finish: mapStopReason(ev.Delta.StopReason)})
		}
	case "message_stop":
		emit(ir.Event{Kind: ir.EvDone})
	case "error":
		e := ir.Event{Kind: ir.EvError, ErrType: "api_error", Text: "upstream error"}
		if ev.Error != nil {
			if ev.Error.Type != "" {
				e.ErrType = ev.Error.Type
			}
			if ev.Error.Message != "" {
				e.Text = ev.Error.Message
			}
		}
		emit(e)
	}
	return nil
}

// ---- client stream encoder ----

type streamEncoder struct {
	model       string
	id          string
	started     bool
	done        bool
	open        blockKind
	index       int
	thinkSigned bool
	toolID      string
	toolCustom  bool
	toolBuf     []byte
	sawTool     bool
	usage       ir.Usage
	finish      ir.FinishReason
	buf         []byte
}

// NewStreamEncoder renders IR events as client-facing Messages API SSE.
func NewStreamEncoder(req *ir.Request, model string) ir.StreamEncoder {
	if model == "" && req != nil {
		model = req.Model
	}
	return &streamEncoder{model: model, buf: make([]byte, 0, 256)}
}

func (e *streamEncoder) frame(dst []byte, name string) []byte {
	return sse.AppendEvent(dst, name, e.buf)
}

func (e *streamEncoder) ensureStart(dst []byte) []byte {
	if e.started {
		return dst
	}
	e.started = true
	if e.id == "" {
		e.id = newID("msg_")
	}
	in := e.usage.InputTokens - e.usage.CacheReadTokens - e.usage.CacheWriteTokens
	b := append(e.buf[:0], `{"type":"message_start","message":{"id":`...)
	b = appendJSONString(b, e.id)
	b = append(b, `,"type":"message","role":"assistant","model":`...)
	b = appendJSONString(b, e.model)
	b = append(b, `,"content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":`...)
	b = strconv.AppendInt(b, max(in, 0), 10)
	if e.usage.CacheReadTokens > 0 || e.usage.CacheWriteTokens > 0 {
		b = append(b, `,"cache_read_input_tokens":`...)
		b = strconv.AppendInt(b, e.usage.CacheReadTokens, 10)
		b = append(b, `,"cache_creation_input_tokens":`...)
		b = strconv.AppendInt(b, e.usage.CacheWriteTokens, 10)
	}
	b = append(b, `,"output_tokens":0}}}`...)
	e.buf = b
	return e.frame(dst, "message_start")
}

func (e *streamEncoder) closeBlock(dst []byte) []byte {
	if e.open == bNone {
		return dst
	}
	if e.open == bTool && e.toolCustom {
		payload := wrapString(`{"input":`, string(e.toolBuf))
		dst = e.delta(dst, "input_json_delta", "partial_json", string(payload))
		e.toolBuf = e.toolBuf[:0]
	}
	b := append(e.buf[:0], `{"type":"content_block_stop","index":`...)
	b = strconv.AppendInt(b, int64(e.index), 10)
	e.buf = append(b, '}')
	dst = e.frame(dst, "content_block_stop")
	e.index++
	e.open = bNone
	e.toolCustom = false
	return dst
}

// openBlock emits content_block_start; block is the content_block JSON.
func (e *streamEncoder) openBlock(dst []byte, kind blockKind, block func(b []byte) []byte) []byte {
	dst = e.closeBlock(dst)
	b := append(e.buf[:0], `{"type":"content_block_start","index":`...)
	b = strconv.AppendInt(b, int64(e.index), 10)
	b = append(b, `,"content_block":`...)
	b = block(b)
	e.buf = append(b, '}')
	e.open = kind
	return e.frame(dst, "content_block_start")
}

func (e *streamEncoder) delta(dst []byte, typ, field, val string) []byte {
	b := append(e.buf[:0], `{"type":"content_block_delta","index":`...)
	b = strconv.AppendInt(b, int64(e.index), 10)
	b = append(b, `,"delta":{"type":"`...)
	b = append(b, typ...)
	b = append(b, `","`...)
	b = append(b, field...)
	b = append(b, `":`...)
	b = appendJSONString(b, val)
	e.buf = append(b, '}', '}')
	return e.frame(dst, "content_block_delta")
}

func openText(b []byte) []byte { return append(b, `{"type":"text","text":""}`...) }
func openThinking(b []byte) []byte {
	return append(b, `{"type":"thinking","thinking":"","signature":""}`...)
}

func stopReason(f ir.FinishReason, sawTool bool) string {
	switch f {
	case ir.FinishLength:
		return "max_tokens"
	case ir.FinishToolCalls:
		return "tool_use"
	case ir.FinishContentFilter:
		return "refusal"
	case ir.FinishStop:
		return "end_turn"
	}
	if sawTool {
		return "tool_use"
	}
	return "end_turn"
}

func appendUsage(b []byte, u *ir.Usage) []byte {
	in := max(u.InputTokens-u.CacheReadTokens-u.CacheWriteTokens, 0)
	b = append(b, `{"input_tokens":`...)
	b = strconv.AppendInt(b, in, 10)
	b = append(b, `,"output_tokens":`...)
	b = strconv.AppendInt(b, u.OutputTokens, 10)
	b = append(b, `,"cache_read_input_tokens":`...)
	b = strconv.AppendInt(b, u.CacheReadTokens, 10)
	b = append(b, `,"cache_creation_input_tokens":`...)
	b = strconv.AppendInt(b, u.CacheWriteTokens, 10)
	return append(b, '}')
}

func (e *streamEncoder) Encode(dst []byte, ev ir.Event) []byte {
	if e.done {
		return dst
	}
	switch ev.Kind {
	case ir.EvStart:
		if e.id == "" && !e.started {
			e.id = ev.ID
		}
		if e.model == "" {
			e.model = ev.Model
		}
		dst = e.ensureStart(dst)
	case ir.EvText:
		if ev.Text == "" {
			return dst
		}
		dst = e.ensureStart(dst)
		if e.open != bText {
			dst = e.openBlock(dst, bText, openText)
		}
		dst = e.delta(dst, "text_delta", "text", ev.Text)
	case ir.EvThinking:
		if ev.Text == "" {
			return dst
		}
		dst = e.ensureStart(dst)
		if e.open != bThinking || e.thinkSigned {
			dst = e.openBlock(dst, bThinking, openThinking)
			e.thinkSigned = false
		}
		dst = e.delta(dst, "thinking_delta", "thinking", ev.Text)
	case ir.EvThinkingSignature:
		if ev.Signature == "" {
			return dst
		}
		dst = e.ensureStart(dst)
		if e.open != bThinking || e.thinkSigned {
			dst = e.openBlock(dst, bThinking, openThinking)
		}
		dst = e.delta(dst, "signature_delta", "signature", encodeSig(ev.SigSource, ev.Signature))
		e.thinkSigned = true
	case ir.EvRedactedThinking:
		if ev.Signature == "" {
			return dst
		}
		dst = e.ensureStart(dst)
		data := encodeSig(ev.SigSource, ev.Signature)
		dst = e.openBlock(dst, bThinking, func(b []byte) []byte {
			b = append(b, `{"type":"redacted_thinking","data":`...)
			b = appendJSONString(b, data)
			return append(b, '}')
		})
		e.open = bOther
		dst = e.closeBlock(dst)
	case ir.EvToolStart:
		dst = e.ensureStart(dst)
		id := ev.ID
		if id == "" {
			id = newID("toolu_")
		}
		dst = e.openBlock(dst, bTool, func(b []byte) []byte {
			b = append(b, `{"type":"tool_use","id":`...)
			b = appendJSONString(b, id)
			b = append(b, `,"name":`...)
			b = appendJSONString(b, ev.Name)
			return append(b, `,"input":{}}`...)
		})
		e.toolID, e.toolCustom, e.sawTool = ev.ID, ev.ToolKind == ir.ToolCustom, true
	case ir.EvToolArgs:
		if e.open != bTool || ev.Text == "" || (ev.ID != "" && e.toolID != "" && ev.ID != e.toolID) {
			return dst
		}
		if e.toolCustom {
			e.toolBuf = append(e.toolBuf, ev.Text...)
			return dst
		}
		dst = e.delta(dst, "input_json_delta", "partial_json", ev.Text)
	case ir.EvToolEnd:
		if e.open == bTool && (ev.ID == "" || e.toolID == "" || ev.ID == e.toolID) {
			dst = e.closeBlock(dst)
		}
	case ir.EvUsage:
		e.usage.Merge(ev.Usage)
	case ir.EvFinish:
		e.finish = ev.Finish
	case ir.EvError:
		dst = e.closeBlock(dst)
		dst = append(dst, StreamErrorEvent(ev.ErrType, ev.Text)...)
		e.done = true
	case ir.EvDone:
		dst = e.ensureStart(dst)
		dst = e.closeBlock(dst)
		b := append(e.buf[:0], `{"type":"message_delta","delta":{"stop_reason":"`...)
		b = append(b, stopReason(e.finish, e.sawTool)...)
		b = append(b, `","stop_sequence":null},"usage":`...)
		b = appendUsage(b, &e.usage)
		e.buf = append(b, '}')
		dst = e.frame(dst, "message_delta")
		e.buf = append(e.buf[:0], `{"type":"message_stop"}`...)
		dst = e.frame(dst, "message_stop")
		e.done = true
	}
	return dst
}
