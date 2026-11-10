package anthropic

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/Project-by-Myself/clirouter/internal/ir"
	"github.com/Project-by-Myself/clirouter/internal/sse"
)

func TestAppendJSONString(t *testing.T) {
	cases := []string{
		"", "hello", `quo"te`, `back\slash`, "nl\nr\rt\t", "ctl\x00\x01\x1f",
		"unicode ✓ 日本語 🎉", "ls\u2028ps\u2029", "<html>&amp;",
	}
	for _, s := range cases {
		b := appendJSONString(nil, s)
		var got string
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatalf("%q: invalid json %s: %v", s, b, err)
		}
		if got != s {
			t.Fatalf("roundtrip %q -> %q", s, got)
		}
	}
	b := appendJSONString(nil, "a\xffb")
	var got string
	if err := json.Unmarshal(b, &got); err != nil || got != "a�b" {
		t.Fatalf("invalid utf8: %s %q %v", b, got, err)
	}
	if s := string(appendJSONString(nil, "\u2028")); s != `"\u2028"` {
		t.Fatalf("u2028 not escaped: %s", s)
	}
}

const claudeCodeRequest = `{
  "model": "claude-sonnet-4-5",
  "max_tokens": 32000,
  "stream": true,
  "temperature": 1,
  "metadata": {"user_id": "user_abc"},
  "system": [
    {"type": "text", "text": "You are Claude Code."},
    {"type": "text", "text": "Big context", "cache_control": {"type": "ephemeral"}}
  ],
  "thinking": {"type": "enabled", "budget_tokens": 10000},
  "tools": [
    {"name": "Bash", "description": "run", "input_schema": {"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}, "cache_control": {"type":"ephemeral"}},
    {"type": "web_search_20250305", "name": "web_search", "max_uses": 5},
    {"name": "Read", "description": "read", "input_schema": {"type":"object","properties":{"path":{"type":"string"}}}}
  ],
  "tool_choice": {"type": "auto", "disable_parallel_tool_use": true},
  "messages": [
    {"role": "user", "content": "list files"},
    {"role": "assistant", "content": [
      {"type": "thinking", "thinking": "I should run ls", "signature": "SIG1"},
      {"type": "redacted_thinking", "data": "REDACTED"},
      {"type": "text", "text": "Running ls."},
      {"type": "tool_use", "id": "toolu_01", "name": "Bash", "input": {"command": "ls"}},
      {"type": "tool_use", "id": "toolu_02", "name": "Read", "input": {"path": "a.txt"}}
    ]},
    {"role": "user", "content": [
      {"type": "tool_result", "tool_use_id": "toolu_01", "content": "a.txt\nb.txt"},
      {"type": "tool_result", "tool_use_id": "toolu_02", "is_error": true, "content": [{"type":"text","text":"denied"}]},
      {"type": "text", "text": "continue", "cache_control": {"type": "ephemeral"}},
      {"type": "image", "source": {"type": "base64", "media_type": "image/png", "data": "iVBOR"}}
    ]}
  ]
}`

func TestDecodeClaudeCodeRequest(t *testing.T) {
	req, err := DecodeRequest([]byte(claudeCodeRequest))
	if err != nil {
		t.Fatal(err)
	}
	if req.Model != "claude-sonnet-4-5" || req.MaxTokens != 32000 || !req.Stream || req.User != "user_abc" {
		t.Fatalf("scalars: %+v", req)
	}
	if len(req.System) != 2 || string(req.System[1].CacheControl) != `{"type": "ephemeral"}` {
		t.Fatalf("system: %+v", req.System)
	}
	if req.Reasoning == nil || req.Reasoning.BudgetTokens != 10000 {
		t.Fatalf("reasoning: %+v", req.Reasoning)
	}
	if len(req.Tools) != 2 || req.Tools[0].Name != "Bash" || req.Tools[1].Name != "Read" || req.Tools[0].CacheControl == nil {
		t.Fatalf("tools: %+v", req.Tools)
	}
	if req.ToolChoice == nil || req.ToolChoice.Mode != "auto" || req.ParallelToolCalls == nil || *req.ParallelToolCalls {
		t.Fatalf("tool choice: %+v %v", req.ToolChoice, req.ParallelToolCalls)
	}
	roles := []ir.Role{}
	for _, m := range req.Messages {
		roles = append(roles, m.Role)
	}
	want := []ir.Role{ir.RoleUser, ir.RoleAssistant, ir.RoleTool, ir.RoleUser}
	if !reflect.DeepEqual(roles, want) {
		t.Fatalf("roles %v want %v", roles, want)
	}
	asst := req.Messages[1].Parts
	if asst[0].Type != ir.PartThinking || asst[0].Signature != "SIG1" || asst[0].SigSource != ir.SigAnthropic {
		t.Fatalf("thinking: %+v", asst[0])
	}
	if asst[1].Type != ir.PartRedactedThinking || asst[1].Signature != "REDACTED" {
		t.Fatalf("redacted: %+v", asst[1])
	}
	if asst[3].Type != ir.PartToolCall || asst[3].Args != `{"command": "ls"}` || asst[3].ToolID != "toolu_01" {
		t.Fatalf("tool call: %+v", asst[3])
	}
	tool := req.Messages[2].Parts
	if len(tool) != 2 || tool[0].ToolID != "toolu_01" || tool[0].Result[0].Text != "a.txt\nb.txt" || !tool[1].IsError || tool[1].Result[0].Text != "denied" {
		t.Fatalf("tool results: %+v", tool)
	}
	user := req.Messages[3].Parts
	if len(user) != 2 || user[0].Text != "continue" || user[0].CacheControl == nil || user[1].Type != ir.PartImage || user[1].Data != "iVBOR" {
		t.Fatalf("trailing user: %+v", user)
	}
}

