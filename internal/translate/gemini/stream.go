package gemini

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"

	"github.com/Project-by-Myself/clirouter/internal/ir"
)

type streamDecoder struct {
	custom  map[string]bool
	started bool
	sawTool bool
	done    bool
}

// NewStreamDecoder decodes upstream streamGenerateContent?alt=sse chunks.
func NewStreamDecoder() ir.StreamDecoder { return &streamDecoder{} }

// NewStreamDecoderFor is NewStreamDecoder that also knows the request's custom
// (freeform) tools, so their {"input": ...} wrapper is unwrapped back to raw
// text and the tool call is reported with ToolKind custom.
func NewStreamDecoderFor(req *ir.Request) ir.StreamDecoder {
	d := &streamDecoder{}
	if req != nil {
		for _, t := range req.Tools {
			if t.Kind == ir.ToolCustom {
				if d.custom == nil {
					d.custom = make(map[string]bool)
				}
				d.custom[t.Name] = true
			}
		}
	}
	return d
}

func newCallID() string { return "call_" + rand.Text()[:24] }

func (d *streamDecoder) Decode(_, data []byte, emit func(ir.Event)) error {
	data = bytes.TrimSpace(data)
	if d.done || len(data) == 0 || string(data) == "[DONE]" {
		return nil
	}
	var raw gResponse
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("gemini: decode stream chunk: %w", err)
	}
	r := raw.unwrap()
	if r.Error != nil {
		d.done = true
		emit(ir.Event{Kind: ir.EvError, Text: r.Error.Message, ErrType: errTypeFromStatus(r.Error.Status, r.Error.Code)})
		return nil
	}
	if !d.started {
		d.started = true
		emit(ir.Event{Kind: ir.EvStart, ID: r.ResponseID, Model: r.ModelVersion})
	}
	if len(r.Candidates) == 0 && r.PromptFeedback != nil && r.PromptFeedback.BlockReason != "" {
		d.done = true
		msg := "prompt blocked: " + r.PromptFeedback.BlockReason
		if r.PromptFeedback.BlockReasonMessage != "" {
			msg += ": " + r.PromptFeedback.BlockReasonMessage
		}
		emit(ir.Event{Kind: ir.EvError, Text: msg, ErrType: "content_filter"})
		return nil
	}
	finish := ""
	if len(r.Candidates) > 0 {
		c := &r.Candidates[0]
		if c.Content != nil {
			for i := range c.Content.Parts {
				d.part(&c.Content.Parts[i], emit)
			}
		}
		finish = c.FinishReason
		if finish == "FINISH_REASON_UNSPECIFIED" {
			finish = ""
		}
	}
	if finish != "" {
		emit(ir.Event{Kind: ir.EvFinish, Finish: d.mapFinish(finish)})
	}
	if u := r.UsageMetadata; u != nil {
		emit(ir.Event{Kind: ir.EvUsage, Usage: usageFromG(u)})
	}
	if finish != "" {
		d.done = true
		emit(ir.Event{Kind: ir.EvDone})
	}
	return nil
}

func (d *streamDecoder) part(p *gPart, emit func(ir.Event)) {
	switch {
	case p.FunctionCall != nil:
		fc := p.FunctionCall
		id := fc.ID
		if id == "" {
			id = newCallID()
		}
		d.sawTool = true
		if p.ThoughtSignature != "" {
			RememberSignature(id, p.ThoughtSignature)
		}
		args := bytes.TrimSpace(fc.Args)
		if len(args) == 0 || string(args) == "null" {
			args = []byte("{}")
		}
		kind := ir.ToolFunction
		text := string(args)
		if d.custom[fc.Name] {
			kind = ir.ToolCustom
			var in struct {
				Input json.RawMessage `json:"input"`
			}
			if json.Unmarshal(args, &in) == nil && len(in.Input) > 0 {
				text = rawText(in.Input)
			}
		}
		emit(ir.Event{Kind: ir.EvToolStart, ID: id, Name: fc.Name, ToolKind: kind})
		emit(ir.Event{Kind: ir.EvToolArgs, ID: id, Text: text})
		emit(ir.Event{Kind: ir.EvToolEnd, ID: id})
	case p.Text != nil:
		if *p.Text != "" {
			k := ir.EvText
			if p.Thought {
				k = ir.EvThinking
			}
			emit(ir.Event{Kind: k, Text: *p.Text})
		}
		if p.ThoughtSignature != "" {
			emit(ir.Event{Kind: ir.EvThinkingSignature, Signature: p.ThoughtSignature, SigSource: ir.SigGemini})
		}
	case p.ExecutableCode != nil || p.CodeExecutionResult != nil:
		emit(ir.Event{Kind: ir.EvText, Text: codeText(p)})
	}
}

