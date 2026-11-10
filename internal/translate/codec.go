// Package translate binds every wire format's codec behind one table.
package translate

import (
	"github.com/Project-by-Myself/clirouter/internal/ir"
	"github.com/Project-by-Myself/clirouter/internal/translate/anthropic"
	"github.com/Project-by-Myself/clirouter/internal/translate/gemini"
	"github.com/Project-by-Myself/clirouter/internal/translate/openai"
	"github.com/Project-by-Myself/clirouter/internal/translate/responses"
)

type Codec struct {
	DecodeRequest    func([]byte) (*ir.Request, error)
	EncodeRequest    func(*ir.Request, ir.EncodeOptions) ([]byte, error)
	NewStreamDecoder func() ir.StreamDecoder
	NewStreamEncoder func(*ir.Request, string) ir.StreamEncoder
	EncodeResponse   func(*ir.Request, *ir.Response) ([]byte, error)
	Ping             func() []byte
	ErrorBody        func(status int, errType, msg string) []byte
	StreamErrorEvent func(errType, msg string) []byte
	UsageFromEvent   func(name, data []byte) (*ir.Usage, bool)
	IsErrorEvent     func(name, data []byte) (string, string, bool)
	IsTerminal       func(name, data []byte) bool
	// UsageMarker is a byte substring present in every event carrying usage,
	// so passthrough only parses those events.
	UsageMarker string
	// Preamble reports events that carry no content yet; the proxy holds
	// them back so it can still fail over if the next event is an error.
	Preamble func(name, data []byte) bool
}

var codecs = map[ir.Format]*Codec{
	ir.FormatAnthropic: {
		DecodeRequest: anthropic.DecodeRequest, EncodeRequest: anthropic.EncodeRequest,
		NewStreamDecoder: anthropic.NewStreamDecoder, NewStreamEncoder: anthropic.NewStreamEncoder,
		EncodeResponse: anthropic.EncodeResponse, Ping: anthropic.Ping, ErrorBody: anthropic.ErrorBody,
		StreamErrorEvent: anthropic.StreamErrorEvent, UsageFromEvent: anthropic.UsageFromEvent,
		IsErrorEvent: anthropic.IsErrorEvent, IsTerminal: anthropic.IsTerminal,
		UsageMarker: `"usage"`,
		Preamble: func(name, _ []byte) bool {
			s := string(name)
			return s == "message_start" || s == "ping"
		},
	},
	ir.FormatChat: {
		DecodeRequest: openai.DecodeRequest, EncodeRequest: openai.EncodeRequest,
		NewStreamDecoder: openai.NewStreamDecoder, NewStreamEncoder: openai.NewStreamEncoder,
		EncodeResponse: openai.EncodeResponse, Ping: openai.Ping, ErrorBody: openai.ErrorBody,
		StreamErrorEvent: openai.StreamErrorEvent, UsageFromEvent: openai.UsageFromEvent,
		IsErrorEvent: openai.IsErrorEvent, IsTerminal: openai.IsTerminal,
		UsageMarker: `"usage"`,
		Preamble:    func(_, _ []byte) bool { return false },
	},
	ir.FormatResponses: {
		DecodeRequest: responses.DecodeRequest, EncodeRequest: responses.EncodeRequest,
		NewStreamDecoder: responses.NewStreamDecoder, NewStreamEncoder: responses.NewStreamEncoder,
		EncodeResponse: responses.EncodeResponse, Ping: responses.Ping, ErrorBody: responses.ErrorBody,
		StreamErrorEvent: responses.StreamErrorEvent, UsageFromEvent: responses.UsageFromEvent,
		IsErrorEvent: responses.IsErrorEvent, IsTerminal: responses.IsTerminal,
		UsageMarker: `"usage"`,
		Preamble: func(name, _ []byte) bool {
			s := string(name)
			return s == "response.created" || s == "response.in_progress"
		},
	},
	ir.FormatGemini: {
		DecodeRequest: gemini.DecodeRequest, EncodeRequest: gemini.EncodeRequest,
		NewStreamDecoder: gemini.NewStreamDecoder, NewStreamEncoder: gemini.NewStreamEncoder,
		EncodeResponse: gemini.EncodeResponse, Ping: gemini.Ping, ErrorBody: gemini.ErrorBody,
		StreamErrorEvent: gemini.StreamErrorEvent, UsageFromEvent: gemini.UsageFromEvent,
		IsErrorEvent: gemini.IsErrorEvent, IsTerminal: gemini.IsTerminal,
		UsageMarker: `"usageMetadata"`,
		Preamble:    func(_, _ []byte) bool { return false },
	},
}

func For(f ir.Format) *Codec { return codecs[f] }
