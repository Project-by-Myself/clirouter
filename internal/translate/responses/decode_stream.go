package responses

import (
	"encoding/json"
	"fmt"

	"github.com/Project-by-Myself/clirouter/internal/ir"
)

type upItem struct {
	Type             string `json:"type"`
	ID               string `json:"id"`
	CallID           string `json:"call_id"`
	Name             string `json:"name"`
	Arguments        string `json:"arguments"`
	Input            string `json:"input"`
	EncryptedContent string `json:"encrypted_content"`
}

type upEvent struct {
	Type         string          `json:"type"`
	Response     *upResponse     `json:"response"`
	Item         *upItem         `json:"item"`
	OutputIndex  int             `json:"output_index"`
	ItemID       string          `json:"item_id"`
	Delta        json.RawMessage `json:"delta"`
	SummaryIndex int             `json:"summary_index"`
	Code         string          `json:"code"`
	Message      string          `json:"message"`
	Error        *upError        `json:"error"`
}

func (ev *upEvent) errorInfo(t string) (errType, msg string) {
	switch {
	case t == "response.failed" && ev.Response != nil && ev.Response.Error != nil:
		errType, msg = ev.Response.Error.kind(), ev.Response.Error.Message
	case ev.Error != nil:
		errType, msg = ev.Error.kind(), ev.Error.Message
	default:
		errType, msg = ev.Code, ev.Message
	}
	if errType == "" {
		errType = "server_error"
	}
	if msg == "" {
		msg = "upstream response failed"
	}
	return errType, msg
}

type decTool struct {
	callID  string
	sawArgs bool
	ended   bool
}

type streamDecoder struct {
	started bool
	sawTool bool
	byItem  map[string]*decTool
	byIndex map[int]*decTool
}

func NewStreamDecoder() ir.StreamDecoder {
	return &streamDecoder{byItem: map[string]*decTool{}, byIndex: map[int]*decTool{}}
}

func (d *streamDecoder) tool(ev *upEvent) *decTool {
	if ev.ItemID != "" {
		if t := d.byItem[ev.ItemID]; t != nil {
			return t
		}
	}
	return d.byIndex[ev.OutputIndex]
}

func (d *streamDecoder) Decode(name, data []byte, emit func(ir.Event)) error {
	t := eventType(name, data)
	switch t {
	case "response.created", "response.in_progress",
		"response.output_item.added", "response.output_item.done",
		"response.function_call_arguments.delta", "response.custom_tool_call_input.delta",
		"response.output_text.delta", "response.refusal.delta",
		"response.reasoning_summary_text.delta", "response.reasoning_text.delta",
		"response.reasoning_summary_part.added",
		"response.completed", "response.incomplete", "response.failed", "error":
	default:
		return nil
	}
	var ev upEvent
	if err := json.Unmarshal(data, &ev); err != nil {
		return fmt.Errorf("responses: decode %s event: %w", t, err)
	}
	switch t {
	case "response.created", "response.in_progress":
		if !d.started && ev.Response != nil {
			d.started = true
			emit(ir.Event{Kind: ir.EvStart, ID: ev.Response.ID, Model: ev.Response.Model})
		}
	case "response.output_item.added":
		if it := ev.Item; it != nil && (it.Type == "function_call" || it.Type == "custom_tool_call") {
			d.startTool(&ev, emit)
		}
	case "response.function_call_arguments.delta", "response.custom_tool_call_input.delta":
		if tl := d.tool(&ev); tl != nil {
			if s := rawString(ev.Delta); s != "" {
				tl.sawArgs = true
				emit(ir.Event{Kind: ir.EvToolArgs, ID: tl.callID, Text: s})
			}
		}
	case "response.output_item.done":
		it := ev.Item
		if it == nil {
			return nil
		}
		switch it.Type {
		case "function_call", "custom_tool_call":
			tl := d.tool(&ev)
			if tl == nil || tl.ended {
				tl = d.startTool(&ev, emit)
			}
			if !tl.sawArgs {
				args := it.Arguments
				if it.Type == "custom_tool_call" {
					args = it.Input
				}
				if args != "" {
					emit(ir.Event{Kind: ir.EvToolArgs, ID: tl.callID, Text: args})
				}
			}
			tl.ended = true
			emit(ir.Event{Kind: ir.EvToolEnd, ID: tl.callID})
		case "reasoning":
			if it.EncryptedContent != "" {
				emit(ir.Event{Kind: ir.EvThinkingSignature, Signature: it.EncryptedContent, SigSource: ir.SigOpenAI})
			}
		}
	case "response.output_text.delta", "response.refusal.delta":
		if s := rawString(ev.Delta); s != "" {
			emit(ir.Event{Kind: ir.EvText, Text: s})
		}
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		if s := rawString(ev.Delta); s != "" {
			emit(ir.Event{Kind: ir.EvThinking, Text: s})
		}
	case "response.reasoning_summary_part.added":
		if ev.SummaryIndex > 0 {
			emit(ir.Event{Kind: ir.EvThinking, Text: "\n\n"})
		}
	case "response.completed", "response.incomplete":
		r := ev.Response
		if r == nil {
			r = &upResponse{}
		}
		if !d.started {
			d.started = true
			emit(ir.Event{Kind: ir.EvStart, ID: r.ID, Model: r.Model})
		}
		if r.Usage != nil {
			emit(ir.Event{Kind: ir.EvUsage, Usage: r.Usage.ir()})
		}
		finish := ir.FinishStop
		if d.sawTool {
			finish = ir.FinishToolCalls
		}
		if t == "response.incomplete" || r.Status == "incomplete" {
			reason := ""
			if r.IncompleteDetails != nil {
				reason = r.IncompleteDetails.Reason
			}
			switch reason {
			case "max_output_tokens":
				finish = ir.FinishLength
			case "content_filter":
				finish = ir.FinishContentFilter
			}
		}
		emit(ir.Event{Kind: ir.EvFinish, Finish: finish})
		emit(ir.Event{Kind: ir.EvDone})
	case "response.failed", "error":
		errType, msg := ev.errorInfo(t)
		emit(ir.Event{Kind: ir.EvError, Text: msg, ErrType: errType})
	}
	return nil
}

func (d *streamDecoder) startTool(ev *upEvent, emit func(ir.Event)) *decTool {
	it := ev.Item
	kind := ir.ToolFunction
	if it.Type == "custom_tool_call" {
		kind = ir.ToolCustom
	}
	callID := it.CallID
	if callID == "" {
		callID = it.ID
	}
	tl := &decTool{callID: callID}
	if it.ID != "" {
		d.byItem[it.ID] = tl
	}
	d.byIndex[ev.OutputIndex] = tl
	d.sawTool = true
	emit(ir.Event{Kind: ir.EvToolStart, ID: callID, Name: it.Name, ToolKind: kind})
	return tl
}