func encodeToMap(t *testing.T, req *ir.Request, model string) map[string]any {
	t.Helper()
	b, err := EncodeRequest(req, ir.EncodeOptions{Model: model})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("invalid json %s: %v", b, err)
	}
	return m
}

func TestEncodeRoundTripClaudeCode(t *testing.T) {
	req, err := DecodeRequest([]byte(claudeCodeRequest))
	if err != nil {
		t.Fatal(err)
	}
	m := encodeToMap(t, req, "claude-opus-4-1")
	if m["model"] != "claude-opus-4-1" || m["stream"] != true {
		t.Fatalf("model/stream: %v %v", m["model"], m["stream"])
	}
	th := m["thinking"].(map[string]any)
	if th["type"] != "enabled" || th["budget_tokens"].(float64) != 10000 {
		t.Fatalf("thinking: %v", th)
	}
	if _, ok := m["temperature"]; ok {
		t.Fatal("temperature must be dropped when thinking")
	}
	if m["metadata"].(map[string]any)["user_id"] != "user_abc" {
		t.Fatal("metadata")
	}
	tc := m["tool_choice"].(map[string]any)
	if tc["type"] != "auto" || tc["disable_parallel_tool_use"] != true {
		t.Fatalf("tool_choice: %v", tc)
	}
	msgs := m["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("want 3 messages, got %d: %v", len(msgs), msgs)
	}
	last := msgs[2].(map[string]any)
	content := last["content"].([]any)
	types := []string{}
	for _, c := range content {
		types = append(types, c.(map[string]any)["type"].(string))
	}
	if !reflect.DeepEqual(types, []string{"tool_result", "tool_result", "text", "image"}) {
		t.Fatalf("merged user content types %v", types)
	}
	if content[1].(map[string]any)["is_error"] != true {
		t.Fatal("is_error lost")
	}
	if content[2].(map[string]any)["cache_control"] == nil {
		t.Fatal("cache_control lost")
	}
	asst := msgs[1].(map[string]any)["content"].([]any)
	if asst[0].(map[string]any)["type"] != "thinking" || asst[0].(map[string]any)["signature"] != "SIG1" {
		t.Fatalf("assistant[0] %v", asst[0])
	}
	if asst[1].(map[string]any)["type"] != "redacted_thinking" || asst[1].(map[string]any)["data"] != "REDACTED" {
		t.Fatalf("assistant[1] %v", asst[1])
	}
	tu := asst[3].(map[string]any)
	if tu["input"].(map[string]any)["command"] != "ls" {
		t.Fatalf("tool_use input %v", tu)
	}
	tools := m["tools"].([]any)
	if len(tools) != 2 {
		t.Fatalf("tools %v", tools)
	}

	// Decoding our own output again yields an equivalent IR.
	b, _ := EncodeRequest(req, ir.EncodeOptions{Model: req.Model})
	req2, err := DecodeRequest(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(req2.Messages) != len(req.Messages) || req2.Reasoning.BudgetTokens != 10000 {
		t.Fatalf("re-decode mismatch: %d vs %d", len(req2.Messages), len(req.Messages))
	}
}

func TestEncodeThinkingDisableRule(t *testing.T) {
	base := func(parts ...ir.Part) *ir.Request {
		return &ir.Request{
			Model:       "m",
			Reasoning:   &ir.Reasoning{Effort: "high"},
			Temperature: new(0.5),
			Messages: []ir.Message{
				{Role: ir.RoleUser, Parts: []ir.Part{{Type: ir.PartText, Text: "hi"}}},
				{Role: ir.RoleAssistant, Parts: parts},
				{Role: ir.RoleTool, Parts: []ir.Part{{Type: ir.PartToolResult, ToolID: "c1", Result: []ir.Part{{Type: ir.PartText, Text: "ok"}}}}},
			},
		}
	}
	call := ir.Part{Type: ir.PartToolCall, ToolID: "c1", ToolName: "f", Args: `{"a":1}`}
	cases := []struct {
		name      string
		req       *ir.Request
		thinking  bool
		maxTokens float64
	}{
		{"unsigned tool turn", base(call), false, 8192},
		{"openai-signed tool turn", base(ir.Part{Type: ir.PartThinking, Text: "x", Signature: "enc", SigSource: ir.SigOpenAI}, call), false, 8192},
		{"anthropic-signed tool turn", base(ir.Part{Type: ir.PartThinking, Text: "x", Signature: "s", SigSource: ir.SigAnthropic}, call), true, 32000},
		{"no tool turns", &ir.Request{Model: "m", Reasoning: &ir.Reasoning{Effort: "low"}, MaxTokens: 1000, Messages: []ir.Message{{Role: ir.RoleUser, Parts: []ir.Part{{Type: ir.PartText, Text: "hi"}}}}}, true, 2048 + 8192},
		{"disabled", &ir.Request{Model: "m", Reasoning: &ir.Reasoning{Disabled: true, BudgetTokens: 5000}, Messages: []ir.Message{{Role: ir.RoleUser, Parts: []ir.Part{{Type: ir.PartText, Text: "hi"}}}}}, false, 8192},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := encodeToMap(t, c.req, "")
			_, has := m["thinking"]
			if has != c.thinking {
				t.Fatalf("thinking=%v want %v: %v", has, c.thinking, m["thinking"])
			}
			if m["max_tokens"].(float64) != c.maxTokens {
				t.Fatalf("max_tokens=%v want %v", m["max_tokens"], c.maxTokens)
			}
			_, hasTemp := m["temperature"]
			if c.req.Temperature != nil && hasTemp == c.thinking {
				t.Fatalf("temperature presence %v with thinking %v", hasTemp, c.thinking)
			}
			// Only anthropic-signed thinking blocks survive.
			for _, msg := range m["messages"].([]any) {
				for _, blk := range msg.(map[string]any)["content"].([]any) {
					b := blk.(map[string]any)
					if b["type"] == "thinking" && b["signature"] != "s" {
						t.Fatalf("foreign thinking emitted: %v", b)
					}
				}
			}
		})
	}
}

