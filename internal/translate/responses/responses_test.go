package responses

import (
	"bytes"
	"encoding/json"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/Project-by-Myself/clirouter/internal/ir"
	"github.com/Project-by-Myself/clirouter/internal/sse"
)

const codexRequest = `{
 "model":"gpt-5-codex",
 "instructions":"You are Codex.",
 "input":[
  {"type":"message","role":"developer","content":[{"type":"input_text","text":"<permissions>sandboxed</permissions>"}]},
  {"type":"message","role":"user","content":[{"type":"input_text","text":"<environment_context>cwd</environment_context>"}]},
  {"role":"user","content":"fix the bug"},
  {"type":"reasoning","summary":[{"type":"summary_text","text":"**Plan**"},{"type":"summary_text","text":"look at files"}],"content":null,"encrypted_content":"gAAAAopaque"},
  {"type":"function_call","name":"shell","arguments":"{\"command\":[\"ls\"]}","call_id":"call_1"},
  {"type":"function_call_output","call_id":"call_1","output":"a.go\nb.go"},
  {"type":"custom_tool_call","status":"completed","call_id":"call_2","name":"apply_patch","input":"*** Begin Patch\n*** End Patch"},
  {"type":"custom_tool_call_output","call_id":"call_2","output":"Done!"},
  {"type":"local_shell_call","call_id":"x","status":"completed","action":{"type":"exec","command":["ls"]}},
  {"type":"message","role":"assistant","content":[{"type":"output_text","text":"Fixed."}]},
  {"type":"message","role":"user","content":[{"type":"input_text","text":"thanks"},{"type":"input_image","image_url":"data:image/png;base64,iVBOR"}]}
 ],
 "tools":[
  {"type":"function","name":"shell","description":"Run","strict":false,"parameters":{"type":"object","properties":{"command":{"type":"array"}}}},
  {"type":"custom","name":"apply_patch","description":"Patch","format":{"type":"grammar","syntax":"lark","definition":"start: x"}},
  {"type":"web_search"},
  {"type":"local_shell"}
 ],
 "tool_choice":"auto",
 "parallel_tool_calls":false,
 "reasoning":{"effort":"high","summary":"auto"},
 "store":false,
 "stream":true,
 "include":["reasoning.encrypted_content"],
 "prompt_cache_key":"sess-1",
 "text":{"format":{"type":"json_schema","name":"out","schema":{"type":"object"},"strict":true}}
}`

func TestDecodeCodexRequest(t *testing.T) {
	req, err := DecodeRequest([]byte(codexRequest))
	if err != nil {
		t.Fatal(err)
	}
	if req.Model != "gpt-5-codex" || !req.Stream || req.PromptCacheKey != "sess-1" {
		t.Fatalf("scalars: %+v", req)
	}
	if len(req.System) != 2 || req.System[0].Text != "You are Codex." || !strings.Contains(req.System[1].Text, "permissions") {
		t.Fatalf("system: %+v", req.System)
	}
	if req.ParallelToolCalls == nil || *req.ParallelToolCalls {
		t.Fatal("parallel_tool_calls")
	}
	if req.Reasoning == nil || req.Reasoning.Effort != "high" || req.Reasoning.Summary != "auto" {
		t.Fatalf("reasoning: %+v", req.Reasoning)
	}
	if req.ToolChoice == nil || req.ToolChoice.Mode != "auto" {
		t.Fatalf("tool choice: %+v", req.ToolChoice)
	}
	if len(req.Tools) != 2 || req.Tools[0].Name != "shell" || req.Tools[1].Kind != ir.ToolCustom || !bytes.Contains(req.Tools[1].Format, []byte("lark")) {
		t.Fatalf("tools: %+v", req.Tools)
	}
	if string(req.ResponseFormat) != `{"type":"json_schema","json_schema":{"name":"out","schema":{"type":"object"},"strict":true}}` {
		t.Fatalf("response format: %s", req.ResponseFormat)
	}
	roles := []ir.Role{}
	for _, m := range req.Messages {
		roles = append(roles, m.Role)
	}
	wantRoles := []ir.Role{ir.RoleUser, ir.RoleUser, ir.RoleAssistant, ir.RoleTool, ir.RoleAssistant, ir.RoleTool, ir.RoleAssistant, ir.RoleUser}
	if !reflect.DeepEqual(roles, wantRoles) {
		t.Fatalf("roles %v want %v", roles, wantRoles)
	}
	a := req.Messages[2]
	if len(a.Parts) != 2 {
		t.Fatalf("merged assistant: %+v", a.Parts)
	}
	if th := a.Parts[0]; th.Type != ir.PartThinking || th.Text != "**Plan**\n\nlook at files" || th.Signature != "gAAAAopaque" || th.SigSource != ir.SigOpenAI {
		t.Fatalf("thinking: %+v", th)
	}
	if tc := a.Parts[1]; tc.Type != ir.PartToolCall || tc.ToolID != "call_1" || tc.ToolName != "shell" || tc.Args != `{"command":["ls"]}` {
		t.Fatalf("call: %+v", tc)
	}
	if tr := req.Messages[3].Parts[0]; tr.Type != ir.PartToolResult || tr.ToolID != "call_1" || tr.ToolName != "shell" || tr.Result[0].Text != "a.go\nb.go" {
		t.Fatalf("result: %+v", tr)
	}
	if cc := req.Messages[4].Parts[0]; cc.ToolKind != ir.ToolCustom || cc.Args != "*** Begin Patch\n*** End Patch" {
		t.Fatalf("custom call: %+v", cc)
	}
	if cr := req.Messages[5].Parts[0]; cr.ToolKind != ir.ToolCustom || cr.ToolName != "apply_patch" || cr.Result[0].Text != "Done!" {
		t.Fatalf("custom result: %+v", cr)
	}
	if req.Messages[6].Parts[0].Text != "Fixed." {
		t.Fatal("assistant text")
	}
	if img := req.Messages[7].Parts[1]; img.Type != ir.PartImage || img.MediaType != "image/png" || img.Data != "iVBOR" {
		t.Fatalf("image: %+v", img)
	}
}

