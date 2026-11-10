package responses

import (
	"strconv"
	"strings"
	"time"

	"github.com/Project-by-Myself/clirouter/internal/ir"
	"github.com/Project-by-Myself/clirouter/internal/sse"
)

type respHeader struct {
	id         string
	model      string
	created    int64
	status     string
	errCode    string
	errMsg     string
	incomplete string
}

// appendResponseObject writes a Responses API response object. output is the
// comma-separated JSON of finished items.
func appendResponseObject(b []byte, h respHeader, output []byte, usage *ir.Usage) []byte {
	b = append(b, `{"id":`...)
	b = appendJSONString(b, h.id)
	b = append(b, `,"object":"response"`...)
	b = appendIntField(b, "created_at", h.created)
	b = appendStrField(b, "status", h.status)
	b = appendKey(b, "error")
	if h.status == "failed" {
		b = append(b, `{"code":`...)
		b = appendJSONString(b, h.errCode)
		b = appendStrField(b, "message", h.errMsg)
		b = append(b, '}')
	} else {
		b = append(b, "null"...)
	}
	b = appendKey(b, "incomplete_details")
	if h.incomplete != "" {
		b = append(b, `{"reason":`...)
		b = appendJSONString(b, h.incomplete)
		b = append(b, '}')
	} else {
		b = append(b, "null"...)
	}
	b = appendStrField(b, "model", h.model)
	b = append(b, `,"output":[`...)
	b = append(b, output...)
	b = append(b, `],"parallel_tool_calls":true,"tool_choice":"auto","tools":[]`...)
	b = appendKey(b, "usage")
	if usage == nil {
		b = append(b, "null"...)
	} else {
		b = append(b, `{"input_tokens":`...)
		b = strconv.AppendInt(b, usage.InputTokens, 10)
		b = append(b, `,"input_tokens_details":{"cached_tokens":`...)
		b = strconv.AppendInt(b, usage.CacheReadTokens, 10)
		b = append(b, `},"output_tokens":`...)
		b = strconv.AppendInt(b, usage.OutputTokens, 10)
		b = append(b, `,"output_tokens_details":{"reasoning_tokens":`...)
		b = strconv.AppendInt(b, usage.ReasoningTokens, 10)
		b = append(b, `},"total_tokens":`...)
		b = strconv.AppendInt(b, usage.InputTokens+usage.OutputTokens, 10)
		b = append(b, '}')
	}
	return append(b, '}')
}

type itemKind uint8

const (
	itemNone itemKind = iota
	itemText
	itemReasoning
	itemTool
)

type streamEncoder struct {
	customTools map[string]bool
	hdr         respHeader
	seq         int64
	started     bool
	terminal    bool
	quiet       bool

	nextIdx  int
	kind     itemKind
	itemID   string
	idx      int
	text     strings.Builder
	partOpen bool
	sig      string
	sigSrc   string
	callID   string
	name     string
	custom   bool

	output  []byte
	usage   ir.Usage
	hasUse  bool
	finish  ir.FinishReason
	sawTool bool

	evType string
	buf    []byte
	item   []byte
}

func NewStreamEncoder(req *ir.Request, model string) ir.StreamEncoder {
	return newStreamEncoder(req, model)
}

func newStreamEncoder(req *ir.Request, model string) *streamEncoder {
	e := &streamEncoder{hdr: respHeader{id: newID("resp_"), model: model, created: time.Now().Unix()}}
	if req != nil {
		for _, t := range req.Tools {
			if t.Kind == ir.ToolCustom {
				if e.customTools == nil {
					e.customTools = map[string]bool{}
				}
				e.customTools[t.Name] = true
			}
		}
	}
	return e
}

func (e *streamEncoder) begin(typ string) {
	e.evType = typ
	b := append(e.buf[:0], `{"type":"`...)
	b = append(b, typ...)
	b = append(b, `","sequence_number":`...)
	e.buf = strconv.AppendInt(b, e.seq, 10)
	e.seq++
}

func (e *streamEncoder) end(dst []byte) []byte {
	e.buf = append(e.buf, '}')
	if e.quiet {
		return dst
	}
	return sse.AppendEvent(dst, e.evType, e.buf)
}

