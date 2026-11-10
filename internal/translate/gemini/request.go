package gemini

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/Project-by-Myself/clirouter/internal/ir"
)

var customToolSchema = json.RawMessage(`{"type":"object","properties":{"input":{"type":"string"}},"required":["input"]}`)

var effortBudget = map[string]int{"minimal": 512, "low": 2048, "medium": 8192, "high": 24576, "xhigh": 32768}

// EncodeRequest builds the body for streamGenerateContent / generateContent.
// The model is carried in the URL, not the body.
func EncodeRequest(req *ir.Request, opts ir.EncodeOptions) ([]byte, error) {
	if req == nil {
		return nil, errors.New("gemini: nil request")
	}
	model := opts.Model
	if model == "" {
		model = req.Model
	}
	var custom map[string]bool
	for _, t := range req.Tools {
		if t.Kind == ir.ToolCustom {
			if custom == nil {
				custom = make(map[string]bool)
			}
			custom[t.Name] = true
		}
	}

	out := gRequest{Contents: make([]gContent, 0, len(req.Messages))}
	if len(req.System) > 0 {
		var parts []gPart
		for _, p := range req.System {
			if p.Type == ir.PartText && p.Text != "" {
				parts = append(parts, gPart{Text: strp(p.Text)})
			}
		}
		if len(parts) > 0 {
			out.SystemInstruction = &gContent{Parts: parts}
		}
	}

	callNames := make(map[string]string)
	for _, m := range req.Messages {
		role := "user"
		if m.Role == ir.RoleAssistant {
			role = "model"
		}
		parts := make([]gPart, 0, len(m.Parts))
		for i := range m.Parts {
			p := &m.Parts[i]
			switch p.Type {
			case ir.PartText:
				if p.Text != "" {
					parts = append(parts, gPart{Text: strp(p.Text)})
				}
			case ir.PartImage, ir.PartFile:
				if mp, ok := mediaPart(p); ok {
					parts = append(parts, mp)
				}
			case ir.PartThinking:
				if role != "model" || p.SigSource != ir.SigGemini || p.Signature == "" {
					continue
				}
				if p.Text == "" && len(parts) > 0 {
					last := &parts[len(parts)-1]
					if last.Text != nil && !last.Thought && last.ThoughtSignature == "" {
						last.ThoughtSignature = p.Signature
						continue
					}
				}
				parts = append(parts, gPart{Text: strp(p.Text), Thought: true, ThoughtSignature: p.Signature})
			case ir.PartToolCall:
				if role != "model" {
					continue
				}
				callNames[p.ToolID] = p.ToolName
				sig, ok := LookupSignature(p.ToolID)
				if !ok {
					sig = DummySignature
				}
				var args json.RawMessage
				if p.ToolKind == ir.ToolCustom || custom[p.ToolName] {
					args = appendString(append(make([]byte, 0, len(p.Args)+16), `{"input":`...), p.Args)
					args = append(args, '}')
				} else {
					args = objectArgs(p.Args)
				}
				parts = append(parts, gPart{FunctionCall: &gFunctionCall{Name: p.ToolName, Args: args}, ThoughtSignature: sig})
			case ir.PartToolResult:
				role = "user"
				name := callNames[p.ToolID]
				if name == "" {
					name = p.ToolName
				}
				if name == "" {
					name = p.ToolID
				}
				text, media := resultContent(p)
				key := `{"result":`
				if p.IsError {
					key = `{"error":`
				}
				resp := appendString(append(make([]byte, 0, len(text)+16), key...), text)
				resp = append(resp, '}')
				parts = append(parts, gPart{FunctionResponse: &gFunctionResponse{Name: name, Response: resp}})
				parts = append(parts, media...)
			}
		}
		if len(parts) == 0 {
			continue
		}
		if n := len(out.Contents); n > 0 && out.Contents[n-1].Role == role {
			out.Contents[n-1].Parts = append(out.Contents[n-1].Parts, parts...)
		} else {
			out.Contents = append(out.Contents, gContent{Role: role, Parts: parts})
		}
	}

	if len(req.Tools) > 0 {
		decls := make([]gFuncDecl, 0, len(req.Tools))
		for _, t := range req.Tools {
			d := gFuncDecl{Name: t.Name, Description: t.Description}
			if t.Kind == ir.ToolCustom {
				d.ParametersJSONSchema = customToolSchema
			} else if len(bytes.TrimSpace(t.Schema)) > 0 && string(bytes.TrimSpace(t.Schema)) != "null" {
				d.ParametersJSONSchema = t.Schema
			}
			decls = append(decls, d)
		}
		out.Tools = []gTool{{FunctionDeclarations: decls}}
	}
	if tc := req.ToolChoice; tc != nil && len(req.Tools) > 0 {
		fc := &gFuncCallingConfig{}
		switch tc.Mode {
		case "none":
			fc.Mode = "NONE"
		case "required":
			fc.Mode = "ANY"
		case "tool":
			fc.Mode = "ANY"
			if tc.Name != "" {
				fc.AllowedFunctionNames = []string{tc.Name}
			}
		default:
			fc.Mode = "AUTO"
		}
		out.ToolConfig = &gToolConfig{FunctionCallingConfig: fc}
	}

	gc := gGenConfig{
		MaxOutputTokens: req.MaxTokens,
		Temperature:     req.Temperature,
		TopP:            req.TopP,
		StopSequences:   req.Stop,
	}
	if req.TopK != nil {
		k := float64(*req.TopK)
		gc.TopK = &k
	}
	if len(req.ResponseFormat) > 0 {
		var rf struct {
			Type       string `json:"type"`
			JSONSchema struct {
				Schema json.RawMessage `json:"schema"`
			} `json:"json_schema"`
		}
		if json.Unmarshal(req.ResponseFormat, &rf) == nil {
			switch rf.Type {
			case "json_schema":
				gc.ResponseMimeType = "application/json"
				if len(rf.JSONSchema.Schema) > 0 {
					gc.ResponseJSONSchema = rf.JSONSchema.Schema
				}
			case "json_object":
				gc.ResponseMimeType = "application/json"
			}
		}
	}
	gc.ThinkingConfig = thinkingConfig(model, req.Reasoning)
	if gc.MaxOutputTokens != 0 || gc.Temperature != nil || gc.TopP != nil || gc.TopK != nil || len(gc.StopSequences) > 0 ||
		gc.ResponseMimeType != "" || gc.ThinkingConfig != nil {
		out.GenerationConfig = &gc
	}
	return marshalNoEscape(&out)
}

func marshalNoEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	b := buf.Bytes()
	return b[:len(b)-1], nil
}

func thinkingConfig(model string, r *ir.Reasoning) *gThinkingConfig {
	lm := strings.ToLower(model)
	if strings.Contains(lm, "gemini-3") {
		if r == nil {
			return &gThinkingConfig{IncludeThoughts: true}
		}
		if r.Disabled {
			return &gThinkingConfig{ThinkingLevel: "low"}
		}
		level := "high"
		switch r.Effort {
		case "minimal", "low":
			level = "low"
		case "":
			if r.BudgetTokens > 0 && r.BudgetTokens <= 2048 {
				level = "low"
			}
		}
		return &gThinkingConfig{ThinkingLevel: level, IncludeThoughts: true}
	}
	if r == nil {
		if strings.Contains(lm, "gemini-1") || strings.Contains(lm, "gemini-2.0") || strings.Contains(lm, "gemma") {
			return nil
		}
		return &gThinkingConfig{IncludeThoughts: true}
	}
	if r.Disabled {
		zero := 0
		return &gThinkingConfig{ThinkingBudget: &zero}
	}
	tc := &gThinkingConfig{IncludeThoughts: true}
	b := r.BudgetTokens
	if b <= 0 {
		b = effortBudget[r.Effort]
	}
	if b > 0 {
		tc.ThinkingBudget = &b
	}
	return tc
}

// objectArgs returns args when it is a JSON object, else {}.
func objectArgs(s string) json.RawMessage {
	t := strings.TrimSpace(s)
	if len(t) < 2 || t[0] != '{' || !json.Valid([]byte(t)) {
		return json.RawMessage("{}")
	}
	return json.RawMessage(t)
}