func (d *streamDecoder) mapFinish(f string) ir.FinishReason {
	switch f {
	case "STOP":
		if d.sawTool {
			return ir.FinishToolCalls
		}
		return ir.FinishStop
	case "MAX_TOKENS":
		return ir.FinishLength
	case "SAFETY", "RECITATION", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII", "IMAGE_SAFETY", "LANGUAGE":
		return ir.FinishContentFilter
	}
	return ir.FinishError
}

func usageFromG(u *gUsage) *ir.Usage {
	return &ir.Usage{
		InputTokens:     u.PromptTokenCount,
		OutputTokens:    u.CandidatesTokenCount + u.ThoughtsTokenCount,
		CacheReadTokens: u.CachedContentTokenCount,
		ReasoningTokens: u.ThoughtsTokenCount,
	}
}

type pendingTool struct {
	id, name string
	kind     ir.ToolKind
	args     []byte
}

type streamEncoder struct {
	model     string
	id        string
	custom    map[string]bool
	suffix    []byte
	lastText  bool // last emitted part was non-thought text
	tools     []pendingTool
	finish    ir.FinishReason
	usage     ir.Usage
	haveUsage bool
	done      bool
}

// NewStreamEncoder emits "data: {GenerateContentResponse}\n\n" chunks for a
// Gemini client. Gemini streams have no event names and no [DONE] marker.
func NewStreamEncoder(req *ir.Request, model string) ir.StreamEncoder {
	e := &streamEncoder{model: model}
	if req != nil {
		if e.model == "" {
			e.model = req.Model
		}
		for _, t := range req.Tools {
			if t.Kind == ir.ToolCustom {
				if e.custom == nil {
					e.custom = make(map[string]bool)
				}
				e.custom[t.Name] = true
			}
		}
	}
	return e
}

const chunkPrefix = `data: {"candidates":[{"content":{"role":"model","parts":[`

func (e *streamEncoder) ensureID() {
	if e.id == "" {
		e.id = rand.Text()[:22]
	}
}

func (e *streamEncoder) tail(dst []byte) []byte {
	if e.suffix == nil {
		e.ensureID()
		s := append(make([]byte, 0, 64+len(e.model)+len(e.id)), `]},"index":0}],"modelVersion":`...)
		s = appendString(s, e.model)
		s = append(s, `,"responseId":`...)
		s = appendString(s, e.id)
		e.suffix = append(s, "}\n\n"...)
	}
	return append(dst, e.suffix...)
}

