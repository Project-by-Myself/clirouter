package ir

import "encoding/json"

// UnwrapCustomTools adapts an upstream decoder for requests that declared
// custom (freeform) tools. Upstreams without custom tools receive them as
// functions taking {"input": string}; this restores the raw input text and
// marks the call as custom before client encoders see it.
func UnwrapCustomTools(dec StreamDecoder, req *Request) StreamDecoder {
	if req == nil {
		return dec
	}
	custom := map[string]bool{}
	for _, t := range req.Tools {
		if t.Kind == ToolCustom {
			custom[t.Name] = true
		}
	}
	if len(custom) == 0 {
		return dec
	}
	return &customUnwrapper{dec: dec, custom: custom, open: map[string][]byte{}}
}

type customUnwrapper struct {
	dec    StreamDecoder
	custom map[string]bool
	open   map[string][]byte // tool id -> buffered JSON args
}

func (u *customUnwrapper) Decode(name, data []byte, emit func(Event)) error {
	return u.dec.Decode(name, data, func(ev Event) {
		switch ev.Kind {
		case EvToolStart:
			if ev.ToolKind != ToolCustom && u.custom[ev.Name] {
				u.open[ev.ID] = []byte{}
				ev.ToolKind = ToolCustom
			}
		case EvToolArgs:
			if buf, ok := u.open[ev.ID]; ok {
				u.open[ev.ID] = append(buf, ev.Text...)
				return
			}
		case EvToolEnd:
			if buf, ok := u.open[ev.ID]; ok {
				delete(u.open, ev.ID)
				var w struct {
					Input *string `json:"input"`
				}
				text := string(buf)
				if json.Unmarshal(buf, &w) == nil && w.Input != nil {
					text = *w.Input
				}
				if text != "" {
					emit(Event{Kind: EvToolArgs, ID: ev.ID, Text: text})
				}
			}
		}
		emit(ev)
	})
}