func TestEncodeCustomToolsAndArgs(t *testing.T) {
	f := false
	req := &ir.Request{
		Model: "m",
		Tools: []ir.Tool{
			{Name: "apply_patch", Kind: ir.ToolCustom, Description: "patch"},
			{Name: "fn"},
		},
		ToolChoice:        &ir.ToolChoice{Mode: "required"},
		ParallelToolCalls: &f,
		Messages: []ir.Message{
			{Role: ir.RoleUser, Parts: []ir.Part{{Type: ir.PartText, Text: "go"}}},
			{Role: ir.RoleAssistant, Parts: []ir.Part{
				{Type: ir.PartToolCall, ToolID: "call.1", ToolName: "apply_patch", ToolKind: ir.ToolCustom, Args: "*** Begin \"Patch\"\n"},
				{Type: ir.PartToolCall, ToolName: "fn", Args: ""},
				{Type: ir.PartToolCall, ToolID: "c3", ToolName: "fn", Args: `not json`},
			}},
			{Role: ir.RoleTool, Parts: []ir.Part{{Type: ir.PartToolResult, ToolID: "call.1", Result: []ir.Part{{Type: ir.PartText, Text: "done"}}}}},
			{Role: ir.RoleTool, Parts: []ir.Part{{Type: ir.PartToolResult, ToolName: "fn", Text: "r2"}}},
			{Role: ir.RoleTool, Parts: []ir.Part{{Type: ir.PartToolResult, ToolID: "c3", Result: []ir.Part{{Type: ir.PartImage, URL: "data:image/jpeg;base64,AAAA"}}}}},
		},
	}
	m := encodeToMap(t, req, "")
	tools := m["tools"].([]any)
	schema := tools[0].(map[string]any)["input_schema"].(map[string]any)
	if schema["required"].([]any)[0] != "input" {
		t.Fatalf("custom schema %v", schema)
	}
	if tools[1].(map[string]any)["input_schema"].(map[string]any)["type"] != "object" {
		t.Fatal("default schema")
	}
	tc := m["tool_choice"].(map[string]any)
	if tc["type"] != "any" || tc["disable_parallel_tool_use"] != true {
		t.Fatalf("tool_choice %v", tc)
	}
	msgs := m["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("consecutive tool messages must merge: %d", len(msgs))
	}
	calls := msgs[1].(map[string]any)["content"].([]any)
	c0 := calls[0].(map[string]any)
	if c0["id"] != "call_1" || c0["input"].(map[string]any)["input"] != "*** Begin \"Patch\"\n" {
		t.Fatalf("custom call %v", c0)
	}
	c1 := calls[1].(map[string]any)
	if !strings.HasPrefix(c1["id"].(string), "toolu_") || len(c1["input"].(map[string]any)) != 0 {
		t.Fatalf("empty-args call %v", c1)
	}
	if calls[2].(map[string]any)["input"].(map[string]any)["_raw"] != "not json" {
		t.Fatalf("invalid args %v", calls[2])
	}
	results := msgs[2].(map[string]any)["content"].([]any)
	if results[0].(map[string]any)["tool_use_id"] != "call_1" || results[1].(map[string]any)["tool_use_id"] != c1["id"] {
		t.Fatalf("result ids %v / %v", results, c1["id"])
	}
	img := results[2].(map[string]any)["content"].([]any)[0].(map[string]any)["source"].(map[string]any)
	if img["type"] != "base64" || img["media_type"] != "image/jpeg" {
		t.Fatalf("data url image %v", img)
	}
}