func mediaPart(p *ir.Part) (gPart, bool) {
	if p.Data != "" {
		mt := p.MediaType
		if mt == "" {
			if p.Type == ir.PartImage {
				mt = "image/png"
			} else {
				mt = "application/octet-stream"
			}
		}
		return gPart{InlineData: &gBlob{MimeType: mt, Data: p.Data}}, true
	}
	if p.URL == "" {
		return gPart{}, false
	}
	if rest, ok := strings.CutPrefix(p.URL, "data:"); ok {
		if i := strings.Index(rest, ";base64,"); i >= 0 {
			mt := rest[:i]
			if mt == "" {
				mt = "application/octet-stream"
			}
			return gPart{InlineData: &gBlob{MimeType: mt, Data: rest[i+8:]}}, true
		}
		return gPart{}, false
	}
	mt := p.MediaType
	if mt == "" {
		mt = guessMime(p.URL)
	}
	return gPart{FileData: &gFileData{MimeType: mt, FileURI: p.URL}}, true
}

func resultContent(p *ir.Part) (string, []gPart) {
	if len(p.Result) == 0 {
		return p.Text, nil
	}
	var sb strings.Builder
	var media []gPart
	for i := range p.Result {
		r := &p.Result[i]
		switch r.Type {
		case ir.PartText:
			if sb.Len() > 0 && r.Text != "" {
				sb.WriteByte('\n')
			}
			sb.WriteString(r.Text)
		case ir.PartImage, ir.PartFile:
			if mp, ok := mediaPart(r); ok {
				media = append(media, mp)
			}
		}
	}
	return sb.String(), media
}

