// Package gemini translates between the IR and the Google Gemini
// generateContent wire format (v1beta, streamGenerateContent?alt=sse), both as
// an upstream format (AI Studio / Vertex) and as a client format.
package gemini

import (
	"encoding/json"
	"strings"

	"github.com/Project-by-Myself/clirouter/internal/ir"
)

type gRequest struct {
	Contents          []gContent      `json:"contents"`
	SystemInstruction *gContent       `json:"systemInstruction,omitempty"`
	Tools             []gTool         `json:"tools,omitempty"`
	ToolConfig        *gToolConfig    `json:"toolConfig,omitempty"`
	GenerationConfig  *gGenConfig     `json:"generationConfig,omitempty"`
	SafetySettings    json.RawMessage `json:"safetySettings,omitempty"`
}

type gContent struct {
	Role  string  `json:"role,omitempty"`
	Parts []gPart `json:"parts"`
}

type gPart struct {
	Text                *string            `json:"text,omitempty"`
	Thought             bool               `json:"thought,omitempty"`
	ThoughtSignature    string             `json:"thoughtSignature,omitempty"`
	InlineData          *gBlob             `json:"inlineData,omitempty"`
	FileData            *gFileData         `json:"fileData,omitempty"`
	FunctionCall        *gFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse    *gFunctionResponse `json:"functionResponse,omitempty"`
	ExecutableCode      *gExecutableCode   `json:"executableCode,omitempty"`
	CodeExecutionResult *gCodeResult       `json:"codeExecutionResult,omitempty"`
}

type gBlob struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

type gFileData struct {
	MimeType string `json:"mimeType,omitempty"`
	FileURI  string `json:"fileUri"`
}

type gFunctionCall struct {
	ID   string          `json:"id,omitempty"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args,omitempty"`
}

type gFunctionResponse struct {
	ID       string          `json:"id,omitempty"`
	Name     string          `json:"name"`
	Response json.RawMessage `json:"response"`
}

type gExecutableCode struct {
	Language string `json:"language,omitempty"`
	Code     string `json:"code"`
}

type gCodeResult struct {
	Outcome string `json:"outcome,omitempty"`
	Output  string `json:"output,omitempty"`
}

type gTool struct {
	FunctionDeclarations []gFuncDecl `json:"functionDeclarations,omitempty"`
}

type gFuncDecl struct {
	Name                 string          `json:"name"`
	Description          string          `json:"description,omitempty"`
	Parameters           json.RawMessage `json:"parameters,omitempty"`
	ParametersJSONSchema json.RawMessage `json:"parametersJsonSchema,omitempty"`
}

type gToolConfig struct {
	FunctionCallingConfig *gFuncCallingConfig `json:"functionCallingConfig,omitempty"`
}

type gFuncCallingConfig struct {
	Mode                 string   `json:"mode,omitempty"`
	AllowedFunctionNames []string `json:"allowedFunctionNames,omitempty"`
}

type gGenConfig struct {
	MaxOutputTokens    int              `json:"maxOutputTokens,omitempty"`
	Temperature        *float64         `json:"temperature,omitempty"`
	TopP               *float64         `json:"topP,omitempty"`
	TopK               *float64         `json:"topK,omitempty"`
	StopSequences      []string         `json:"stopSequences,omitempty"`
	ResponseMimeType   string           `json:"responseMimeType,omitempty"`
	ResponseJSONSchema json.RawMessage  `json:"responseJsonSchema,omitempty"`
	ResponseSchema     json.RawMessage  `json:"responseSchema,omitempty"`
	ThinkingConfig     *gThinkingConfig `json:"thinkingConfig,omitempty"`
}

type gThinkingConfig struct {
	ThinkingBudget  *int   `json:"thinkingBudget,omitempty"`
	ThinkingLevel   string `json:"thinkingLevel,omitempty"`
	IncludeThoughts bool   `json:"includeThoughts,omitempty"`
}

type gResponse struct {
	Candidates     []gCandidate     `json:"candidates"`
	UsageMetadata  *gUsage          `json:"usageMetadata"`
	ModelVersion   string           `json:"modelVersion"`
	ResponseID     string           `json:"responseId"`
	PromptFeedback *gPromptFeedback `json:"promptFeedback"`
	Error          *gError          `json:"error"`
	// Code Assist (cloudcode-pa) wraps the response in {"response": {...}}.
	Response *gResponse `json:"response"`
}

func (r *gResponse) unwrap() *gResponse {
	if r.Response != nil && r.Candidates == nil && r.Error == nil && r.UsageMetadata == nil {
		return r.Response
	}
	return r
}

type gCandidate struct {
	Content      *gContent `json:"content"`
	FinishReason string    `json:"finishReason"`
}

type gUsage struct {
	PromptTokenCount        int64 `json:"promptTokenCount"`
	CandidatesTokenCount    int64 `json:"candidatesTokenCount"`
	ThoughtsTokenCount      int64 `json:"thoughtsTokenCount"`
	CachedContentTokenCount int64 `json:"cachedContentTokenCount"`
	TotalTokenCount         int64 `json:"totalTokenCount"`
}

type gPromptFeedback struct {
	BlockReason        string `json:"blockReason"`
	BlockReasonMessage string `json:"blockReasonMessage"`
}

type gError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Status  string `json:"status"`
}

func strp(s string) *string { return &s }

var extMime = map[string]string{
	"png": "image/png", "jpg": "image/jpeg", "jpeg": "image/jpeg", "gif": "image/gif",
	"webp": "image/webp", "heic": "image/heic", "heif": "image/heif", "bmp": "image/bmp",
	"pdf": "application/pdf", "txt": "text/plain", "md": "text/markdown", "html": "text/html",
	"htm": "text/html", "csv": "text/csv", "json": "application/json", "xml": "text/xml",
	"js": "text/javascript", "py": "text/x-python", "css": "text/css",
	"mp3": "audio/mpeg", "wav": "audio/wav", "flac": "audio/flac", "ogg": "audio/ogg",
	"aac": "audio/aac", "m4a": "audio/mp4", "mp4": "video/mp4", "mov": "video/quicktime",
	"webm": "video/webm", "mpeg": "video/mpeg", "avi": "video/x-msvideo",
}

// guessMime infers a MIME type from a URL's file extension.
func guessMime(u string) string {
	if i := strings.IndexAny(u, "?#"); i >= 0 {
		u = u[:i]
	}
	if i := strings.LastIndexByte(u, '/'); i >= 0 {
		u = u[i+1:]
	}
	if i := strings.LastIndexByte(u, '.'); i >= 0 {
		if m, ok := extMime[strings.ToLower(u[i+1:])]; ok {
			return m
		}
	}
	return "application/octet-stream"
}

// Non-Gemini reasoning signatures are tunnelled through a Gemini client's
// thoughtSignature field with a prefix so they survive the round trip.
const sigTunnelPrefix = "x-"

func tunnelSig(src, sig string, redacted bool) string {
	if src == ir.SigGemini && !redacted {
		return sig
	}
	s := sigTunnelPrefix + src
	if redacted {
		s += "-redacted"
	}
	return s + "|" + sig
}

func untunnelSig(s string) (src, sig string, redacted bool) {
	if strings.HasPrefix(s, sigTunnelPrefix) {
		if i := strings.IndexByte(s, '|'); i > 0 {
			src = s[len(sigTunnelPrefix):i]
			if r, ok := strings.CutSuffix(src, "-redacted"); ok {
				return r, s[i+1:], true
			}
			return src, s[i+1:], false
		}
	}
	return ir.SigGemini, s, false
}