func TestEncodeToolChoiceAndStubTools(t *testing.T) {
	req := &ir.Request{
		Model:      "m",
		ToolChoice: &ir.ToolChoice{Mode: "none"},
		Messages: []ir.Message{
			{Role: ir.RoleAssistant, Parts: []ir.Part{{Type: ir.PartToolCall, ToolID: "a", ToolName: "x", Args: "{}"}}},
			{Role: ir.RoleTool, Parts: []ir.Part{{Type: ir.PartToolResult, ToolID: "a"}}},
		},
	}
	m := encodeToMap(t, req, "")
	if len(m["tools"].([]any)) != 1 || m["tool_choice"].(map[string]any)["type"] != "none" {
		t.Fatalf("stub tools %v %v", m["tools"], m["tool_choice"])
	}
	req = &ir.Request{Model: "m", Tools: []ir.Tool{{Name: "x"}}, ToolChoice: &ir.ToolChoice{Mode: "tool", Name: "x"}, Reasoning: &ir.Reasoning{BudgetTokens: 4000}}
	m = encodeToMap(t, req, "")
	if tc := m["tool_choice"].(map[string]any); tc["type"] != "tool" || tc["name"] != "x" {
		t.Fatalf("%v", tc)
	}
	if _, ok := m["thinking"]; ok {
		t.Fatal("forced tool choice must disable thinking")
	}
}

const upstreamTranscript = `event: message_start
data: {"type":"message_start","message":{"id":"msg_up1","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":12,"cache_creation_input_tokens":100,"cache_read_input_tokens":2000,"output_tokens":1}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}

event: ping
data: {"type": "ping"}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Let me "}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"think."}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"EqQBCkgIARABGAIi"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"I'll run \"ls\"."}}

event: content_block_stop
data: {"type":"content_block_stop","index":1}

event: content_block_start
data: {"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_01A","name":"Bash","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"command\": "}}

event: content_block_delta
data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"\"ls\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":2}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":89}}

event: message_stop
data: {"type":"message_stop"}

`

func decodeSSE(t *testing.T, d ir.StreamDecoder, transcript []byte) []ir.Event {
	t.Helper()
	var evs []ir.Event
	r := sse.NewReader(bytes.NewReader(transcript), 0)
	for {
		e, err := r.Next()
		if err != nil {
			break
		}
		if err := d.Decode(e.Name, e.Data, func(ev ir.Event) { evs = append(evs, ev) }); err != nil {
			t.Fatalf("decode %s: %v", e.Name, err)
		}
	}
	return evs
}

func TestStreamDecode(t *testing.T) {
	evs := decodeSSE(t, NewStreamDecoder(), []byte(upstreamTranscript))
	kinds := []ir.EventKind{}
	for _, e := range evs {
		kinds = append(kinds, e.Kind)
	}
	want := []ir.EventKind{ir.EvStart, ir.EvUsage, ir.EvThinking, ir.EvThinking, ir.EvThinkingSignature, ir.EvText,
		ir.EvToolStart, ir.EvToolArgs, ir.EvToolArgs, ir.EvToolEnd, ir.EvUsage, ir.EvFinish, ir.EvDone}
	if !reflect.DeepEqual(kinds, want) {
		t.Fatalf("kinds %v\nwant %v", kinds, want)
	}
	if evs[0].ID != "msg_up1" || evs[0].Model != "claude-sonnet-4-5" {
		t.Fatalf("start %+v", evs[0])
	}
	if u := evs[1].Usage; u.InputTokens != 2112 || u.CacheReadTokens != 2000 || u.CacheWriteTokens != 100 {
		t.Fatalf("usage %+v", u)
	}
	if evs[4].SigSource != ir.SigAnthropic || evs[4].Signature != "EqQBCkgIARABGAIi" {
		t.Fatalf("sig %+v", evs[4])
	}
	if evs[7].ID != "toolu_01A" || evs[7].Text+evs[8].Text != `{"command": "ls"}` {
		t.Fatalf("args %+v %+v", evs[7], evs[8])
	}
	if evs[11].Finish != ir.FinishToolCalls {
		t.Fatalf("finish %+v", evs[11])
	}

	agg := ir.NewAggregator()
	for _, e := range evs {
		agg.Add(e)
	}
	resp := agg.Response()
	if len(resp.Content) != 3 || resp.Content[0].Signature != "EqQBCkgIARABGAIi" || resp.Content[2].Args != `{"command": "ls"}` {
		t.Fatalf("aggregate %+v", resp.Content)
	}
	if resp.Usage.OutputTokens != 89 || resp.Usage.InputTokens != 2112 {
		t.Fatalf("aggregate usage %+v", resp.Usage)
	}
}