func TestDecodeRequestVariants(t *testing.T) {
	req, err := DecodeRequest([]byte(`{"model":"m","input":"hi","tool_choice":{"type":"function","name":"f"},"max_output_tokens":99,"safety_identifier":"u1",
	 "reasoning":{"effort":"none"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(req.Messages) != 1 || req.Messages[0].Parts[0].Text != "hi" || req.MaxTokens != 99 || req.User != "u1" {
		t.Fatalf("%+v", req)
	}
	if req.ToolChoice.Mode != "tool" || req.ToolChoice.Name != "f" || !req.Reasoning.Disabled {
		t.Fatalf("%+v %+v", req.ToolChoice, req.Reasoning)
	}
	req, err = DecodeRequest([]byte(`{"input":[{"type":"function_call_output","call_id":"c","output":[{"type":"input_text","text":"t"},{"type":"input_image","image_url":"https://x/y.png"}]},
	 {"type":"message","role":"user","content":[{"type":"input_file","filename":"a.pdf","file_data":"data:application/pdf;base64,JVBE"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	res := req.Messages[0].Parts[0].Result
	if len(res) != 2 || res[1].URL != "https://x/y.png" {
		t.Fatalf("%+v", res)
	}
	if f := req.Messages[1].Parts[0]; f.Type != ir.PartFile || f.MediaType != "application/pdf" || f.Data != "JVBE" || f.Text != "a.pdf" {
		t.Fatalf("%+v", f)
	}
	if _, err := DecodeRequest([]byte(`{"input":[{"type":"message","role":"user","content":42}]}`)); err == nil {
		t.Fatal("expected error for bad content")
	}
}

func TestEnvelope(t *testing.T) {
	cases := []struct{ src, sig, text string }{
		{"anthropic", "EqQBCkYIBxgC", "Let me think\nabout \"this\" ✓"},
		{"gemini", "", "plain"},
		{"", "", ""},
	}
	for _, c := range cases {
		env := EncodeEnvelope(c.src, c.sig, c.text)
		if !strings.HasPrefix(env, "clr1.") || strings.ContainsAny(env, "+/=") {
			t.Fatalf("bad envelope %q", env)
		}
		s, g, x, ok := DecodeEnvelope(env)
		if !ok || s != c.src || g != c.sig || x != c.text {
			t.Fatalf("round trip %v -> %q %q %q %v", c, s, g, x, ok)
		}
	}
	for _, bad := range []string{"gAAAA", "clr1.!!!", "clr1." + "bm90IGpzb24"} {
		if _, _, _, ok := DecodeEnvelope(bad); ok {
			t.Fatalf("%q decoded", bad)
		}
	}
	body := `{"input":[{"type":"reasoning","summary":[{"type":"summary_text","text":"sum"}],"encrypted_content":"` + EncodeEnvelope("anthropic", "SIG", "full thinking") + `"},
	 {"type":"reasoning","summary":[],"encrypted_content":"` + EncodeEnvelope("anthropic-redacted", "OPAQUE", "") + `"}]}`
	req, err := DecodeRequest([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	p := req.Messages[0].Parts
	if p[0].Type != ir.PartThinking || p[0].Text != "full thinking" || p[0].Signature != "SIG" || p[0].SigSource != "anthropic" {
		t.Fatalf("%+v", p[0])
	}
	if p[1].Type != ir.PartRedactedThinking || p[1].Signature != "OPAQUE" || p[1].SigSource != "anthropic" {
		t.Fatalf("%+v", p[1])
	}
}

func sampleIR() *ir.Request {
	temp := 0.5
	return &ir.Request{
		Model:          "client-model",
		System:         []ir.Part{{Type: ir.PartText, Text: "sys1"}, {Type: ir.PartText, Text: "sys2"}},
		MaxTokens:      1000,
		Temperature:    &temp,
		Reasoning:      &ir.Reasoning{BudgetTokens: 8000},
		PromptCacheKey: "k",
		ResponseFormat: json.RawMessage(`{"type":"json_schema","json_schema":{"name":"o","schema":{"type":"object"},"strict":true}}`),
		Tools: []ir.Tool{
			{Name: "shell", Schema: json.RawMessage(`{"type":"object"}`)},
			{Name: "apply_patch", Kind: ir.ToolCustom, Format: json.RawMessage(`{"type":"text"}`)},
		},
		ToolChoice: &ir.ToolChoice{Mode: "tool", Name: "apply_patch"},
		Messages: []ir.Message{
			{Role: ir.RoleUser, Parts: []ir.Part{{Type: ir.PartText, Text: "hi <b>"}, {Type: ir.PartImage, MediaType: "image/png", Data: "AAA"}}},
			{Role: ir.RoleAssistant, Parts: []ir.Part{
				{Type: ir.PartThinking, Text: "anthropic thought", Signature: "x", SigSource: ir.SigAnthropic},
				{Type: ir.PartThinking, Text: "summary", Signature: "enc", SigSource: ir.SigOpenAI},
				{Type: ir.PartText, Text: "calling"},
				{Type: ir.PartToolCall, ToolID: "c1", ToolName: "shell", Args: `{"a":1}`},
				{Type: ir.PartToolCall, ToolID: "c2", ToolName: "apply_patch", ToolKind: ir.ToolCustom, Args: "patch"},
			}},
			{Role: ir.RoleUser, Parts: []ir.Part{
				{Type: ir.PartToolResult, ToolID: "c1", Result: []ir.Part{{Type: ir.PartText, Text: "l1"}, {Type: ir.PartText, Text: "l2"}}},
				{Type: ir.PartToolResult, ToolID: "c2", Result: []ir.Part{{Type: ir.PartText, Text: "ok"}}},
				{Type: ir.PartText, Text: "next"},
			}},
		},
	}
}

func TestEncodeRequest(t *testing.T) {
	for _, pt := range []string{"openai", "codex"} {
		t.Run(pt, func(t *testing.T) {
			body, err := EncodeRequest(sampleIR(), ir.EncodeOptions{Model: "gpt-5", ProviderType: pt})
			if err != nil {
				t.Fatal(err)
			}
			var m map[string]any
			if err := json.Unmarshal(body, &m); err != nil {
				t.Fatal(err)
			}
			if m["model"] != "gpt-5" || m["stream"] != true || m["store"] != false || m["instructions"] != "sys1\n\nsys2" || m["prompt_cache_key"] != "k" {
				t.Fatalf("top: %s", body)
			}
			if !bytes.Contains(body, []byte(`"include":["reasoning.encrypted_content"]`)) || !bytes.Contains(body, []byte(`"reasoning":{"effort":"medium","summary":"auto"}`)) {
				t.Fatalf("reasoning: %s", body)
			}
			if !bytes.Contains(body, []byte(`"tool_choice":{"name":"apply_patch","type":"custom"}`)) {
				t.Fatalf("tool_choice: %s", body)
			}
			if !bytes.Contains(body, []byte(`"text":{"format":{"type":"json_schema","name":"o","schema":{"type":"object"},"strict":true}}`)) {
				t.Fatalf("text: %s", body)
			}
			if !bytes.Contains(body, []byte(`hi <b>`)) {
				t.Fatal("html escaped")
			}
			_, hasMax := m["max_output_tokens"]
			_, hasTemp := m["temperature"]
			pc, hasPC := m["parallel_tool_calls"]
			if pt == "codex" {
				if hasMax || hasTemp || !hasPC || pc != false {
					t.Fatalf("codex knobs: %s", body)
				}
			} else if !hasMax || !hasTemp || hasPC {
				t.Fatalf("openai knobs: %s", body)
			}
			var in []map[string]any
			for _, it := range m["input"].([]any) {
				in = append(in, it.(map[string]any))
			}
			types := []string{}
			for _, it := range in {
				types = append(types, it["type"].(string))
			}
			want := []string{"message", "reasoning", "message", "function_call", "custom_tool_call", "function_call_output", "custom_tool_call_output", "message"}
			if !reflect.DeepEqual(types, want) {
				t.Fatalf("types %v", types)
			}
			if in[1]["encrypted_content"] != "enc" || in[5]["output"] != "l1\nl2" || in[4]["input"] != "patch" {
				t.Fatalf("items: %s", body)
			}
			img := in[0]["content"].([]any)[1].(map[string]any)
			if img["image_url"] != "data:image/png;base64,AAA" {
				t.Fatalf("image %v", img)
			}
			tools := m["tools"].([]any)
			if tools[0].(map[string]any)["strict"] != false || tools[1].(map[string]any)["type"] != "custom" {
				t.Fatalf("tools %v", tools)
			}
		})
	}
}

func TestEncodeDecodeRequestRoundTrip(t *testing.T) {
	req, err := DecodeRequest([]byte(codexRequest))
	if err != nil {
		t.Fatal(err)
	}
	body, err := EncodeRequest(req, ir.EncodeOptions{Model: "gpt-5-codex", ProviderType: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	back, err := DecodeRequest(body)
	if err != nil {
		t.Fatal(err)
	}
	back.Stream = req.Stream
	back.System = []ir.Part{{Type: ir.PartText, Text: back.System[0].Text}}
	req.System = []ir.Part{{Type: ir.PartText, Text: req.System[0].Text + "\n\n" + req.System[1].Text}}
	if !reflect.DeepEqual(req.Messages, back.Messages) || !reflect.DeepEqual(req.System, back.System) {
		t.Fatalf("round trip mismatch\n%s", body)
	}
}

const upstreamTranscript = `event: response.created
data: {"type":"response.created","sequence_number":0,"response":{"id":"resp_up","object":"response","status":"in_progress","model":"gpt-5-codex","output":[]}}

event: response.in_progress
data: {"type":"response.in_progress","sequence_number":1,"response":{"id":"resp_up","status":"in_progress"}}

event: response.output_item.added
data: {"type":"response.output_item.added","sequence_number":2,"output_index":0,"item":{"id":"rs_1","type":"reasoning","summary":[]}}

event: response.reasoning_summary_part.added
data: {"type":"response.reasoning_summary_part.added","sequence_number":3,"item_id":"rs_1","output_index":0,"summary_index":0,"part":{"type":"summary_text","text":""}}

event: response.reasoning_summary_text.delta
data: {"type":"response.reasoning_summary_text.delta","sequence_number":4,"item_id":"rs_1","output_index":0,"summary_index":0,"delta":"**Plan**"}

event: response.reasoning_summary_part.added
data: {"type":"response.reasoning_summary_part.added","sequence_number":5,"item_id":"rs_1","output_index":0,"summary_index":1,"part":{"type":"summary_text","text":""}}

event: response.reasoning_summary_text.delta
data: {"type":"response.reasoning_summary_text.delta","sequence_number":6,"item_id":"rs_1","output_index":0,"summary_index":1,"delta":"read \"files\""}

event: response.output_item.done
data: {"type":"response.output_item.done","sequence_number":7,"output_index":0,"item":{"id":"rs_1","type":"reasoning","summary":[{"type":"summary_text","text":"**Plan**"}],"encrypted_content":"gAAAA-enc"}}

event: response.output_item.added
data: {"type":"response.output_item.added","sequence_number":8,"output_index":1,"item":{"id":"msg_1","type":"message","status":"in_progress","role":"assistant","content":[]}}

event: response.output_text.delta
data: {"type":"response.output_text.delta","sequence_number":9,"item_id":"msg_1","output_index":1,"content_index":0,"delta":"Let me look."}

event: response.output_item.done
data: {"type":"response.output_item.done","sequence_number":10,"output_index":1,"item":{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Let me look."}]}}

event: response.output_item.added
data: {"type":"response.output_item.added","sequence_number":11,"output_index":2,"item":{"id":"fc_1","type":"function_call","status":"in_progress","call_id":"call_9","name":"shell","arguments":""}}

event: response.function_call_arguments.delta
data: {"type":"response.function_call_arguments.delta","sequence_number":12,"item_id":"fc_1","output_index":2,"delta":"{\"command\":"}

event: response.function_call_arguments.delta
data: {"type":"response.function_call_arguments.delta","sequence_number":13,"item_id":"fc_1","output_index":2,"delta":"[\"ls\"]}"}

event: response.function_call_arguments.done
data: {"type":"response.function_call_arguments.done","sequence_number":14,"item_id":"fc_1","output_index":2,"arguments":"{\"command\":[\"ls\"]}"}

event: response.output_item.done
data: {"type":"response.output_item.done","sequence_number":15,"output_index":2,"item":{"id":"fc_1","type":"function_call","status":"completed","call_id":"call_9","name":"shell","arguments":"{\"command\":[\"ls\"]}"}}

event: response.output_item.added
data: {"type":"response.output_item.added","sequence_number":16,"output_index":3,"item":{"id":"ctc_1","type":"custom_tool_call","status":"in_progress","call_id":"call_10","name":"apply_patch","input":""}}

event: response.output_item.done
data: {"type":"response.output_item.done","sequence_number":17,"output_index":3,"item":{"id":"ctc_1","type":"custom_tool_call","status":"completed","call_id":"call_10","name":"apply_patch","input":"*** Begin Patch"}}

event: response.completed
data: {"type":"response.completed","sequence_number":18,"response":{"id":"resp_up","status":"completed","model":"gpt-5-codex","output":[],"usage":{"input_tokens":1200,"input_tokens_details":{"cached_tokens":1000},"output_tokens":300,"output_tokens_details":{"reasoning_tokens":200},"total_tokens":1500}}}

`

func decodeAll(t *testing.T, stream string) []ir.Event {
	t.Helper()
	r := sse.NewReader(strings.NewReader(stream), 0)
	d := NewStreamDecoder()
	var evs []ir.Event
	for {
		e, err := r.Next()
		if err == io.EOF {
			return evs
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := d.Decode(e.Name, e.Data, func(ev ir.Event) { evs = append(evs, ev) }); err != nil {
			t.Fatal(err)
		}
	}
}

func TestStreamDecode(t *testing.T) {
	evs := decodeAll(t, upstreamTranscript)
	want := []ir.Event{
		{Kind: ir.EvStart, ID: "resp_up", Model: "gpt-5-codex"},
		{Kind: ir.EvThinking, Text: "**Plan**"},
		{Kind: ir.EvThinking, Text: "\n\n"},
		{Kind: ir.EvThinking, Text: `read "files"`},
		{Kind: ir.EvThinkingSignature, Signature: "gAAAA-enc", SigSource: ir.SigOpenAI},
		{Kind: ir.EvText, Text: "Let me look."},
		{Kind: ir.EvToolStart, ID: "call_9", Name: "shell"},
		{Kind: ir.EvToolArgs, ID: "call_9", Text: `{"command":`},
		{Kind: ir.EvToolArgs, ID: "call_9", Text: `["ls"]}`},
		{Kind: ir.EvToolEnd, ID: "call_9"},
		{Kind: ir.EvToolStart, ID: "call_10", Name: "apply_patch", ToolKind: ir.ToolCustom},
		{Kind: ir.EvToolArgs, ID: "call_10", Text: "*** Begin Patch"},
		{Kind: ir.EvToolEnd, ID: "call_10"},
		{Kind: ir.EvUsage, Usage: &ir.Usage{InputTokens: 1200, OutputTokens: 300, CacheReadTokens: 1000, ReasoningTokens: 200}},
		{Kind: ir.EvFinish, Finish: ir.FinishToolCalls},
		{Kind: ir.EvDone},
	}
	if !reflect.DeepEqual(evs, want) {
		for i := range evs {
			t.Logf("%d %+v", i, evs[i])
		}
		t.Fatal("event mismatch")
	}
}

func TestStreamDecodeTerminalVariants(t *testing.T) {
	cases := []struct {
		name, data string
		want       []ir.Event
	}{
		{"incomplete", `{"type":"response.incomplete","response":{"id":"r","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"usage":{"input_tokens":1,"output_tokens":2}}}`,
			[]ir.Event{{Kind: ir.EvStart, ID: "r"}, {Kind: ir.EvUsage, Usage: &ir.Usage{InputTokens: 1, OutputTokens: 2}}, {Kind: ir.EvFinish, Finish: ir.FinishLength}, {Kind: ir.EvDone}}},
		{"filter", `{"type":"response.incomplete","response":{"id":"r","incomplete_details":{"reason":"content_filter"}}}`,
			[]ir.Event{{Kind: ir.EvStart, ID: "r"}, {Kind: ir.EvFinish, Finish: ir.FinishContentFilter}, {Kind: ir.EvDone}}},
		{"failed", `{"type":"response.failed","response":{"id":"r","status":"failed","error":{"code":"rate_limit_exceeded","message":"slow down"}}}`,
			[]ir.Event{{Kind: ir.EvError, Text: "slow down", ErrType: "rate_limit_exceeded"}}},
		{"error", `{"type":"error","code":"server_error","message":"boom","param":null}`,
			[]ir.Event{{Kind: ir.EvError, Text: "boom", ErrType: "server_error"}}},
		{"ignored", `{"type":"response.output_text.annotation.added","annotation":{}}`, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var evs []ir.Event
			if err := NewStreamDecoder().Decode([]byte("x"), []byte(c.data), func(e ir.Event) { evs = append(evs, e) }); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(evs, c.want) {
				t.Fatalf("got %+v", evs)
			}
		})
	}
}

type parsedEvent struct {
	name string
	m    map[string]any
	raw  []byte
}

func parseStream(t *testing.T, b []byte) []parsedEvent {
	t.Helper()
	r := sse.NewReader(bytes.NewReader(b), 0)
	var out []parsedEvent
	for {
		e, err := r.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(e.Data, &m); err != nil {
			t.Fatalf("invalid json %s: %v", e.Data, err)
		}
		if m["type"] != string(e.Name) {
			t.Fatalf("name %s != type %v", e.Name, m["type"])
		}
		out = append(out, parsedEvent{string(e.Name), m, append([]byte(nil), e.Data...)})
	}
}

func encodeAll(enc ir.StreamEncoder, evs []ir.Event) []byte {
	var out []byte
	for _, ev := range evs {
		out = enc.Encode(out, ev)
	}
	return out
}

func checkSequence(t *testing.T, evs []parsedEvent) {
	t.Helper()
	for i, e := range evs {
		if int(e.m["sequence_number"].(float64)) != i {
			t.Fatalf("event %d (%s) sequence_number %v", i, e.name, e.m["sequence_number"])
		}
	}
}

func TestStreamEncodeRoundTrip(t *testing.T) {
	input := []ir.Event{
		{Kind: ir.EvStart, ID: "up", Model: "claude"},
		{Kind: ir.EvThinking, Text: "think \"hard\"\n"},
		{Kind: ir.EvThinking, Text: "more "},
		{Kind: ir.EvThinkingSignature, Signature: "ANTSIG", SigSource: ir.SigAnthropic},
		{Kind: ir.EvRedactedThinking, Signature: "REDACTED", SigSource: ir.SigAnthropic},
		{Kind: ir.EvText, Text: "Hello "},
		{Kind: ir.EvText, Text: "world <&>"},
		{Kind: ir.EvToolStart, ID: "call_a", Name: "shell"},
		{Kind: ir.EvToolArgs, ID: "call_a", Text: `{"command":`},
		{Kind: ir.EvToolArgs, ID: "call_a", Text: `["ls"]}`},
		{Kind: ir.EvToolEnd, ID: "call_a"},
		{Kind: ir.EvToolStart, ID: "call_b", Name: "apply_patch"},
		{Kind: ir.EvToolArgs, ID: "call_b", Text: "*** Begin Patch\n"},
		{Kind: ir.EvToolEnd, ID: "call_b"},
		{Kind: ir.EvUsage, Usage: &ir.Usage{InputTokens: 100, OutputTokens: 50, CacheReadTokens: 80, ReasoningTokens: 10}},
		{Kind: ir.EvFinish, Finish: ir.FinishToolCalls},
		{Kind: ir.EvDone},
		{Kind: ir.EvText, Text: "after terminal"},
	}
	req := &ir.Request{Tools: []ir.Tool{{Name: "shell"}, {Name: "apply_patch", Kind: ir.ToolCustom}}}
	out := encodeAll(NewStreamEncoder(req, "gpt-5-codex"), input)
	evs := parseStream(t, out)
	checkSequence(t, evs)
	if evs[0].name != "response.created" || evs[1].name != "response.in_progress" || evs[len(evs)-1].name != "response.completed" {
		t.Fatalf("framing: %s ... %s", evs[0].name, evs[len(evs)-1].name)
	}
	if bytes.Contains(out, []byte("after terminal")) {
		t.Fatal("emitted after terminal")
	}

	var doneItems []map[string]any
	nextAdded := 0
	for _, e := range evs {
		switch e.name {
		case "response.output_item.added":
			if int(e.m["output_index"].(float64)) != nextAdded {
				t.Fatalf("output_index %v want %d", e.m["output_index"], nextAdded)
			}
			nextAdded++
		case "response.output_item.done":
			if int(e.m["output_index"].(float64)) != len(doneItems) {
				t.Fatalf("done output_index %v", e.m["output_index"])
			}
			doneItems = append(doneItems, e.m["item"].(map[string]any))
		}
	}
	if len(doneItems) != 5 {
		t.Fatalf("items: %d", len(doneItems))
	}
	types := []string{}
	for _, it := range doneItems {
		types = append(types, it["type"].(string))
	}
	if !reflect.DeepEqual(types, []string{"reasoning", "reasoning", "message", "function_call", "custom_tool_call"}) {
		t.Fatalf("types %v", types)
	}
	if s, g, x, ok := DecodeEnvelope(doneItems[0]["encrypted_content"].(string)); !ok || s != "anthropic" || g != "ANTSIG" || x != "think \"hard\"\nmore " {
		t.Fatalf("envelope %q %q %q", s, g, x)
	}
	if s, g, _, ok := DecodeEnvelope(doneItems[1]["encrypted_content"].(string)); !ok || s != "anthropic-redacted" || g != "REDACTED" {
		t.Fatal("redacted envelope")
	}
	if doneItems[2]["content"].([]any)[0].(map[string]any)["text"] != "Hello world <&>" {
		t.Fatal("message text")
	}
	if doneItems[3]["arguments"] != `{"command":["ls"]}` || doneItems[4]["input"] != "*** Begin Patch\n" {
		t.Fatal("tool args")
	}

	final := evs[len(evs)-1].m["response"].(map[string]any)
	if final["status"] != "completed" || len(final["output"].([]any)) != 5 || final["model"] != "gpt-5-codex" {
		t.Fatalf("final %v", final)
	}
	u := final["usage"].(map[string]any)
	if u["total_tokens"] != 150.0 || u["input_tokens_details"].(map[string]any)["cached_tokens"] != 80.0 {
		t.Fatalf("usage %v", u)
	}

	// Re-decode the client stream with our own upstream decoder and aggregate.
	agg := ir.NewAggregator()
	for _, ev := range decodeAll(t, string(out)) {
		agg.Add(ev)
	}
	resp := agg.Response()
	if resp.Finish != ir.FinishToolCalls || resp.Usage.CacheReadTokens != 80 || resp.Usage.InputTokens != 100 {
		t.Fatalf("resp %+v", resp)
	}
	var kinds []ir.PartType
	for _, p := range resp.Content {
		kinds = append(kinds, p.Type)
	}
	// Two adjacent reasoning items fold into one thinking part in ir.Aggregator.
	if !reflect.DeepEqual(kinds, []ir.PartType{ir.PartThinking, ir.PartText, ir.PartToolCall, ir.PartToolCall}) {
		t.Fatalf("kinds %v", kinds)
	}
	if resp.Content[1].Text != "Hello world <&>" || resp.Content[3].ToolKind != ir.ToolCustom || resp.Content[3].Args != "*** Begin Patch\n" {
		t.Fatalf("content %+v", resp.Content)
	}
}

func TestStreamEncodeOpenAISignatureOnlyAndIncomplete(t *testing.T) {
	out := encodeAll(NewStreamEncoder(nil, "m"), []ir.Event{
		{Kind: ir.EvThinkingSignature, Signature: "gAAAenc", SigSource: ir.SigOpenAI},
		{Kind: ir.EvText, Text: "partial"},
		{Kind: ir.EvFinish, Finish: ir.FinishLength},
		{Kind: ir.EvDone},
	})
	evs := parseStream(t, out)
	checkSequence(t, evs)
	last := evs[len(evs)-1]
	if last.name != "response.incomplete" {
		t.Fatalf("got %s", last.name)
	}
	r := last.m["response"].(map[string]any)
	if r["status"] != "incomplete" || r["incomplete_details"].(map[string]any)["reason"] != "max_output_tokens" {
		t.Fatalf("%v", r)
	}
	rs := r["output"].([]any)[0].(map[string]any)
	if rs["encrypted_content"] != "gAAAenc" || len(rs["summary"].([]any)) != 0 {
		t.Fatalf("reasoning %v", rs)
	}
	var fin []ir.Event
	for _, ev := range decodeAll(t, string(out)) {
		if ev.Kind == ir.EvFinish || ev.Kind == ir.EvThinkingSignature {
			fin = append(fin, ev)
		}
	}
	if len(fin) != 2 || fin[0].Signature != "gAAAenc" || fin[1].Finish != ir.FinishLength {
		t.Fatalf("%+v", fin)
	}
}

func TestStreamEncodeError(t *testing.T) {
	out := encodeAll(NewStreamEncoder(nil, "m"), []ir.Event{
		{Kind: ir.EvStart},
		{Kind: ir.EvText, Text: "x"},
		{Kind: ir.EvError, Text: "overloaded", ErrType: "overloaded_error"},
		{Kind: ir.EvDone},
	})
	evs := parseStream(t, out)
	checkSequence(t, evs)
	last := evs[len(evs)-1]
	if last.name != "response.failed" {
		t.Fatalf("%s", last.name)
	}
	if et, msg, ok := IsErrorEvent([]byte(last.name), last.raw); !ok || et != "overloaded_error" || msg != "overloaded" {
		t.Fatalf("%q %q %v", et, msg, ok)
	}
	if !IsTerminal(nil, last.raw) {
		t.Fatal("terminal")
	}
}

func TestEncodeResponse(t *testing.T) {
	resp := &ir.Response{
		ID: "x", Model: "up",
		Content: []ir.Part{
			{Type: ir.PartThinking, Text: "t", Signature: "s", SigSource: ir.SigGemini},
			{Type: ir.PartText, Text: "a"},
			{Type: ir.PartText, Text: "b"},
			{Type: ir.PartToolCall, ToolID: "c", ToolName: "f", Args: "{}"},
		},
		Finish: ir.FinishToolCalls,
		Usage:  ir.Usage{InputTokens: 3, OutputTokens: 4},
	}
	b, err := EncodeResponse(&ir.Request{Model: "client"}, resp)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("%v %s", err, b)
	}
	out := m["output"].([]any)
	if m["status"] != "completed" || m["model"] != "client" || len(out) != 3 || m["object"] != "response" {
		t.Fatalf("%s", b)
	}
	if out[1].(map[string]any)["content"].([]any)[0].(map[string]any)["text"] != "ab" {
		t.Fatalf("%s", b)
	}
	if out[2].(map[string]any)["call_id"] != "c" || m["usage"].(map[string]any)["total_tokens"] != 7.0 {
		t.Fatalf("%s", b)
	}
}

func TestHelpers(t *testing.T) {
	completed := []byte(`{"type":"response.completed","response":{"usage":{"input_tokens":5,"input_tokens_details":{"cached_tokens":2},"output_tokens":7,"output_tokens_details":{"reasoning_tokens":1}}}}`)
	u, ok := UsageFromEvent(nil, completed)
	if !ok || *u != (ir.Usage{InputTokens: 5, CacheReadTokens: 2, OutputTokens: 7, ReasoningTokens: 1}) {
		t.Fatalf("%+v", u)
	}
	if _, ok := UsageFromEvent([]byte("response.completed"), []byte(`{"type":"response.output_text.delta","delta":"x"}`)); ok {
		t.Fatal("type in data must win")
	}
	if !IsTerminal(nil, completed) || IsTerminal([]byte("response.completed"), []byte(`{"type":"response.created"}`)) {
		t.Fatal("IsTerminal")
	}
	if !IsTerminal([]byte("response.incomplete"), []byte(`{}`)) {
		t.Fatal("fallback to name")
	}
	if et, msg, ok := IsErrorEvent([]byte("error"), []byte(`{"type":"error","error":{"type":"invalid_request_error","message":"bad"}}`)); !ok || et != "invalid_request_error" || msg != "bad" {
		t.Fatalf("%q %q", et, msg)
	}
	if string(Ping()) != ": keepalive\n\n" {
		t.Fatal("ping")
	}
	var eb struct {
		Error struct{ Message, Type, Code string }
	}
	if err := json.Unmarshal(ErrorBody(429, "", "slow \"down\""), &eb); err != nil || eb.Error.Type != "rate_limit_exceeded" || eb.Error.Message != `slow "down"` {
		t.Fatalf("%+v %v", eb, err)
	}
	se := parseStream(t, StreamErrorEvent("server_error", "x"))
	r := se[0].m["response"].(map[string]any)
	if se[0].name != "response.failed" || r["status"] != "failed" || r["error"].(map[string]any)["code"] != "server_error" {
		t.Fatalf("%v", se[0].m)
	}
}

func TestAppendJSONString(t *testing.T) {
	cases := []string{"", "plain", "q\"b\\s", "\n\r\t\b\f\x00\x1f", "héllo ✓ 😀", "  ", "bad\xffutf8\xc3", "<html>&"}
	for _, s := range cases {
		b := appendJSONString(nil, s)
		if !json.Valid(b) {
			t.Fatalf("invalid %q -> %s", s, b)
		}
		var got string
		_ = json.Unmarshal(b, &got)
		want := strings.ToValidUTF8(s, "�")
		if s == "bad\xffutf8\xc3" {
			want = "bad�utf8�"
		}
		if got != want {
			t.Fatalf("%q -> %q", s, got)
		}
		if bytes.ContainsAny(b, "  ") {
			t.Fatalf("raw line separator in %s", b)
		}
	}
}

func BenchmarkStreamEncodeText(b *testing.B) {
	enc := NewStreamEncoder(nil, "m")
	buf := enc.Encode(nil, ir.Event{Kind: ir.EvText, Text: "x"})
	ev := ir.Event{Kind: ir.EvText, Text: "some streamed \"token\" text\n"}
	b.ReportAllocs()
	for b.Loop() {
		buf = enc.Encode(buf[:0], ev)
	}
}
