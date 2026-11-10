// Package ir defines the canonical intermediate representation that every
// wire format is translated through when client and upstream formats differ.
package ir

import "encoding/json"

type Format string

const (
	FormatChat      Format = "openai-chat"
	FormatResponses Format = "openai-responses"
	FormatAnthropic Format = "anthropic"
	FormatGemini    Format = "gemini"
)

type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

type PartType uint8

const (
	PartText PartType = iota
	PartImage
	PartFile
	PartToolCall
	PartToolResult
	PartThinking
	PartRedactedThinking
)

type ToolKind uint8

const (
	ToolFunction ToolKind = iota
	ToolCustom            // OpenAI freeform/custom tool: single string input
)

// Signature sources, so a reasoning signature is only replayed to the
// provider family that issued it.
const (
	SigAnthropic = "anthropic"
	SigOpenAI    = "openai"
	SigGemini    = "gemini"
)

type Part struct {
	Type PartType `json:"type"`
	Text string   `json:"text,omitempty"`

	// Image / file
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"` // base64, no data: prefix
	URL       string `json:"url,omitempty"`

	// Tool call / tool result
	ToolID   string   `json:"tool_id,omitempty"`
	ToolName string   `json:"tool_name,omitempty"`
	ToolKind ToolKind `json:"tool_kind,omitempty"`
	Args     string   `json:"args,omitempty"` // raw JSON for functions, raw text for custom tools
	Result   []Part   `json:"result,omitempty"`
	IsError  bool     `json:"is_error,omitempty"`

	// Thinking
	Signature string `json:"signature,omitempty"`
	SigSource string `json:"sig_source,omitempty"`

	CacheControl json.RawMessage `json:"cache_control,omitempty"`
}

type Message struct {
	Role  Role   `json:"role"`
	Parts []Part `json:"parts"`
}

type Tool struct {
	Name         string          `json:"name"`
	Description  string          `json:"description,omitempty"`
	Schema       json.RawMessage `json:"schema,omitempty"`
	Kind         ToolKind        `json:"kind,omitempty"`
	Format       json.RawMessage `json:"format,omitempty"` // custom tool grammar
	CacheControl json.RawMessage `json:"cache_control,omitempty"`
}

type ToolChoice struct {
	Mode string `json:"mode"` // auto | none | required | tool
	Name string `json:"name,omitempty"`
}

type Reasoning struct {
	Effort       string `json:"effort,omitempty"` // minimal | low | medium | high | xhigh
	BudgetTokens int    `json:"budget_tokens,omitempty"`
	Summary      string `json:"summary,omitempty"`
	Disabled     bool   `json:"disabled,omitempty"`
}

type Request struct {
	Model             string          `json:"model"`
	System            []Part          `json:"system,omitempty"`
	Messages          []Message       `json:"messages"`
	Tools             []Tool          `json:"tools,omitempty"`
	ToolChoice        *ToolChoice     `json:"tool_choice,omitempty"`
	MaxTokens         int             `json:"max_tokens,omitempty"`
	Temperature       *float64        `json:"temperature,omitempty"`
	TopP              *float64        `json:"top_p,omitempty"`
	TopK              *int            `json:"top_k,omitempty"`
	Stop              []string        `json:"stop,omitempty"`
	Stream            bool            `json:"stream"`
	Reasoning         *Reasoning      `json:"reasoning,omitempty"`
	ParallelToolCalls *bool           `json:"parallel_tool_calls,omitempty"`
	ResponseFormat    json.RawMessage `json:"response_format,omitempty"` // OpenAI chat shape
	User              string          `json:"user,omitempty"`
	IncludeUsage      bool            `json:"include_usage,omitempty"`
	PromptCacheKey    string          `json:"prompt_cache_key,omitempty"`
}

// EncodeOptions carries upstream-specific knobs for request encoders.
type EncodeOptions struct {
	Model        string // upstream model id to put on the wire
	ProviderType string // e.g. "openai", "openai-compatible", "anthropic", "gemini", "codex"
}

type FinishReason string

const (
	FinishStop          FinishReason = "stop"
	FinishLength        FinishReason = "length"
	FinishToolCalls     FinishReason = "tool_calls"
	FinishContentFilter FinishReason = "content_filter"
	FinishError         FinishReason = "error"
)

type Usage struct {
	InputTokens      int64 `json:"input_tokens"` // total prompt tokens, cache reads/writes included
	OutputTokens     int64 `json:"output_tokens"`
	CacheReadTokens  int64 `json:"cache_read_tokens"`
	CacheWriteTokens int64 `json:"cache_write_tokens"`
	ReasoningTokens  int64 `json:"reasoning_tokens"`
}

// Merge keeps the largest value seen per field: providers report usage
// either cumulatively or split across start/end events.
func (u *Usage) Merge(o *Usage) {
	if o == nil {
		return
	}
	u.InputTokens = max(u.InputTokens, o.InputTokens)
	u.OutputTokens = max(u.OutputTokens, o.OutputTokens)
	u.CacheReadTokens = max(u.CacheReadTokens, o.CacheReadTokens)
	u.CacheWriteTokens = max(u.CacheWriteTokens, o.CacheWriteTokens)
	u.ReasoningTokens = max(u.ReasoningTokens, o.ReasoningTokens)
}

type EventKind uint8

const (
	EvStart             EventKind = iota // ID, Model
	EvText                               // Text
	EvThinking                           // Text
	EvThinkingSignature                  // Signature, SigSource
	EvRedactedThinking                   // Signature carries the opaque data
	EvToolStart                          // ID, Name, ToolKind
	EvToolArgs                           // ID, Text (argument delta)
	EvToolEnd                            // ID
	EvUsage                              // Usage
	EvFinish                             // Finish
	EvError                              // Text = message, ErrType
	EvDone                               // stream finished; encoders flush terminal frames
)

type Event struct {
	Kind      EventKind
	Text      string
	ID        string
	Name      string
	Model     string
	ToolKind  ToolKind
	Signature string
	SigSource string
	Usage     *Usage
	Finish    FinishReason
	ErrType   string
}

// Response is a fully aggregated (non-streaming) assistant turn.
type Response struct {
	ID      string
	Model   string
	Content []Part
	Finish  FinishReason
	Usage   Usage
}

// StreamDecoder turns one upstream SSE event into zero or more IR events.
type StreamDecoder interface {
	Decode(name, data []byte, emit func(Event)) error
}

// StreamEncoder appends the client wire bytes for an IR event to dst.
type StreamEncoder interface {
	Encode(dst []byte, ev Event) []byte
}