func TestStreamDecodeMisc(t *testing.T) {
	cases := []struct {
		name, event, data string
		want              ir.Event
	}{
		{"max_tokens", "message_delta", `{"type":"message_delta","delta":{"stop_reason":"max_tokens"}}`, ir.Event{Kind: ir.EvFinish, Finish: ir.FinishLength}},
		{"refusal", "message_delta", `{"type":"message_delta","delta":{"stop_reason":"refusal"}}`, ir.Event{Kind: ir.EvFinish, Finish: ir.FinishContentFilter}},
		{"pause", "", `{"type":"message_delta","delta":{"stop_reason":"pause_turn"}}`, ir.Event{Kind: ir.EvFinish, Finish: ir.FinishStop}},
		{"error", "error", `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`, ir.Event{Kind: ir.EvError, ErrType: "overloaded_error", Text: "Overloaded"}},
		{"redacted", "content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"redacted_thinking","data":"XYZ"}}`, ir.Event{Kind: ir.EvRedactedThinking, Signature: "XYZ", SigSource: ir.SigAnthropic}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got []ir.Event
			if err := NewStreamDecoder().Decode([]byte(c.event), []byte(c.data), func(e ir.Event) { got = append(got, e) }); err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 || !reflect.DeepEqual(got[0], c.want) {
				t.Fatalf("got %+v want %+v", got, c.want)
			}
		})
	}
	if err := NewStreamDecoder().Decode([]byte("x"), []byte("{bad"), func(ir.Event) {}); err == nil {
		t.Fatal("expected error on bad json")
	}
}

func TestStreamDecodeCustomTool(t *testing.T) {
	req := &ir.Request{Tools: []ir.Tool{{Name: "apply_patch", Kind: ir.ToolCustom}}}
	tr := "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"t1\",\"name\":\"apply_patch\",\"input\":{}}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"input\\\": \\\"line1\\\\n\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"line2\\\"}\"}}\n\n" +
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n"
	evs := decodeSSE(t, NewStreamDecoderFor(req), []byte(tr))
	if len(evs) != 3 || evs[0].ToolKind != ir.ToolCustom || evs[1].Text != "line1\nline2" || evs[2].Kind != ir.EvToolEnd {
		t.Fatalf("%+v", evs)
	}
}

func encodeAll(enc ir.StreamEncoder, evs []ir.Event) []byte {
	var out []byte
	for _, e := range evs {
		out = enc.Encode(out, e)
	}
	return out
}

// validateSequence checks Messages API SSE framing invariants.
func validateSequence(t *testing.T, stream []byte) []string {
	t.Helper()
	r := sse.NewReader(bytes.NewReader(stream), 0)
	var names []string
	open := -1
	next := 0
	stopped := false
	for {
		e, err := r.Next()
		if err != nil {
			break
		}
		if stopped {
			t.Fatalf("event after message_stop: %s", e.Raw)
		}
		var m map[string]any
		if err := json.Unmarshal(e.Data, &m); err != nil {
			t.Fatalf("invalid json %s: %v", e.Data, err)
		}
		if m["type"] != string(e.Name) {
			t.Fatalf("name %s != type %v", e.Name, m["type"])
		}
		names = append(names, string(e.Name))
		switch string(e.Name) {
		case "content_block_start":
			if open != -1 || int(m["index"].(float64)) != next {
				t.Fatalf("bad start index %v open=%d next=%d", m["index"], open, next)
			}
			open = next
			next++
		case "content_block_delta":
			if int(m["index"].(float64)) != open {
				t.Fatalf("delta on closed block %v", m)
			}
		case "content_block_stop":
			if int(m["index"].(float64)) != open {
				t.Fatalf("stop on wrong block %v", m)
			}
			open = -1
		case "message_stop":
			stopped = true
		}
	}
	return names
}

func TestStreamEncodeReDecode(t *testing.T) {
	src := decodeSSE(t, NewStreamDecoder(), []byte(upstreamTranscript))
	out := encodeAll(NewStreamEncoder(&ir.Request{Model: "client-model"}, ""), src)
	names := validateSequence(t, out)
	if names[0] != "message_start" || names[len(names)-1] != "message_stop" {
		t.Fatalf("names %v", names)
	}
	again := decodeSSE(t, NewStreamDecoder(), out)
	agg1, agg2 := ir.NewAggregator(), ir.NewAggregator()
	for _, e := range src {
		agg1.Add(e)
	}
	for _, e := range again {
		agg2.Add(e)
	}
	r1, r2 := agg1.Response(), agg2.Response()
	if !reflect.DeepEqual(r1.Content, r2.Content) || r1.Finish != r2.Finish || r1.Usage != r2.Usage {
		t.Fatalf("roundtrip mismatch\n%+v\n%+v", r1, r2)
	}
	if r2.ID != "msg_up1" || r2.Model != "client-model" {
		t.Fatalf("id/model %s %s", r2.ID, r2.Model)
	}
	if !bytes.Contains(out, []byte(`"input_tokens":12,"output_tokens":89,"cache_read_input_tokens":2000,"cache_creation_input_tokens":100`)) {
		t.Fatalf("usage in message_delta: %s", out)
	}
}