func (e *streamEncoder) itemRef() {
	e.buf = appendStrField(e.buf, "item_id", e.itemID)
	e.buf = appendIntField(e.buf, "output_index", int64(e.idx))
}

func (e *streamEncoder) ensureStarted(dst []byte) []byte {
	if e.started {
		return dst
	}
	e.started = true
	h := e.hdr
	h.status = "in_progress"
	for _, typ := range [...]string{"response.created", "response.in_progress"} {
		e.begin(typ)
		e.buf = appendKey(e.buf, "response")
		e.buf = appendResponseObject(e.buf, h, nil, nil)
		dst = e.end(dst)
	}
	return dst
}

func (e *streamEncoder) openItem(kind itemKind, prefix string) {
	e.kind = kind
	e.itemID = newID(prefix)
	e.idx = e.nextIdx
	e.nextIdx++
	e.text.Reset()
	e.partOpen = false
	e.sig, e.sigSrc = "", ""
}

func (e *streamEncoder) emitAdded(dst []byte) []byte {
	e.begin("response.output_item.added")
	e.buf = appendIntField(e.buf, "output_index", int64(e.idx))
	e.buf = appendKey(e.buf, "item")
	e.buf = e.appendItem(e.buf, false)
	return e.end(dst)
}

// appendItem writes the current item; done selects the completed form.
func (e *streamEncoder) appendItem(b []byte, done bool) []byte {
	status := "in_progress"
	text := ""
	if done {
		status = "completed"
		text = e.text.String()
	}
	b = append(b, `{"id":`...)
	b = appendJSONString(b, e.itemID)
	switch e.kind {
	case itemText:
		b = append(b, `,"type":"message"`...)
		b = appendStrField(b, "status", status)
		b = append(b, `,"role":"assistant","content":[`...)
		if done {
			b = append(b, `{"type":"output_text","text":`...)
			b = appendJSONString(b, text)
			b = append(b, `,"annotations":[],"logprobs":[]}`...)
		}
		b = append(b, ']')
	case itemReasoning:
		b = append(b, `,"type":"reasoning","summary":[`...)
		if done && e.partOpen {
			b = append(b, `{"type":"summary_text","text":`...)
			b = appendJSONString(b, text)
			b = append(b, '}')
		}
		b = append(b, ']')
		if done {
			if enc := e.encrypted(text); enc != "" {
				b = appendStrField(b, "encrypted_content", enc)
			}
		}
	case itemTool:
		typ, field := "function_call", "arguments"
		if e.custom {
			typ, field = "custom_tool_call", "input"
		}
		b = appendStrField(b, "type", typ)
		b = appendStrField(b, "status", status)
		b = appendStrField(b, "call_id", e.callID)
		b = appendStrField(b, "name", e.name)
		b = appendStrField(b, field, text)
	}
	return append(b, '}')
}

func (e *streamEncoder) encrypted(text string) string {
	if e.sigSrc == ir.SigOpenAI {
		return e.sig
	}
	if e.sig == "" && e.sigSrc == "" && text == "" {
		return ""
	}
	return EncodeEnvelope(e.sigSrc, e.sig, text)
}

