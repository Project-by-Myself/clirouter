package ir

import "strings"

// Aggregator folds a stream of IR events into a Response, used when a client
// asked for a non-streaming reply but the upstream is always streamed.
type Aggregator struct {
	resp     Response
	text     strings.Builder
	thinking strings.Builder
	sig      string
	sigSrc   string
	inText   bool
	inThink  bool
	ErrMsg   string
	ErrType  string
}

func NewAggregator() *Aggregator {
	return &Aggregator{}
}

func (a *Aggregator) flushText() {
	if a.inText {
		t := a.text.String()
		a.text.Reset()
		a.inText = false
		if t != "" {
			a.resp.Content = append(a.resp.Content, Part{Type: PartText, Text: t})
		}
	}
}

func (a *Aggregator) flushThinking() {
	if a.inThink {
		t := a.thinking.String()
		a.thinking.Reset()
		a.inThink = false
		if t != "" || a.sig != "" {
			a.resp.Content = append(a.resp.Content, Part{Type: PartThinking, Text: t, Signature: a.sig, SigSource: a.sigSrc})
		}
		a.sig, a.sigSrc = "", ""
	}
}

func (a *Aggregator) Add(ev Event) {
	switch ev.Kind {
	case EvStart:
		if ev.ID != "" {
			a.resp.ID = ev.ID
		}
		if ev.Model != "" {
			a.resp.Model = ev.Model
		}
	case EvText:
		a.flushThinking()
		a.inText = true
		a.text.WriteString(ev.Text)
	case EvThinking:
		a.flushText()
		if a.sig != "" {
			a.flushThinking() // a signature closes its block; this is a new one
		}
		a.inThink = true
		a.thinking.WriteString(ev.Text)
	case EvThinkingSignature:
		a.flushText()
		a.inThink = true
		a.sig, a.sigSrc = ev.Signature, ev.SigSource
	case EvRedactedThinking:
		a.flushText()
		a.flushThinking()
		a.resp.Content = append(a.resp.Content, Part{Type: PartRedactedThinking, Signature: ev.Signature, SigSource: ev.SigSource})
	case EvToolStart:
		a.flushText()
		a.flushThinking()
		a.resp.Content = append(a.resp.Content, Part{Type: PartToolCall, ToolID: ev.ID, ToolName: ev.Name, ToolKind: ev.ToolKind})
	case EvToolArgs:
		for i := len(a.resp.Content) - 1; i >= 0; i-- {
			p := &a.resp.Content[i]
			if p.Type == PartToolCall && p.ToolID == ev.ID {
				p.Args += ev.Text
				break
			}
		}
	case EvUsage:
		a.resp.Usage.Merge(ev.Usage)
	case EvFinish:
		a.resp.Finish = ev.Finish
	case EvError:
		a.ErrMsg, a.ErrType = ev.Text, ev.ErrType
	}
}

func (a *Aggregator) Response() *Response {
	a.flushText()
	a.flushThinking()
	if a.resp.Finish == "" {
		a.resp.Finish = FinishStop
		for _, p := range a.resp.Content {
			if p.Type == PartToolCall {
				a.resp.Finish = FinishToolCalls
				break
			}
		}
	}
	return &a.resp
}
