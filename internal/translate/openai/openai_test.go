package openai

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Project-by-Myself/clirouter/internal/ir"
	"github.com/Project-by-Myself/clirouter/internal/sse"
)

func TestDecodeRequestMultiTurn(t *testing.T) {
	body := `{
	 "model":"gpt-5","stream":true,"stream_options":{"include_usage":true},
	 "max_tokens":100,"max_completion_tokens":2000,"temperature":0.2,"stop":"END",
	 "reasoning_effort":"high","parallel_tool_calls":false,"user":"u1","prompt_cache_key":"k",
	 "response_format":{"type":"json_object"},
	 "tool_choice":{"type":"function","function":{"name":"read"}},
	 "tools":[
	  {"type":"function","function":{"name":"read","description":"Read a file","parameters":{"type":"object"},"strict":true}},
	  {"type":"web_search"}
	 ],
	 "messages":[
	  {"role":"system","content":"sys1"},
	  {"role":"developer","content":[{"type":"text","text":"dev2"}]},
	  {"role":"user","content":[
	    {"type":"text","text":"look"},
	    {"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA","detail":"high"}},
	    {"type":"image_url","image_url":{"url":"https://x/y.jpg"}},
	    {"type":"file","file":{"file_data":"data:application/pdf;base64,UERG","filename":"a.pdf"}},
	    {"type":"input_audio","input_audio":{"data":"x","format":"wav"}}
	  ]},
	  {"role":"assistant","content":null,"reasoning_content":"hmm","tool_calls":[
	    {"id":"call_1","type":"function","function":{"name":"read","arguments":"{\"p\":1}"}}],
	   "function_call":{"name":"legacy","arguments":"{}"}},
	  {"role":"tool","tool_call_id":"call_1","content":[{"type":"text","text":"file body"}]},
	  {"role":"assistant","content":[{"type":"text","text":"done"},{"type":"refusal","refusal":"no"}]}
	 ]}`
	req, err := DecodeRequest([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if req.MaxTokens != 2000 || *req.Temperature != 0.2 || !reflect.DeepEqual(req.Stop, []string{"END"}) ||
		!req.Stream || !req.IncludeUsage || req.Reasoning.Effort != "high" || *req.ParallelToolCalls ||
		req.User != "u1" || req.PromptCacheKey != "k" || string(req.ResponseFormat) != `{"type":"json_object"}` {
		t.Fatalf("scalars: %+v", req)
	}
	if len(req.Tools) != 1 || req.Tools[0].Name != "read" || string(req.Tools[0].Schema) != `{"type":"object"}` {
		t.Fatalf("tools: %+v", req.Tools)
	}
	if *req.ToolChoice != (ir.ToolChoice{Mode: "tool", Name: "read"}) {
		t.Fatalf("tool choice %+v", req.ToolChoice)
	}
	if len(req.System) != 2 || req.System[0].Text != "sys1" || req.System[1].Text != "dev2" {
		t.Fatalf("system %+v", req.System)
	}
	if len(req.Messages) != 4 {
		t.Fatalf("messages %d", len(req.Messages))
	}
	u := req.Messages[0].Parts
	want := []ir.Part{
		{Type: ir.PartText, Text: "look"},
		{Type: ir.PartImage, MediaType: "image/png", Data: "AAAA"},
		{Type: ir.PartImage, URL: "https://x/y.jpg"},
		{Type: ir.PartFile, MediaType: "application/pdf", Data: "UERG"},
	}
	if !reflect.DeepEqual(u, want) {
		t.Fatalf("user parts %+v", u)
	}
	a := req.Messages[1]
	if a.Role != ir.RoleAssistant || len(a.Parts) != 2 || !reflect.DeepEqual(a.Parts[0], ir.Part{Type: ir.PartThinking, Text: "hmm"}) ||
		a.Parts[1].ToolID != "call_1" || a.Parts[1].Args != `{"p":1}` || a.Parts[1].ToolName != "read" {
		t.Fatalf("assistant %+v", a)
	}
	tm := req.Messages[2]
	if tm.Role != ir.RoleTool || tm.Parts[0].Type != ir.PartToolResult || tm.Parts[0].ToolID != "call_1" || tm.Parts[0].Result[0].Text != "file body" {
		t.Fatalf("tool %+v", tm)
	}
	if p := req.Messages[3].Parts; len(p) != 2 || p[1].Text != "no" {
		t.Fatalf("refusal %+v", p)
	}
}

func TestDecodeRequestVariants(t *testing.T) {
	cases := []struct {
		body  string
		check func(*ir.Request) bool
	}{
		{`{"messages":[],"stop":["a","b"],"max_tokens":5}`, func(r *ir.Request) bool { return len(r.Stop) == 2 && r.MaxTokens == 5 }},
		{`{"messages":[],"reasoning":{"effort":"low"}}`, func(r *ir.Request) bool { return r.Reasoning.Effort == "low" }},
		{`{"messages":[],"reasoning":{"max_tokens":3000}}`, func(r *ir.Request) bool { return r.Reasoning.BudgetTokens == 3000 }},
		{`{"messages":[],"reasoning_effort":"none"}`, func(r *ir.Request) bool { return r.Reasoning.Disabled }},
		{`{"messages":[],"tool_choice":"required"}`, func(r *ir.Request) bool { return r.ToolChoice.Mode == "required" }},
		{`{"messages":[{"role":"assistant","reasoning":"r","content":"c"}]}`, func(r *ir.Request) bool {
			p := r.Messages[0].Parts
			return len(p) == 2 && p[0].Type == ir.PartThinking && p[0].Text == "r" && p[1].Text == "c"
		}},
		{`{"messages":[{"role":"tool","tool_call_id":"x","content":"plain"}]}`, func(r *ir.Request) bool {
			return r.Messages[0].Parts[0].Result[0].Text == "plain"
		}},
	}
	for i, c := range cases {
		r, err := DecodeRequest([]byte(c.body))
		if err != nil || !c.check(r) {
			t.Errorf("case %d: err=%v req=%+v", i, err, r)
		}
	}
	if _, err := DecodeRequest([]byte(`{"messages":[],"tool_choice":"bogus"}`)); err == nil {
		t.Error("expected tool_choice error")
	}
}

func sampleIR() *ir.Request {
	temp := 0.5
	ptc := true
	return &ir.Request{
		Model:  "client-model",
		System: []ir.Part{{Type: ir.PartText, Text: "a"}, {Type: ir.PartText, Text: "b"}},
		Messages: []ir.Message{
			{Role: ir.RoleUser, Parts: []ir.Part{{Type: ir.PartText, Text: "hi"}}},
			{Role: ir.RoleUser, Parts: []ir.Part{
				{Type: ir.PartText, Text: "see"},
				{Type: ir.PartImage, MediaType: "image/png", Data: "AAAA"},
				{Type: ir.PartFile, MediaType: "application/pdf", Data: "UERG"},
			}},
			{Role: ir.RoleAssistant, Parts: []ir.Part{
				{Type: ir.PartThinking, Text: "secret", Signature: "sig"},
				{Type: ir.PartToolCall, ToolID: "c1", ToolName: "read"},
				{Type: ir.PartToolCall, ToolID: "c2", ToolName: "apply_patch", ToolKind: ir.ToolCustom, Args: "*** Begin \"Patch\""},
			}},
			{Role: ir.RoleTool, Parts: []ir.Part{
				{Type: ir.PartToolResult, ToolID: "c1", Result: []ir.Part{{Type: ir.PartText, Text: "x"}, {Type: ir.PartImage, Data: "ZZ"}}},
				{Type: ir.PartToolResult, ToolID: "c2", Result: []ir.Part{{Type: ir.PartText, Text: "ok"}}},
			}},
		},
		Tools: []ir.Tool{
			{Name: "read", Schema: json.RawMessage(`{"type":"object","properties":{"p":{"type":"string"}}}`)},
			{Name: "apply_patch", Kind: ir.ToolCustom},
		},
		ToolChoice:        &ir.ToolChoice{Mode: "tool", Name: "read"},
		MaxTokens:         1000,
		Temperature:       &temp,
		Reasoning:         &ir.Reasoning{BudgetTokens: 8000},
		ParallelToolCalls: &ptc,
		PromptCacheKey:    "pck",
	}
}

func TestEncodeRequestProviders(t *testing.T) {
	for _, pt := range []string{"openai", "openai-compatible", "deepseek"} {
		b, err := EncodeRequest(sampleIR(), ir.EncodeOptions{Model: "up", ProviderType: pt})
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		if m["model"] != "up" || m["stream"] != true || m["stream_options"].(map[string]any)["include_usage"] != true {
			t.Fatalf("%s: base %s", pt, b)
		}
		_, hasMCT := m["max_completion_tokens"]
		_, hasMT := m["max_tokens"]
		_, hasPCK := m["prompt_cache_key"]
		effort, _ := m["reasoning_effort"].(string)
		switch pt {
		case "openai":
			if !hasMCT || hasMT || !hasPCK || effort != "medium" {
				t.Fatalf("openai: %s", b)
			}
		case "openai-compatible":
			if hasMCT || !hasMT || hasPCK || effort != "medium" {
				t.Fatalf("compat: %s", b)
			}
		case "deepseek":
			if effort != "" || !hasMT {
				t.Fatalf("deepseek: %s", b)
			}
		}
		s := string(b)
		for _, want := range []string{
			`{"role":"system","content":"a\n\nb"}`,
			`{"role":"user","content":"hi"}`,
			`{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}`,
			`{"type":"file","file":{"file_data":"data:application/pdf;base64,UERG","filename":"file.pdf"}}`,
			`{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"read","arguments":"{}"}}`,
			`"arguments":"{\"input\":\"*** Begin \\\"Patch\\\"\"}"`,
			`{"role":"tool","content":"x\n[image omitted]","tool_call_id":"c1"}`,
			`{"role":"tool","content":"ok","tool_call_id":"c2"}`,
			`"parameters":{"type":"object","properties":{"input":{"type":"string"}},"required":["input"]}`,
			`"tool_choice":{"function":{"name":"read"},"type":"function"}`,
			`"parallel_tool_calls":true`,
		} {
			if !strings.Contains(s, want) {
				t.Errorf("%s: missing %s in\n%s", pt, want, s)
			}
		}
		if strings.Contains(s, "secret") || strings.Contains(s, "reasoning_content") {
			t.Errorf("%s: thinking leaked: %s", pt, s)
		}
	}
}

func TestEncodeRequestReasoning(t *testing.T) {
	for _, c := range []struct {
		r    *ir.Reasoning
		want string
	}{
		{&ir.Reasoning{Effort: "minimal"}, "minimal"},
		{&ir.Reasoning{BudgetTokens: 1024}, "low"},
		{&ir.Reasoning{BudgetTokens: 4096}, "low"},
		{&ir.Reasoning{BudgetTokens: 16384}, "medium"},
		{&ir.Reasoning{BudgetTokens: 32000}, "high"},
		{&ir.Reasoning{Effort: "high", Disabled: true}, ""},
		{nil, ""},
	} {
		b, _ := EncodeRequest(&ir.Request{Reasoning: c.r}, ir.EncodeOptions{Model: "m", ProviderType: "openai"})
		var m struct {
			Effort string `json:"reasoning_effort"`
		}
		json.Unmarshal(b, &m)
		if m.Effort != c.want {
			t.Errorf("%+v: got %q want %q", c.r, m.Effort, c.want)
		}
	}
	// No tools: parallel_tool_calls/tool_choice must be omitted.
	p := true
	b, _ := EncodeRequest(&ir.Request{ParallelToolCalls: &p, ToolChoice: &ir.ToolChoice{Mode: "auto"}}, ir.EncodeOptions{Model: "m"})
	if strings.Contains(string(b), "parallel_tool_calls") || strings.Contains(string(b), "tool_choice") {
		t.Errorf("unexpected: %s", b)
	}
}

func TestRequestRoundTrip(t *testing.T) {
	b, err := EncodeRequest(sampleIR(), ir.EncodeOptions{Model: "up", ProviderType: "openai"})
	if err != nil {
		t.Fatal(err)
	}
	r, err := DecodeRequest(b)
	if err != nil {
		t.Fatal(err)
	}
	if r.Model != "up" || r.MaxTokens != 1000 || len(r.Messages) != 5 || r.Reasoning.Effort != "medium" || len(r.Tools) != 2 {
		t.Fatalf("%+v", r)
	}
	if got := r.Messages[2].Parts[1].Args; got != `{"input":"*** Begin \"Patch\""}` {
		t.Fatalf("custom args %q", got)
	}
}

// ---- stream decode ----

func decodeAll(t *testing.T, frames ...string) []ir.Event {
	t.Helper()
	d := NewStreamDecoder()
	var evs []ir.Event
	for _, f := range frames {
		if err := d.Decode(nil, []byte(f), func(e ir.Event) { evs = append(evs, e) }); err != nil {
			t.Fatalf("decode %s: %v", f, err)
		}
	}
	return evs
}

func aggregate(evs []ir.Event) *ir.Response {
	a := ir.NewAggregator()
	for _, e := range evs {
		a.Add(e)
	}
	return a.Response()
}

func kinds(evs []ir.Event) []ir.EventKind {
	k := make([]ir.EventKind, len(evs))
	for i, e := range evs {
		k[i] = e.Kind
	}
	return k
}

func TestStreamDecodeInterleavedTools(t *testing.T) {
	evs := decodeAll(t,
		`{"id":"chatcmpl-1","model":"gpt-x","choices":[{"index":0,"delta":{"role":"assistant","content":"Let me"},"finish_reason":null}]}`,
		`{"id":"chatcmpl-1","choices":[{"index":0,"delta":{"content":" check."}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"read","arguments":""}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"call_b","type":"function","function":{"name":"grep","arguments":"{\"q\":"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_a","function":{"arguments":"{\"p\":"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"function":{"arguments":"\"x\"}"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"1}"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":null}`,
		`{"choices":[],"usage":{"prompt_tokens":100,"completion_tokens":20,"prompt_tokens_details":{"cached_tokens":64},"completion_tokens_details":{"reasoning_tokens":5}}}`,
		`[DONE]`,
	)
	if evs[0].Kind != ir.EvStart || evs[0].ID != "chatcmpl-1" || evs[0].Model != "gpt-x" {
		t.Fatalf("start %+v", evs[0])
	}
	r := aggregate(evs)
	if len(r.Content) != 3 || r.Content[0].Text != "Let me check." ||
		r.Content[1].ToolID != "call_a" || r.Content[1].Args != `{"p":1}` ||
		r.Content[2].ToolID != "call_b" || r.Content[2].Args != `{"q":"x"}` || r.Content[2].ToolName != "grep" {
		t.Fatalf("content %+v", r.Content)
	}
	if r.Finish != ir.FinishToolCalls || r.Usage != (ir.Usage{InputTokens: 100, OutputTokens: 20, CacheReadTokens: 64, ReasoningTokens: 5}) {
		t.Fatalf("finish/usage %+v %+v", r.Finish, r.Usage)
	}
	k := kinds(evs)
	if k[len(k)-1] != ir.EvDone {
		t.Fatal("no done")
	}
	ends := 0
	for i, e := range evs {
		if e.Kind == ir.EvToolEnd {
			ends++
			if evs[i+1].Kind != ir.EvToolEnd && evs[i+1].Kind != ir.EvFinish {
				t.Fatal("tool end not right before finish")
			}
		}
	}
	if ends != 2 {
		t.Fatalf("ends %d", ends)
	}
}

func TestStreamDecodeDeepSeekReasoningAndCombinedFinishUsage(t *testing.T) {
	evs := decodeAll(t,
		`{"id":"x","model":"deepseek-reasoner","choices":[{"index":0,"delta":{"role":"assistant","content":null,"reasoning_content":"Think"}}]}`,
		`{"choices":[{"index":0,"delta":{"content":null,"reasoning_content":"ing."}}]}`,
		`{"choices":[{"index":0,"delta":{"content":"Answer","reasoning_content":null}}]}`,
		`{"choices":[{"index":0,"delta":{"content":""},"finish_reason":"length"}],"usage":{"prompt_tokens":10,"completion_tokens":7,"prompt_cache_hit_tokens":8,"prompt_cache_miss_tokens":2}}`,
		`[DONE]`,
		`{"choices":[{"index":0,"delta":{"content":"late"}}]}`,
	)
	r := aggregate(evs)
	if len(r.Content) != 2 || r.Content[0].Type != ir.PartThinking || r.Content[0].Text != "Thinking." || r.Content[1].Text != "Answer" {
		t.Fatalf("%+v", r.Content)
	}
	if r.Finish != ir.FinishLength || r.Usage.CacheReadTokens != 8 || r.Usage.InputTokens != 10 {
		t.Fatalf("%+v", r)
	}
	k := kinds(evs)
	if !reflect.DeepEqual(k[len(k)-3:], []ir.EventKind{ir.EvUsage, ir.EvFinish, ir.EvDone}) {
		t.Fatalf("kinds %v", k)
	}
}

func TestStreamDecodeSingleChunkArgsAndQuirks(t *testing.T) {
	evs := decodeAll(t,
		// all at once, no index, stop finish reason despite tool call
		`{"id":"o","model":"llama","choices":[{"index":0,"delta":{"tool_calls":[{"id":"call_z","function":{"name":"ls","arguments":{"dir":"."}}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"function":{"name":"pwd","arguments":"{}"}}]},"finish_reason":"stop"}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`[DONE]`,
	)
	r := aggregate(evs)
	if len(r.Content) != 2 || r.Content[0].Args != `{"dir":"."}` || r.Content[0].ToolID != "call_z" ||
		!strings.HasPrefix(r.Content[1].ToolID, "call_") || r.Content[1].Args != "{}" {
		t.Fatalf("%+v", r.Content)
	}
	if r.Finish != ir.FinishToolCalls {
		t.Fatalf("finish %s", r.Finish)
	}
	n := 0
	for _, e := range evs {
		if e.Kind == ir.EvFinish {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("finish count %d", n)
	}
}

func TestStreamDecodeSameIndexDifferentIDs(t *testing.T) {
	evs := decodeAll(t,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"a","function":{"name":"f","arguments":"{}"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"a","function":{"arguments":""}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"b","function":{"name":"g","arguments":"{}"}}]}}]}`,
		`[DONE]`,
	)
	r := aggregate(evs)
	if len(r.Content) != 2 || r.Content[0].ToolID != "a" || r.Content[1].ToolID != "b" || r.Finish != ir.FinishToolCalls {
		t.Fatalf("%+v", r)
	}
}

func TestStreamDecodeErrors(t *testing.T) {
	evs := decodeAll(t, `{"error":{"message":"Rate limited","type":"rate_limit_error","code":"rate_limit_exceeded"}}`)
	if len(evs) != 1 || evs[0].Kind != ir.EvError || evs[0].Text != "Rate limited" || evs[0].ErrType != "rate_limit_error" {
		t.Fatalf("%+v", evs)
	}
	evs = decodeAll(t, `{"error":{"message":"boom","code":502}}`)
	if evs[0].ErrType != "server_error" {
		t.Fatalf("%+v", evs)
	}
	evs = decodeAll(t, `{"error":"plain"}`)
	if evs[0].Text != "plain" {
		t.Fatalf("%+v", evs)
	}
	if err := NewStreamDecoder().Decode(nil, []byte(`{bad`), func(ir.Event) {}); err == nil {
		t.Fatal("expected error")
	}
	if typ, msg, ok := IsErrorEvent(nil, []byte(`{"error":{"message":"m","type":"t"}}`)); !ok || typ != "t" || msg != "m" {
		t.Fatal("IsErrorEvent")
	}
	if _, _, ok := IsErrorEvent(nil, []byte(`{"choices":[{"delta":{"content":"\"error\""}}],"error":null}`)); ok {
		t.Fatal("false positive")
	}
	if !IsTerminal(nil, []byte(" [DONE]\n")) || IsTerminal(nil, []byte(`{}`)) {
		t.Fatal("IsTerminal")
	}
	if u, ok := UsageFromEvent(nil, []byte(`{"choices":[],"usage":{"prompt_tokens":3,"completion_tokens":4,"cached_tokens":2}}`)); !ok || *u != (ir.Usage{InputTokens: 3, OutputTokens: 4, CacheReadTokens: 2}) {
		t.Fatalf("usage %+v", u)
	}
	if _, ok := UsageFromEvent(nil, []byte(`{"choices":[],"usage":null}`)); ok {
		t.Fatal("null usage")
	}
}

// ---- stream encode ----

func readFrames(t *testing.T, b []byte) []string {
	t.Helper()
	r := sse.NewReader(strings.NewReader(string(b)), 0)
	var out []string
	for {
		ev, err := r.Next()
		if err != nil {
			break
		}
		out = append(out, string(ev.Data))
	}
	return out
}

func TestStreamEncodeRoundTrip(t *testing.T) {
	req := &ir.Request{Model: "client", IncludeUsage: true}
	e := NewStreamEncoder(req, "client")
	weird := "q\"\\\n\t\x01   \xff é 😀 </script>"
	src := []ir.Event{
		{Kind: ir.EvStart, ID: "msg_1", Model: "up"},
		{Kind: ir.EvThinking, Text: "think"},
		{Kind: ir.EvThinkingSignature, Signature: "sig"},
		{Kind: ir.EvText, Text: weird},
		{Kind: ir.EvToolStart, ID: "t1", Name: "read"},
		{Kind: ir.EvToolArgs, ID: "t1", Text: `{"p":`},
		{Kind: ir.EvToolArgs, ID: "t1", Text: `1}`},
		{Kind: ir.EvToolEnd, ID: "t1"},
		{Kind: ir.EvToolStart, ID: "t2", Name: "apply_patch", ToolKind: ir.ToolCustom},
		{Kind: ir.EvToolArgs, ID: "t2", Text: "*** Begin\n"},
		{Kind: ir.EvToolArgs, ID: "t2", Text: `"x"`},
		{Kind: ir.EvToolEnd, ID: "t2"},
		{Kind: ir.EvUsage, Usage: &ir.Usage{InputTokens: 50, CacheReadTokens: 10}},
		{Kind: ir.EvUsage, Usage: &ir.Usage{OutputTokens: 9, ReasoningTokens: 3}},
		{Kind: ir.EvFinish, Finish: ir.FinishStop},
		{Kind: ir.EvDone},
		{Kind: ir.EvText, Text: "after done"},
	}
	var out []byte
	for _, ev := range src {
		out = e.Encode(out, ev)
	}
	frames := readFrames(t, out)
	for _, f := range frames[:len(frames)-1] {
		if !json.Valid([]byte(f)) {
			t.Fatalf("invalid json %s", f)
		}
	}
	if frames[len(frames)-1] != "[DONE]" || strings.Contains(string(out), "after done") {
		t.Fatalf("tail %q", frames[len(frames)-1])
	}
	if !strings.Contains(frames[0], `"delta":{"role":"assistant","content":""}`) || !strings.Contains(frames[0], `"id":"msg_1"`) ||
		!strings.Contains(frames[0], `"model":"client"`) || !strings.Contains(frames[0], `"object":"chat.completion.chunk"`) {
		t.Fatalf("first %s", frames[0])
	}
	if !strings.Contains(string(out), "\\u2028") {
		t.Fatal("U+2028 not escaped")
	}
	usageFrame := frames[len(frames)-2]
	if !strings.Contains(usageFrame, `"choices":[],"usage":{"prompt_tokens":50,"completion_tokens":9,"total_tokens":59,"prompt_tokens_details":{"cached_tokens":10},"completion_tokens_details":{"reasoning_tokens":3}}`) {
		t.Fatalf("usage %s", usageFrame)
	}
	if !strings.Contains(frames[len(frames)-3], `"finish_reason":"tool_calls"`) {
		t.Fatalf("finish %s", frames[len(frames)-3])
	}

	evs := decodeAll(t, frames...)
	r := aggregate(evs)
	wantText := strings.ToValidUTF8(weird, "�")
	if len(r.Content) != 4 || r.Content[0].Text != "think" || r.Content[1].Text != wantText ||
		r.Content[2].Args != `{"p":1}` || r.Content[2].ToolID != "t1" ||
		r.Content[3].ToolName != "apply_patch" || r.Content[3].Args != `{"input":"*** Begin\n\"x\""}` {
		t.Fatalf("roundtrip %+v", r.Content)
	}
	if r.Finish != ir.FinishToolCalls || r.Usage != (ir.Usage{InputTokens: 50, OutputTokens: 9, CacheReadTokens: 10, ReasoningTokens: 3}) {
		t.Fatalf("%+v", r)
	}
}

func TestStreamEncodeNoUsageAndError(t *testing.T) {
	e := NewStreamEncoder(&ir.Request{}, "m")
	out := e.Encode(nil, ir.Event{Kind: ir.EvText, Text: "hi"})
	out = e.Encode(out, ir.Event{Kind: ir.EvUsage, Usage: &ir.Usage{InputTokens: 1}})
	out = e.Encode(out, ir.Event{Kind: ir.EvDone})
	if strings.Contains(string(out), `"usage"`) || !strings.Contains(string(out), `"finish_reason":"stop"`) {
		t.Fatalf("%s", out)
	}
	e = NewStreamEncoder(&ir.Request{}, "m")
	out = e.Encode(nil, ir.Event{Kind: ir.EvText, Text: "x"})
	out = e.Encode(out, ir.Event{Kind: ir.EvError, ErrType: "overloaded_error", Text: "busy"})
	out = e.Encode(out, ir.Event{Kind: ir.EvDone})
	frames := readFrames(t, out)
	if len(frames) != 4 || frames[3] != "[DONE]" || frames[2] != `{"error":{"message":"busy","type":"overloaded_error","code":"overloaded_error"}}` {
		t.Fatalf("%q", frames)
	}
	typ, msg, ok := IsErrorEvent(nil, []byte(frames[2]))
	if !ok || typ != "overloaded_error" || msg != "busy" {
		t.Fatal("error frame not recognized")
	}
}

func TestEncodeResponse(t *testing.T) {
	resp := &ir.Response{ID: "r1", Model: "up", Finish: ir.FinishStop, Usage: ir.Usage{InputTokens: 5, OutputTokens: 6, CacheReadTokens: 1}, Content: []ir.Part{
		{Type: ir.PartThinking, Text: "t"},
		{Type: ir.PartText, Text: "hello <b>"},
		{Type: ir.PartToolCall, ToolID: "c", ToolName: "f"},
		{Type: ir.PartToolCall, ToolID: "d", ToolName: "g", ToolKind: ir.ToolCustom, Args: "raw"},
	}}
	b, err := EncodeResponse(&ir.Request{Model: "client"}, resp)
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		ID, Object, Model string
		Choices           []struct {
			Message struct {
				Role             string
				Content          *string
				ReasoningContent string        `json:"reasoning_content"`
				ToolCalls        []outToolCall `json:"tool_calls"`
			}
			FinishReason string `json:"finish_reason"`
		}
		Usage respUsage
	}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	c := m.Choices[0]
	if m.ID != "r1" || m.Object != "chat.completion" || m.Model != "client" || *c.Message.Content != "hello <b>" ||
		c.Message.ReasoningContent != "t" || len(c.Message.ToolCalls) != 2 || c.Message.ToolCalls[0].Function.Arguments != "{}" ||
		c.Message.ToolCalls[1].Function.Arguments != `{"input":"raw"}` || c.FinishReason != "tool_calls" ||
		m.Usage.TotalTokens != 11 || m.Usage.PromptTokensDetails.CachedTokens != 1 {
		t.Fatalf("%s", b)
	}
	b, _ = EncodeResponse(&ir.Request{}, &ir.Response{Content: []ir.Part{{Type: ir.PartToolCall, ToolID: "c", ToolName: "f", Args: "{}"}}})
	if !strings.Contains(string(b), `"content":null`) || strings.Contains(string(b), "reasoning_content") {
		t.Fatalf("%s", b)
	}
}

func TestErrorHelpers(t *testing.T) {
	if got := string(ErrorBody(429, "", "slow")); got != `{"error":{"message":"slow","type":"rate_limit_error","code":"rate_limit_error"}}` {
		t.Fatal(got)
	}
	if got := string(ErrorBody(500, "x", "m")); !strings.Contains(got, `"type":"x"`) {
		t.Fatal(got)
	}
	if got := string(StreamErrorEvent("t", "m")); got != "data: {\"error\":{\"message\":\"m\",\"type\":\"t\",\"code\":\"t\"}}\n\ndata: [DONE]\n\n" {
		t.Fatal(got)
	}
	if string(Ping()) != ": ping\n\n" {
		t.Fatal("ping")
	}
}

func TestAppendJSONString(t *testing.T) {
	for _, s := range []string{"", "plain", "a\"b\\c", "\x00\x1f\x7f", "  ", "\xff\xfe", "日本語😀", "<>&", "a\xc3"} {
		b := appendJSONString(nil, s)
		var got string
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatalf("%q -> %s: %v", s, b, err)
		}
		if want := strings.ToValidUTF8(s, "�"); got != want && !(utf8.ValidString(s) == false && strings.Count(got, "�") > 0) {
			t.Fatalf("%q: got %q", s, got)
		}
	}
	if string(appendJSONString(nil, "\xff\xfe")) != "\"\\ufffd\\ufffd\"" {
		t.Fatal("each invalid byte should map to U+FFFD")
	}
}

func BenchmarkStreamEncodeText(b *testing.B) {
	e := NewStreamEncoder(&ir.Request{}, "model").(*streamEncoder)
	buf := e.Encode(nil, ir.Event{Kind: ir.EvStart, ID: "x"})
	ev := ir.Event{Kind: ir.EvText, Text: "Hello, world! This is a \"typical\" token chunk.\n"}
	b.ReportAllocs()
	for b.Loop() {
		buf = e.Encode(buf[:0], ev)
	}
}

func BenchmarkStreamDecodeText(b *testing.B) {
	d := NewStreamDecoder()
	data := []byte(`{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"gpt","choices":[{"index":0,"delta":{"content":"Hello world"},"finish_reason":null}]}`)
	emit := func(ir.Event) {}
	b.ReportAllocs()
	for b.Loop() {
		d.Decode(nil, data, emit)
	}
}