func (e *streamEncoder) closeItem(dst []byte) []byte {
	if e.kind == itemNone {
		return dst
	}
	text := e.text.String()
	switch e.kind {
	case itemText:
		e.begin("response.output_text.done")
		e.itemRef()
		e.buf = append(e.buf, `,"content_index":0,"text":`...)
		e.buf = appendJSONString(e.buf, text)
		e.buf = append(e.buf, `,"logprobs":[]`...)
		dst = e.end(dst)
		e.begin("response.content_part.done")
		e.itemRef()
		e.buf = append(e.buf, `,"content_index":0,"part":{"type":"output_text","text":`...)
		e.buf = appendJSONString(e.buf, text)
		e.buf = append(e.buf, `,"annotations":[]}`...)
		dst = e.end(dst)
	case itemReasoning:
		if e.partOpen {
			e.begin("response.reasoning_summary_text.done")
			e.itemRef()
			e.buf = append(e.buf, `,"summary_index":0,"text":`...)
			e.buf = appendJSONString(e.buf, text)
			dst = e.end(dst)
			e.begin("response.reasoning_summary_part.done")
			e.itemRef()
			e.buf = append(e.buf, `,"summary_index":0,"part":{"type":"summary_text","text":`...)
			e.buf = appendJSONString(e.buf, text)
			e.buf = append(e.buf, '}')
			dst = e.end(dst)
		}
	case itemTool:
		typ, field := "response.function_call_arguments.done", "arguments"
		if e.custom {
			typ, field = "response.custom_tool_call_input.done", "input"
		}
		e.begin(typ)
		e.itemRef()
		e.buf = appendStrField(e.buf, field, text)
		dst = e.end(dst)
	}
	e.item = e.appendItem(e.item[:0], true)
	if len(e.output) > 0 {
		e.output = append(e.output, ',')
	}
	e.output = append(e.output, e.item...)
	e.begin("response.output_item.done")
	e.buf = appendIntField(e.buf, "output_index", int64(e.idx))
	e.buf = appendKey(e.buf, "item")
	e.buf = append(e.buf, e.item...)
	dst = e.end(dst)
	e.kind = itemNone
	return dst
}

func (e *streamEncoder) Encode(dst []byte, ev ir.Event) []byte {
	if e.terminal {
		return dst
	}
	switch ev.Kind {
	case ir.EvStart:
		if e.hdr.model == "" {
			e.hdr.model = ev.Model
		}
		return e.ensureStarted(dst)
	case ir.EvText:
		dst = e.ensureStarted(dst)
		if e.kind != itemText {
			dst = e.closeItem(dst)
			e.openItem(itemText, "msg_")
			dst = e.emitAdded(dst)
			e.begin("response.content_part.added")
			e.itemRef()
			e.buf = append(e.buf, `,"content_index":0,"part":{"type":"output_text","text":"","annotations":[]}`...)
			dst = e.end(dst)
		}
		if ev.Text == "" {
			return dst
		}
		e.text.WriteString(ev.Text)
		e.begin("response.output_text.delta")
		e.itemRef()
		e.buf = append(e.buf, `,"content_index":0,"delta":`...)
		e.buf = appendJSONString(e.buf, ev.Text)
		e.buf = append(e.buf, `,"logprobs":[]`...)
		return e.end(dst)
	case ir.EvThinking:
		dst = e.ensureStarted(dst)
		if e.kind == itemReasoning && e.sig != "" {
			dst = e.closeItem(dst)
		}
		dst = e.openReasoning(dst)
		if ev.Text == "" {
			return dst
		}
		if !e.partOpen {
			e.partOpen = true
			e.begin("response.reasoning_summary_part.added")
			e.itemRef()
			e.buf = append(e.buf, `,"summary_index":0,"part":{"type":"summary_text","text":""}`...)
			dst = e.end(dst)
		}
		e.text.WriteString(ev.Text)
		e.begin("response.reasoning_summary_text.delta")
		e.itemRef()
		e.buf = append(e.buf, `,"summary_index":0,"delta":`...)
		e.buf = appendJSONString(e.buf, ev.Text)
		return e.end(dst)
	case ir.EvThinkingSignature:
		dst = e.ensureStarted(dst)
		if e.kind == itemReasoning && e.sig != "" {
			dst = e.closeItem(dst)
		}
		dst = e.openReasoning(dst)
		e.sig, e.sigSrc = ev.Signature, ev.SigSource
		return dst
	case ir.EvRedactedThinking:
		dst = e.ensureStarted(dst)
		dst = e.closeItem(dst)
		e.openItem(itemReasoning, "rs_")
		dst = e.emitAdded(dst)
		src := ev.SigSource
		if src == "" {
			src = ir.SigAnthropic
		}
		e.sig, e.sigSrc = ev.Signature, src+redactedSuffix
		return e.closeItem(dst)
	case ir.EvToolStart:
		dst = e.ensureStarted(dst)
		dst = e.closeItem(dst)
		e.openItem(itemTool, "fc_")
		e.callID, e.name = ev.ID, ev.Name
		e.custom = ev.ToolKind == ir.ToolCustom || e.customTools[ev.Name]
		if e.custom {
			e.itemID = "ctc_" + e.itemID[3:]
		}
		e.sawTool = true
		return e.emitAdded(dst)
	case ir.EvToolArgs:
		if e.kind != itemTool || (ev.ID != "" && ev.ID != e.callID) || ev.Text == "" {
			return dst
		}
		e.text.WriteString(ev.Text)
		if e.custom {
			e.begin("response.custom_tool_call_input.delta")
		} else {
			e.begin("response.function_call_arguments.delta")
		}
		e.itemRef()
		e.buf = appendStrField(e.buf, "delta", ev.Text)
		return e.end(dst)
	case ir.EvToolEnd:
		if e.kind == itemTool && (ev.ID == "" || ev.ID == e.callID) {
			return e.closeItem(dst)
		}
		return dst
	case ir.EvUsage:
		if ev.Usage != nil {
			e.usage.Merge(ev.Usage)
			e.hasUse = true
		}
		return dst
	case ir.EvFinish:
		e.finish = ev.Finish
		return dst
	case ir.EvError:
		e.terminal = true
		h := e.hdr
		h.status, h.errCode, h.errMsg = "failed", ev.ErrType, ev.Text
		if h.errCode == "" {
			h.errCode = "server_error"
		}
		e.begin("response.failed")
		e.buf = appendKey(e.buf, "response")
		e.buf = appendResponseObject(e.buf, h, e.output, nil)
		return e.end(dst)
	case ir.EvDone:
		dst = e.ensureStarted(dst)
		dst = e.closeItem(dst)
		e.terminal = true
		typ := "response.completed"
		h := e.final()
		if h.status == "incomplete" {
			typ = "response.incomplete"
		}
		e.begin(typ)
		e.buf = appendKey(e.buf, "response")
		e.buf = appendResponseObject(e.buf, h, e.output, &e.usage)
		return e.end(dst)
	}
	return dst
}

