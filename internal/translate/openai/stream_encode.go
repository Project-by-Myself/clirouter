package openai

import (
	"crypto/rand"
	"time"

	"github.com/Project-by-Myself/clirouter/internal/ir"
)

type encTool struct {
	index  int
	custom bool
	ended  bool
	buf    []byte
}

type streamEncoder struct {
	model        string
	includeUsage bool
	id           string

	head    []byte // `{"id":..,"object":..,"created":..,"model":..,`
	started bool
	done    bool

	tools    map[string]*encTool
	lastTool *encTool
	order    []*encTool
	nextIdx  int

	finish  ir.FinishReason
	usage   ir.Usage
	scratch []byte
}

// NewStreamEncoder returns an encoder producing chat.completion.chunk SSE for
// a Chat Completions client. model is the name reported to the client.
func NewStreamEncoder(req *ir.Request, model string) ir.StreamEncoder {
	e := &streamEncoder{model: model}
	if req != nil {
		e.includeUsage = req.IncludeUsage
		if e.model == "" {
			e.model = req.Model
		}
	}
	return e
}

func (e *streamEncoder) ensureStarted(dst []byte) []byte {
	if e.started {
		return dst
	}
	e.started = true
	if e.id == "" {
		e.id = "chatcmpl-" + rand.Text()
	}
	h := make([]byte, 0, 96+len(e.id)+len(e.model))
	h = append(h, `{"id":`...)
	h = appendJSONString(h, e.id)
	h = append(h, `,"object":"chat.completion.chunk","created":`...)
	h = appendInt(h, time.Now().Unix())
	h = append(h, `,"model":`...)
	h = appendJSONString(h, e.model)
	e.head = append(h, ',')
	dst = e.open(dst)
	dst = append(dst, `{"role":"assistant","content":""}`...)
	return e.close(dst, "")
}

func (e *streamEncoder) open(dst []byte) []byte {
	dst = append(dst, "data: "...)
	dst = append(dst, e.head...)
	return append(dst, `"choices":[{"index":0,"delta":`...)
}

func (e *streamEncoder) close(dst []byte, finish string) []byte {
	if finish == "" {
		return append(dst, ",\"finish_reason\":null}]}\n\n"...)
	}
	dst = append(dst, `,"finish_reason":"`...)
	dst = append(dst, finish...)
	return append(dst, "\"}]}\n\n"...)
}

func (e *streamEncoder) argsChunk(dst []byte, t *encTool, args string) []byte {
	dst = e.open(dst)
	dst = append(dst, `{"tool_calls":[{"index":`...)
	dst = appendInt(dst, int64(t.index))
	dst = append(dst, `,"function":{"arguments":`...)
	dst = appendJSONString(dst, args)
	dst = append(dst, "}}]}"...)
	return e.close(dst, "")
}

func (e *streamEncoder) endTool(dst []byte, t *encTool) []byte {
	if t.ended {
		return dst
	}
	t.ended = true
	if t.custom {
		e.scratch = append(e.scratch[:0], `{"input":`...)
		e.scratch = appendJSONString(e.scratch, string(t.buf))
		e.scratch = append(e.scratch, '}')
		dst = e.argsChunk(dst, t, string(e.scratch))
		t.buf = nil
	}
	return dst
}

func (e *streamEncoder) Encode(dst []byte, ev ir.Event) []byte {
	if e.done {
		return dst
	}
	switch ev.Kind {
	case ir.EvStart:
		if !e.started {
			if ev.ID != "" {
				e.id = ev.ID
			}
			if e.model == "" {
				e.model = ev.Model
			}
		}
		dst = e.ensureStarted(dst)
	case ir.EvText, ir.EvThinking:
		if ev.Text == "" {
			return dst
		}
		dst = e.ensureStarted(dst)
		dst = e.open(dst)
		if ev.Kind == ir.EvText {
			dst = append(dst, `{"content":`...)
		} else {
			dst = append(dst, `{"reasoning_content":`...)
		}
		dst = appendJSONString(dst, ev.Text)
		dst = append(dst, '}')
		dst = e.close(dst, "")
	case ir.EvToolStart:
		dst = e.ensureStarted(dst)
		if e.tools == nil {
			e.tools = make(map[string]*encTool, 2)
		}
		t := &encTool{index: e.nextIdx, custom: ev.ToolKind == ir.ToolCustom}
		e.nextIdx++
		e.tools[ev.ID] = t
		e.lastTool = t
		e.order = append(e.order, t)
		id := ev.ID
		if id == "" {
			id = newCallID()
		}
		dst = e.open(dst)
		dst = append(dst, `{"tool_calls":[{"index":`...)
		dst = appendInt(dst, int64(t.index))
		dst = append(dst, `,"id":`...)
		dst = appendJSONString(dst, id)
		dst = append(dst, `,"type":"function","function":{"name":`...)
		dst = appendJSONString(dst, ev.Name)
		dst = append(dst, `,"arguments":""}}]}`...)
		dst = e.close(dst, "")
	case ir.EvToolArgs:
		t := e.tools[ev.ID]
		if t == nil {
			t = e.lastTool
		}
		if t == nil || t.ended || ev.Text == "" {
			return dst
		}
		if t.custom {
			t.buf = append(t.buf, ev.Text...)
			return dst
		}
		dst = e.argsChunk(dst, t, ev.Text)
	case ir.EvToolEnd:
		t := e.tools[ev.ID]
		if t == nil {
			t = e.lastTool
		}
		if t != nil {
			dst = e.endTool(dst, t)
		}
	case ir.EvUsage:
		e.usage.Merge(ev.Usage)
	case ir.EvFinish:
		e.finish = ev.Finish
	case ir.EvError:
		e.done = true
		dst = appendStreamError(dst, ev.ErrType, ev.Text)
	case ir.EvDone:
		dst = e.ensureStarted(dst)
		for _, t := range e.order {
			dst = e.endTool(dst, t)
		}
		dst = e.open(dst)
		dst = append(dst, "{}"...)
		dst = e.close(dst, e.finishString())
		if e.includeUsage {
			dst = append(dst, "data: "...)
			dst = append(dst, e.head...)
			dst = append(dst, `"choices":[],"usage":`...)
			dst = appendUsage(dst, &e.usage)
			dst = append(dst, "}\n\n"...)
		}
		dst = append(dst, "data: [DONE]\n\n"...)
		e.done = true
	}
	return dst
}

func (e *streamEncoder) finishString() string {
	switch e.finish {
	case ir.FinishLength:
		return "length"
	case ir.FinishToolCalls:
		return "tool_calls"
	case ir.FinishContentFilter:
		return "content_filter"
	}
	if len(e.order) > 0 {
		return "tool_calls"
	}
	return "stop"
}

func appendUsage(dst []byte, u *ir.Usage) []byte {
	dst = append(dst, `{"prompt_tokens":`...)
	dst = appendInt(dst, u.InputTokens)
	dst = append(dst, `,"completion_tokens":`...)
	dst = appendInt(dst, u.OutputTokens)
	dst = append(dst, `,"total_tokens":`...)
	dst = appendInt(dst, u.InputTokens+u.OutputTokens)
	dst = append(dst, `,"prompt_tokens_details":{"cached_tokens":`...)
	dst = appendInt(dst, u.CacheReadTokens)
	dst = append(dst, `},"completion_tokens_details":{"reasoning_tokens":`...)
	dst = appendInt(dst, u.ReasoningTokens)
	return append(dst, "}}"...)
}