func (e *streamEncoder) Encode(dst []byte, ev ir.Event) []byte {
	if e.done {
		return dst
	}
	switch ev.Kind {
	case ir.EvStart:
		if e.model == "" {
			e.model = ev.Model
		}
		if e.id == "" {
			e.id = ev.ID
		}
		e.suffix = nil
	case ir.EvText:
		if ev.Text == "" {
			return dst
		}
		dst = append(dst, chunkPrefix...)
		dst = appendTextPart(dst, ev.Text, false, "")
		e.lastText = true
		return e.tail(dst)
	case ir.EvThinking:
		if ev.Text == "" {
			return dst
		}
		dst = append(dst, chunkPrefix...)
		dst = appendTextPart(dst, ev.Text, true, "")
		e.lastText = false
		return e.tail(dst)
	case ir.EvThinkingSignature:
		if ev.Signature == "" {
			return dst
		}
		dst = append(dst, chunkPrefix...)
		dst = appendTextPart(dst, "", !e.lastText, tunnelSig(ev.SigSource, ev.Signature, false))
		return e.tail(dst)
	case ir.EvRedactedThinking:
		dst = append(dst, chunkPrefix...)
		dst = appendTextPart(dst, "", true, tunnelSig(ev.SigSource, ev.Signature, true))
		e.lastText = false
		return e.tail(dst)
	case ir.EvToolStart:
		kind := ev.ToolKind
		if e.custom[ev.Name] {
			kind = ir.ToolCustom
		}
		e.tools = append(e.tools, pendingTool{id: ev.ID, name: ev.Name, kind: kind})
	case ir.EvToolArgs:
		if t := e.tool(ev.ID); t != nil {
			t.args = append(t.args, ev.Text...)
		}
	case ir.EvToolEnd:
		for i := range e.tools {
			if e.tools[i].id == ev.ID {
				t := e.tools[i]
				e.tools = append(e.tools[:i], e.tools[i+1:]...)
				return e.emitTool(dst, &t)
			}
		}
	case ir.EvUsage:
		if ev.Usage != nil {
			e.usage.Merge(ev.Usage)
			e.haveUsage = true
		}
	case ir.EvFinish:
		e.finish = ev.Finish
	case ir.EvError:
		e.done = true
		return append(dst, StreamErrorEvent(ev.ErrType, ev.Text)...)
	case ir.EvDone:
		for len(e.tools) > 0 {
			t := e.tools[0]
			e.tools = e.tools[1:]
			dst = e.emitTool(dst, &t)
		}
		e.done = true
		e.ensureID()
		dst = append(dst, chunkPrefix...)
		dst = append(dst, `{"text":""}]},"finishReason":`...)
		dst = appendString(dst, geminiFinish(e.finish))
		dst = append(dst, `,"index":0}]`...)
		if e.haveUsage {
			dst = append(dst, `,"usageMetadata":`...)
			dst = appendUsage(dst, &e.usage)
		}
		dst = append(dst, `,"modelVersion":`...)
		dst = appendString(dst, e.model)
		dst = append(dst, `,"responseId":`...)
		dst = appendString(dst, e.id)
		return append(dst, "}\n\n"...)
	}
	return dst
}

func (e *streamEncoder) tool(id string) *pendingTool {
	for i := len(e.tools) - 1; i >= 0; i-- {
		if e.tools[i].id == id {
			return &e.tools[i]
		}
	}
	return nil
}

func (e *streamEncoder) emitTool(dst []byte, t *pendingTool) []byte {
	dst = append(dst, chunkPrefix...)
	dst = appendCallPart(dst, t.id, t.name, string(t.args), t.kind == ir.ToolCustom)
	e.lastText = false
	return e.tail(dst)
}

func appendTextPart(dst []byte, text string, thought bool, sig string) []byte {
	dst = append(dst, `{"text":`...)
	dst = appendString(dst, text)
	if thought {
		dst = append(dst, `,"thought":true`...)
	}
	if sig != "" {
		dst = append(dst, `,"thoughtSignature":`...)
		dst = appendString(dst, sig)
	}
	return append(dst, '}')
}

func appendCallPart(dst []byte, id, name, args string, custom bool) []byte {
	dst = append(dst, `{"functionCall":{`...)
	if id != "" {
		dst = append(dst, `"id":`...)
		dst = appendString(dst, id)
		dst = append(dst, ',')
	}
	dst = append(dst, `"name":`...)
	dst = appendString(dst, name)
	dst = append(dst, `,"args":`...)
	if custom {
		dst = append(dst, `{"input":`...)
		dst = appendString(dst, args)
		dst = append(dst, '}')
	} else {
		dst = append(dst, objectArgs(args)...)
	}
	dst = append(dst, '}')
	if sig, ok := LookupSignature(id); ok {
		dst = append(dst, `,"thoughtSignature":`...)
		dst = appendString(dst, sig)
	}
	return append(dst, '}')
}