// DecodeRequest parses a Gemini client request. The model comes from the URL,
// so Model is left empty; Stream is decided by the endpoint as well.
func DecodeRequest(body []byte) (*ir.Request, error) {
	var gr gRequest
	if err := json.Unmarshal(body, &gr); err != nil {
		return nil, err
	}
	req := &ir.Request{Messages: make([]ir.Message, 0, len(gr.Contents))}
	if gr.SystemInstruction != nil {
		for _, p := range gr.SystemInstruction.Parts {
			if p.Text != nil && *p.Text != "" {
				req.System = append(req.System, ir.Part{Type: ir.PartText, Text: *p.Text})
			}
		}
	}

	type call struct {
		id, name string
		matched  bool
	}
	var calls []call
	callIdx := 0
	for ci := range gr.Contents {
		c := &gr.Contents[ci]
		if c.Role == "model" {
			parts := make([]ir.Part, 0, len(c.Parts))
			for i := range c.Parts {
				p := &c.Parts[i]
				switch {
				case p.FunctionCall != nil:
					fc := p.FunctionCall
					id := fc.ID
					if id == "" {
						id = "call_" + fc.Name + "_" + strconv.Itoa(callIdx)
					}
					callIdx++
					calls = append(calls, call{id: id, name: fc.Name})
					if p.ThoughtSignature != "" {
						if src, sig, _ := untunnelSig(p.ThoughtSignature); src == ir.SigGemini {
							RememberSignature(id, sig)
						}
					}
					args := "{}"
					if a := bytes.TrimSpace(fc.Args); len(a) > 0 && string(a) != "null" {
						args = string(a)
					}
					parts = append(parts, ir.Part{Type: ir.PartToolCall, ToolID: id, ToolName: fc.Name, Args: args})
				case p.Text != nil && p.Thought:
					src, sig, redacted := "", "", false
					if p.ThoughtSignature != "" {
						src, sig, redacted = untunnelSig(p.ThoughtSignature)
					}
					if redacted {
						parts = append(parts, ir.Part{Type: ir.PartRedactedThinking, Signature: sig, SigSource: src})
						continue
					}
					if n := len(parts); n > 0 && parts[n-1].Type == ir.PartThinking && parts[n-1].Signature == "" {
						parts[n-1].Text += *p.Text
						parts[n-1].Signature, parts[n-1].SigSource = sig, src
						continue
					}
					parts = append(parts, ir.Part{Type: ir.PartThinking, Text: *p.Text, Signature: sig, SigSource: src})
				case p.Text != nil:
					if *p.Text != "" {
						if n := len(parts); n > 0 && parts[n-1].Type == ir.PartText {
							parts[n-1].Text += *p.Text
						} else {
							parts = append(parts, ir.Part{Type: ir.PartText, Text: *p.Text})
						}
					}
					if p.ThoughtSignature != "" {
						src, sig, redacted := untunnelSig(p.ThoughtSignature)
						typ := ir.PartThinking
						if redacted {
							typ = ir.PartRedactedThinking
						}
						parts = append(parts, ir.Part{Type: typ, Signature: sig, SigSource: src})
					}
				case p.ExecutableCode != nil || p.CodeExecutionResult != nil:
					parts = append(parts, ir.Part{Type: ir.PartText, Text: codeText(p)})
				case p.InlineData != nil || p.FileData != nil:
					parts = append(parts, decodeMedia(p))
				}
			}
			if len(parts) > 0 {
				req.Messages = append(req.Messages, ir.Message{Role: ir.RoleAssistant, Parts: parts})
			}
			continue
		}

		var results, user []ir.Part
		for i := range c.Parts {
			p := &c.Parts[i]
			switch {
			case p.FunctionResponse != nil:
				fr := p.FunctionResponse
				id := ""
				for k := len(calls) - 1; k >= 0; k-- {
					if !calls[k].matched && ((fr.ID != "" && calls[k].id == fr.ID) || (fr.ID == "" && calls[k].name == fr.Name)) {
						calls[k].matched = true
						id = calls[k].id
						break
					}
				}
				if id == "" {
					id = fr.ID
				}
				if id == "" {
					id = "call_" + fr.Name + "_orphan"
				}
				text, isErr := responseText(fr.Response)
				results = append(results, ir.Part{
					Type: ir.PartToolResult, ToolID: id, ToolName: fr.Name, IsError: isErr,
					Result: []ir.Part{{Type: ir.PartText, Text: text}},
				})
			case p.Text != nil:
				if *p.Text != "" {
					user = append(user, ir.Part{Type: ir.PartText, Text: *p.Text})
				}
			case p.InlineData != nil || p.FileData != nil:
				user = append(user, decodeMedia(p))
			case p.ExecutableCode != nil || p.CodeExecutionResult != nil:
				user = append(user, ir.Part{Type: ir.PartText, Text: codeText(p)})
			}
		}
		if len(results) > 0 {
			req.Messages = append(req.Messages, ir.Message{Role: ir.RoleTool, Parts: results})
		}
		if len(user) > 0 {
			req.Messages = append(req.Messages, ir.Message{Role: ir.RoleUser, Parts: user})
		}
	}

	for _, t := range gr.Tools {
		for _, d := range t.FunctionDeclarations {
			tool := ir.Tool{Name: d.Name, Description: d.Description}
			if len(d.ParametersJSONSchema) > 0 {
				tool.Schema = d.ParametersJSONSchema
			} else if len(d.Parameters) > 0 {
				tool.Schema = normalizeSchema(d.Parameters)
			}
			req.Tools = append(req.Tools, tool)
		}
	}
	if gr.ToolConfig != nil && gr.ToolConfig.FunctionCallingConfig != nil {
		fc := gr.ToolConfig.FunctionCallingConfig
		switch strings.ToUpper(fc.Mode) {
		case "NONE":
			req.ToolChoice = &ir.ToolChoice{Mode: "none"}
		case "ANY":
			if len(fc.AllowedFunctionNames) == 1 {
				req.ToolChoice = &ir.ToolChoice{Mode: "tool", Name: fc.AllowedFunctionNames[0]}
			} else {
				req.ToolChoice = &ir.ToolChoice{Mode: "required"}
			}
		case "AUTO", "VALIDATED":
			req.ToolChoice = &ir.ToolChoice{Mode: "auto"}
		}
	}
	if gc := gr.GenerationConfig; gc != nil {
		req.MaxTokens = gc.MaxOutputTokens
		req.Temperature = gc.Temperature
		req.TopP = gc.TopP
		if gc.TopK != nil {
			k := int(*gc.TopK)
			req.TopK = &k
		}
		req.Stop = gc.StopSequences
		if strings.EqualFold(gc.ResponseMimeType, "application/json") {
			schema := gc.ResponseJSONSchema
			if len(schema) == 0 && len(gc.ResponseSchema) > 0 {
				schema = normalizeSchema(gc.ResponseSchema)
			}
			if len(schema) > 0 {
				b := append([]byte(`{"type":"json_schema","json_schema":{"name":"response","schema":`), schema...)
				req.ResponseFormat = append(b, '}', '}')
			} else {
				req.ResponseFormat = json.RawMessage(`{"type":"json_object"}`)
			}
		}
		if tc := gc.ThinkingConfig; tc != nil {
			switch {
			case tc.ThinkingBudget != nil && *tc.ThinkingBudget == 0:
				req.Reasoning = &ir.Reasoning{Disabled: true}
			case tc.ThinkingLevel != "":
				req.Reasoning = &ir.Reasoning{Effort: strings.ToLower(tc.ThinkingLevel)}
			case tc.ThinkingBudget != nil && *tc.ThinkingBudget > 0:
				b := *tc.ThinkingBudget
				req.Reasoning = &ir.Reasoning{BudgetTokens: b, Effort: budgetEffort(b)}
			}
			if req.Reasoning != nil && tc.IncludeThoughts {
				req.Reasoning.Summary = "auto"
			}
		}
	}
	return req, nil
}