func (e *streamEncoder) openReasoning(dst []byte) []byte {
	if e.kind == itemReasoning {
		return dst
	}
	dst = e.closeItem(dst)
	e.openItem(itemReasoning, "rs_")
	return e.emitAdded(dst)
}

func (e *streamEncoder) final() respHeader {
	h := e.hdr
	h.status = "completed"
	switch e.finish {
	case ir.FinishLength:
		h.status, h.incomplete = "incomplete", "max_output_tokens"
	case ir.FinishContentFilter:
		h.status, h.incomplete = "incomplete", "content_filter"
	}
	return h
}

// EncodeResponse renders an aggregated turn as a non-streaming response object.
func EncodeResponse(req *ir.Request, resp *ir.Response) ([]byte, error) {
	model := resp.Model
	if req != nil && req.Model != "" {
		model = req.Model
	}
	e := newStreamEncoder(req, model)
	e.quiet = true
	e.started = true
	prev := ir.PartType(255)
	for i := range resp.Content {
		p := &resp.Content[i]
		if !(p.Type == ir.PartText && prev == ir.PartText) {
			e.closeItem(nil)
		}
		prev = p.Type
		switch p.Type {
		case ir.PartText:
			e.Encode(nil, ir.Event{Kind: ir.EvText, Text: p.Text})
		case ir.PartThinking:
			e.Encode(nil, ir.Event{Kind: ir.EvThinking, Text: p.Text})
			if p.Signature != "" || p.SigSource != "" {
				e.Encode(nil, ir.Event{Kind: ir.EvThinkingSignature, Signature: p.Signature, SigSource: p.SigSource})
			}
		case ir.PartRedactedThinking:
			e.Encode(nil, ir.Event{Kind: ir.EvRedactedThinking, Signature: p.Signature, SigSource: p.SigSource})
		case ir.PartToolCall:
			e.Encode(nil, ir.Event{Kind: ir.EvToolStart, ID: p.ToolID, Name: p.ToolName, ToolKind: p.ToolKind})
			e.Encode(nil, ir.Event{Kind: ir.EvToolArgs, ID: p.ToolID, Text: p.Args})
			e.Encode(nil, ir.Event{Kind: ir.EvToolEnd, ID: p.ToolID})
		}
	}
	e.closeItem(nil)
	e.finish = resp.Finish
	u := resp.Usage
	return appendResponseObject(nil, e.final(), e.output, &u), nil
}