func appendUsage(dst []byte, u *ir.Usage) []byte {
	cand := u.OutputTokens - u.ReasoningTokens
	if cand < 0 {
		cand = 0
	}
	dst = append(dst, `{"promptTokenCount":`...)
	dst = appendInt(dst, u.InputTokens)
	dst = append(dst, `,"candidatesTokenCount":`...)
	dst = appendInt(dst, cand)
	if u.ReasoningTokens > 0 {
		dst = append(dst, `,"thoughtsTokenCount":`...)
		dst = appendInt(dst, u.ReasoningTokens)
	}
	if u.CacheReadTokens > 0 {
		dst = append(dst, `,"cachedContentTokenCount":`...)
		dst = appendInt(dst, u.CacheReadTokens)
	}
	dst = append(dst, `,"totalTokenCount":`...)
	dst = appendInt(dst, u.InputTokens+u.OutputTokens)
	return append(dst, '}')
}

func geminiFinish(f ir.FinishReason) string {
	switch f {
	case ir.FinishLength:
		return "MAX_TOKENS"
	case ir.FinishContentFilter:
		return "SAFETY"
	case ir.FinishError:
		return "OTHER"
	}
	return "STOP"
}

// EncodeResponse renders an aggregated turn as a single GenerateContentResponse.
func EncodeResponse(req *ir.Request, resp *ir.Response) ([]byte, error) {
	if resp == nil {
		return nil, fmt.Errorf("gemini: nil response")
	}
	var custom map[string]bool
	model := resp.Model
	if req != nil {
		if model == "" {
			model = req.Model
		}
		for _, t := range req.Tools {
			if t.Kind == ir.ToolCustom {
				if custom == nil {
					custom = make(map[string]bool)
				}
				custom[t.Name] = true
			}
		}
	}
	id := resp.ID
	if id == "" {
		id = rand.Text()[:22]
	}
	dst := make([]byte, 0, 256)
	dst = append(dst, chunkPrefix[len("data: "):]...)
	n := 0
	lastText := false
	for i := range resp.Content {
		p := &resp.Content[i]
		mark := len(dst)
		if n > 0 {
			dst = append(dst, ',')
		}
		switch p.Type {
		case ir.PartText:
			if p.Text == "" {
				dst = dst[:mark]
				continue
			}
			dst = appendTextPart(dst, p.Text, false, "")
			lastText = true
		case ir.PartThinking:
			sig := ""
			if p.Signature != "" {
				sig = tunnelSig(p.SigSource, p.Signature, false)
			}
			if p.Text == "" && sig == "" {
				dst = dst[:mark]
				continue
			}
			if p.Text == "" && lastText && p.SigSource == ir.SigGemini {
				dst = appendTextPart(dst, "", false, sig)
			} else {
				dst = appendTextPart(dst, p.Text, true, sig)
			}
			lastText = false
		case ir.PartRedactedThinking:
			dst = appendTextPart(dst, "", true, tunnelSig(p.SigSource, p.Signature, true))
			lastText = false
		case ir.PartToolCall:
			dst = appendCallPart(dst, p.ToolID, p.ToolName, p.Args, p.ToolKind == ir.ToolCustom || custom[p.ToolName])
			lastText = false
		default:
			dst = dst[:mark]
			continue
		}
		n++
	}
	if n == 0 {
		dst = append(dst, `{"text":""}`...)
	}
	dst = append(dst, `]},"finishReason":`...)
	dst = appendString(dst, geminiFinish(resp.Finish))
	dst = append(dst, `,"index":0}],"usageMetadata":`...)
	dst = appendUsage(dst, &resp.Usage)
	dst = append(dst, `,"modelVersion":`...)
	dst = appendString(dst, model)
	dst = append(dst, `,"responseId":`...)
	dst = appendString(dst, id)
	return append(dst, '}'), nil
}