func budgetEffort(b int) string {
	switch {
	case b <= 1024:
		return "minimal"
	case b <= 4096:
		return "low"
	case b <= 12288:
		return "medium"
	case b <= 24576:
		return "high"
	}
	return "xhigh"
}

func decodeMedia(p *gPart) ir.Part {
	var out ir.Part
	if p.InlineData != nil {
		out.MediaType, out.Data = p.InlineData.MimeType, p.InlineData.Data
	} else {
		out.MediaType, out.URL = p.FileData.MimeType, p.FileData.FileURI
		if out.MediaType == "" {
			out.MediaType = guessMime(out.URL)
		}
	}
	out.Type = ir.PartFile
	if strings.HasPrefix(out.MediaType, "image/") {
		out.Type = ir.PartImage
	}
	return out
}

func codeText(p *gPart) string {
	if p.ExecutableCode != nil {
		lang := strings.ToLower(p.ExecutableCode.Language)
		if lang == "" || lang == "language_unspecified" {
			lang = ""
		}
		return "```" + lang + "\n" + p.ExecutableCode.Code + "\n```\n"
	}
	return "```\n" + p.CodeExecutionResult.Output + "\n```\n"
}

// responseText unwraps the conventional single-key wrappers clients put
// around a function result.
func responseText(raw json.RawMessage) (string, bool) {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return string(raw), false
	}
	if len(m) == 1 {
		for k, v := range m {
			switch k {
			case "error":
				return rawText(v), true
			case "result", "output", "content":
				return rawText(v), false
			}
		}
	}
	return string(raw), false
}

func rawText(v json.RawMessage) string {
	var s string
	if len(v) > 0 && v[0] == '"' && json.Unmarshal(v, &s) == nil {
		return s
	}
	return string(v)
}

// normalizeSchema lower-cases OpenAPI-style enum types ("OBJECT") used by the
// legacy "parameters" field so the schema is valid JSON Schema.
func normalizeSchema(raw json.RawMessage) json.RawMessage {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return raw
	}
	if !lowerTypes(v) {
		return raw
	}
	b, err := marshalNoEscape(v)
	if err != nil {
		return raw
	}
	return b
}

func lowerTypes(v any) bool {
	changed := false
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			switch k {
			case "type":
				if s, ok := val.(string); ok {
					if l := strings.ToLower(s); l != s {
						t[k] = l
						changed = true
					}
				}
			case "properties", "$defs", "definitions":
				if props, ok := val.(map[string]any); ok {
					for _, pv := range props {
						changed = lowerTypes(pv) || changed
					}
				}
			case "items", "additionalProperties", "not":
				changed = lowerTypes(val) || changed
			case "anyOf", "oneOf", "allOf", "prefixItems":
				if arr, ok := val.([]any); ok {
					for _, e := range arr {
						changed = lowerTypes(e) || changed
					}
				}
			}
		}
	}
	return changed
}