func TestStreamEncodeEdgeCases(t *testing.T) {
	t.Run("done without start", func(t *testing.T) {
		out := encodeAll(NewStreamEncoder(nil, "m"), []ir.Event{{Kind: ir.EvDone}, {Kind: ir.EvText, Text: "late"}})
		names := validateSequence(t, out)
		if !reflect.DeepEqual(names, []string{"message_start", "message_delta", "message_stop"}) {
			t.Fatalf("%v", names)
		}
		if !bytes.Contains(out, []byte(`"stop_reason":"end_turn"`)) || !bytes.Contains(out, []byte(`"id":"msg_`)) {
			t.Fatalf("%s", out)
		}
	})
	t.Run("text without start, interleaved blocks", func(t *testing.T) {
		evs := []ir.Event{
			{Kind: ir.EvText, Text: "a"},
			{Kind: ir.EvThinking, Text: "t1"},
			{Kind: ir.EvThinkingSignature, Signature: "s1", SigSource: ir.SigAnthropic},
			{Kind: ir.EvThinking, Text: "t2"},
			{Kind: ir.EvThinkingSignature, Signature: "enc", SigSource: ir.SigOpenAI},
			{Kind: ir.EvRedactedThinking, Signature: "R", SigSource: ir.SigAnthropic},
			{Kind: ir.EvToolStart, ID: "c1", Name: "f"},
			{Kind: ir.EvToolArgs, ID: "c1", Text: `{"x":`},
			{Kind: ir.EvToolArgs, ID: "c1", Text: `1}`},
			{Kind: ir.EvToolStart, Name: "g"},
			{Kind: ir.EvToolArgs, Text: `{}`},
			{Kind: ir.EvText, Text: "after"},
			{Kind: ir.EvFinish, Finish: ir.FinishLength},
			{Kind: ir.EvDone},
		}
		out := encodeAll(NewStreamEncoder(nil, "m"), evs)
		validateSequence(t, out)
		got := decodeSSE(t, NewStreamDecoder(), out)
		var kinds []ir.EventKind
		for _, e := range got {
			if e.Kind != ir.EvToolArgs && e.Kind != ir.EvUsage {
				kinds = append(kinds, e.Kind)
			}
		}
		wantKinds := []ir.EventKind{ir.EvStart, ir.EvText, ir.EvThinking, ir.EvThinkingSignature, ir.EvThinking, ir.EvThinkingSignature,
			ir.EvRedactedThinking, ir.EvToolStart, ir.EvToolEnd, ir.EvToolStart, ir.EvToolEnd, ir.EvText, ir.EvFinish, ir.EvDone}
		if !reflect.DeepEqual(kinds, wantKinds) {
			t.Fatalf("kinds %v want %v", kinds, wantKinds)
		}
		if n := bytes.Count(out, []byte(`"content_block":{"type":"thinking"`)); n != 2 {
			t.Fatalf("want 2 thinking blocks, got %d", n)
		}
		agg := ir.NewAggregator()
		for _, e := range got {
			agg.Add(e)
		}
		resp := agg.Response()
		if resp.Finish != ir.FinishLength {
			t.Fatalf("finish %v", resp.Finish)
		}
		n := len(resp.Content)
		if resp.Content[n-1].Type != ir.PartText || resp.Content[n-3].Type != ir.PartToolCall || resp.Content[n-2].Type != ir.PartToolCall {
			t.Fatalf("content %+v", resp.Content)
		}
		if resp.Content[n-3].Args != `{"x":1}` || !strings.HasPrefix(resp.Content[n-2].ToolID, "toolu_") {
			t.Fatalf("tools %+v", resp.Content)
		}
		// Foreign signature tunnels through and is recovered by DecodeRequest.
		if !bytes.Contains(out, []byte(`"signature":"clirouter.openai.enc"`)) {
			t.Fatalf("missing tunnelled sig: %s", out)
		}
	})
	t.Run("custom tool args wrapped", func(t *testing.T) {
		evs := []ir.Event{
			{Kind: ir.EvStart, ID: "msg_1"},
			{Kind: ir.EvToolStart, ID: "c1", Name: "apply_patch", ToolKind: ir.ToolCustom},
			{Kind: ir.EvToolArgs, ID: "c1", Text: "*** Begin\n"},
			{Kind: ir.EvToolArgs, ID: "c1", Text: "\"quoted\""},
			{Kind: ir.EvToolEnd, ID: "c1"},
			{Kind: ir.EvDone},
		}
		out := encodeAll(NewStreamEncoder(nil, "m"), evs)
		validateSequence(t, out)
		got := decodeSSE(t, NewStreamDecoder(), out)
		agg := ir.NewAggregator()
		for _, e := range got {
			agg.Add(e)
		}
		var in struct{ Input string }
		args := agg.Response().Content[0].Args
		if err := json.Unmarshal([]byte(args), &in); err != nil || in.Input != "*** Begin\n\"quoted\"" {
			t.Fatalf("args %q %v", args, err)
		}
		if !bytes.Contains(out, []byte(`"stop_reason":"tool_use"`)) {
			t.Fatal("stop reason should default to tool_use")
		}
	})
	t.Run("error mid-stream", func(t *testing.T) {
		out := encodeAll(NewStreamEncoder(nil, "m"), []ir.Event{
			{Kind: ir.EvText, Text: "partial"},
			{Kind: ir.EvError, ErrType: "overloaded_error", Text: "boom"},
			{Kind: ir.EvText, Text: "nope"},
			{Kind: ir.EvDone},
		})
		names := validateSequence(t, out)
		if !reflect.DeepEqual(names, []string{"message_start", "content_block_start", "content_block_delta", "content_block_stop", "error"}) {
			t.Fatalf("%v", names)
		}
		typ, msg, ok := IsErrorEvent([]byte("error"), bytes.TrimSuffix(bytes.SplitN(out[bytes.LastIndex(out, []byte("event: error")):], []byte("data: "), 2)[1], []byte("\n\n")))
		if !ok || typ != "overloaded_error" || msg != "boom" {
			t.Fatalf("%s %s %v", typ, msg, ok)
		}
	})
}

func TestDecodeForeignSignatureRoundTrip(t *testing.T) {
	body := `{"model":"m","max_tokens":10,"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"x","signature":"clirouter.openai.enc"},{"type":"redacted_thinking","data":"clirouter.gemini.g1"}]}]}`
	req, err := DecodeRequest([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	p := req.Messages[0].Parts
	if p[0].SigSource != ir.SigOpenAI || p[0].Signature != "enc" || p[1].SigSource != ir.SigGemini || p[1].Signature != "g1" {
		t.Fatalf("%+v", p)
	}
}

func TestDecodeRequestVariants(t *testing.T) {
	cases := []struct {
		name string
		body string
		chk  func(*ir.Request) bool
	}{
		{"string system", `{"model":"m","system":"sys","messages":[{"role":"user","content":"hi"}]}`,
			func(r *ir.Request) bool { return r.System[0].Text == "sys" && r.Messages[0].Parts[0].Text == "hi" }},
		{"tool choice any", `{"model":"m","messages":[],"tool_choice":{"type":"any"}}`,
			func(r *ir.Request) bool { return r.ToolChoice.Mode == "required" }},
		{"tool choice tool", `{"model":"m","messages":[],"tool_choice":{"type":"tool","name":"x"}}`,
			func(r *ir.Request) bool { return r.ToolChoice.Mode == "tool" && r.ToolChoice.Name == "x" }},
		{"thinking disabled", `{"model":"m","messages":[],"thinking":{"type":"disabled"}}`,
			func(r *ir.Request) bool { return r.Reasoning.Disabled }},
		{"thinking adaptive", `{"model":"m","messages":[],"thinking":{"type":"adaptive"},"output_config":{"effort":"medium"}}`,
			func(r *ir.Request) bool { return r.Reasoning.Effort == "medium" }},
		{"image url + pdf", `{"model":"m","messages":[{"role":"user","content":[{"type":"image","source":{"type":"url","url":"https://x/y.png"}},{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"JVBER"}}]}]}`,
			func(r *ir.Request) bool {
				p := r.Messages[0].Parts
				return p[0].URL == "https://x/y.png" && p[1].Type == ir.PartFile && p[1].MediaType == "application/pdf"
			}},
		{"stop/top", `{"model":"m","messages":[],"stop_sequences":["X"],"top_k":5,"top_p":0.9,"unknown":{"a":1}}`,
			func(r *ir.Request) bool { return r.Stop[0] == "X" && *r.TopK == 5 && *r.TopP == 0.9 }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, err := DecodeRequest([]byte(c.body))
			if err != nil {
				t.Fatal(err)
			}
			if !c.chk(r) {
				t.Fatalf("check failed: %+v", r)
			}
		})
	}
	if _, err := DecodeRequest([]byte(`{"messages":[{"role":"system","content":"x"}]}`)); err == nil {
		t.Fatal("expected error for bad role")
	}
	if _, err := DecodeRequest([]byte(`{`)); err == nil {
		t.Fatal("expected error for bad json")
	}
}

func TestEncodeResponse(t *testing.T) {
	resp := &ir.Response{
		Model: "up",
		Content: []ir.Part{
			{Type: ir.PartThinking, Text: "hmm", Signature: "S", SigSource: ir.SigAnthropic},
			{Type: ir.PartText, Text: "hi <b>"},
			{Type: ir.PartToolCall, ToolID: "t1", ToolName: "f", Args: `{"a":1}`},
		},
		Usage: ir.Usage{InputTokens: 110, OutputTokens: 5, CacheReadTokens: 100},
	}
	b, err := EncodeResponse(&ir.Request{Model: "client"}, resp)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m["type"] != "message" || m["model"] != "client" || m["stop_reason"] != "tool_use" || m["stop_sequence"] != nil {
		t.Fatalf("%s", b)
	}
	if !strings.HasPrefix(m["id"].(string), "msg_") || !bytes.Contains(b, []byte("<b>")) {
		t.Fatalf("%s", b)
	}
	u := m["usage"].(map[string]any)
	if u["input_tokens"].(float64) != 10 || u["cache_read_input_tokens"].(float64) != 100 {
		t.Fatalf("usage %v", u)
	}
	c := m["content"].([]any)
	if len(c) != 3 || c[2].(map[string]any)["input"].(map[string]any)["a"].(float64) != 1 {
		t.Fatalf("content %v", c)
	}
}

func TestErrorsAndHelpers(t *testing.T) {
	statuses := map[int]string{400: "invalid_request_error", 401: "authentication_error", 403: "permission_error", 404: "not_found_error", 413: "request_too_large", 429: "rate_limit_error", 529: "overloaded_error", 500: "api_error", 502: "api_error"}
	for st, typ := range statuses {
		var m struct {
			Type  string
			Error wireError
		}
		if err := json.Unmarshal(ErrorBody(st, "", "msg \"q\""), &m); err != nil {
			t.Fatal(err)
		}
		if m.Type != "error" || m.Error.Type != typ || m.Error.Message != "msg \"q\"" {
			t.Fatalf("%d: %+v", st, m)
		}
	}
	if !bytes.Contains(ErrorBody(500, "custom_type", "x"), []byte(`"custom_type"`)) {
		t.Fatal("explicit errType ignored")
	}
	frame := StreamErrorEvent("overloaded_error", "busy")
	if !bytes.HasPrefix(frame, []byte("event: error\ndata: {")) || !bytes.HasSuffix(frame, []byte("}\n\n")) {
		t.Fatalf("%q", frame)
	}
	if string(Ping()) != "event: ping\ndata: {\"type\":\"ping\"}\n\n" {
		t.Fatal("ping")
	}

	u, ok := UsageFromEvent([]byte("message_start"), []byte(`{"type":"message_start","message":{"usage":{"input_tokens":5,"cache_read_input_tokens":10,"output_tokens":1}}}`))
	if !ok || u.InputTokens != 15 || u.CacheReadTokens != 10 {
		t.Fatalf("%+v %v", u, ok)
	}
	u, ok = UsageFromEvent(nil, []byte(`{"type":"message_delta","delta":{},"usage":{"output_tokens":42}}`))
	if !ok || u.OutputTokens != 42 {
		t.Fatalf("%+v %v", u, ok)
	}
	if _, ok := UsageFromEvent([]byte("content_block_delta"), []byte(`{"usage":1}`)); ok {
		t.Fatal("usage from delta")
	}

	if _, _, ok := IsErrorEvent([]byte("content_block_delta"), []byte(`{"type":"error"}`)); ok {
		t.Fatal("false positive")
	}
	if typ, msg, ok := IsErrorEvent(nil, []byte(`{"type":"error","error":{"type":"api_error","message":"m"}}`)); !ok || typ != "api_error" || msg != "m" {
		t.Fatal("nameless error")
	}
	if _, _, ok := IsErrorEvent(nil, []byte(`{"type":"content_block_delta","delta":{"text":"\"type\":\"error\""}}`)); ok {
		t.Fatal("false positive in text")
	}
	if !IsTerminal([]byte("message_stop"), nil) || IsTerminal([]byte("message_delta"), []byte(`{"type":"message_stop"}`)) || !IsTerminal(nil, []byte(`{"type":"message_stop"}`)) {
		t.Fatal("IsTerminal")
	}
}

func BenchmarkStreamEncodeText(b *testing.B) {
	enc := NewStreamEncoder(nil, "m")
	buf := make([]byte, 0, 4096)
	buf = enc.Encode(buf, ir.Event{Kind: ir.EvStart})
	ev := ir.Event{Kind: ir.EvText, Text: "Hello, \"world\"! This is a typical token chunk.\n"}
	b.ReportAllocs()
	for b.Loop() {
		buf = enc.Encode(buf[:0], ev)
	}
}

func BenchmarkStreamDecodeText(b *testing.B) {
	d := NewStreamDecoder()
	name := []byte("content_block_delta")
	data := []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello, world chunk"}}`)
	emit := func(ir.Event) {}
	b.ReportAllocs()
	for b.Loop() {
		_ = d.Decode(name, data, emit)
	}
}
