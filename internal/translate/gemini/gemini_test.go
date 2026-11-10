package gemini

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Project-by-Myself/clirouter/internal/ir"
	"github.com/Project-by-Myself/clirouter/internal/sse"
)

func mustJSON(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("invalid json %s: %v", b, err)
	}
	return m
}

func path(v any, keys ...any) any {
	for _, k := range keys {
		switch kk := k.(type) {
		case string:
			m, _ := v.(map[string]any)
			v = m[kk]
		case int:
			a, _ := v.([]any)
			if kk >= len(a) {
				return nil
			}
			v = a[kk]
		}
	}
	return v
}

func canon(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestEncodeRequestToolConversation(t *testing.T) {
	RememberSignature("call_cached", "SIG_CACHED")
	req := &ir.Request{
		Model:  "gemini-2.5-pro",
		System: []ir.Part{{Type: ir.PartText, Text: "be terse"}},
		Messages: []ir.Message{
			{Role: ir.RoleUser, Parts: []ir.Part{
				{Type: ir.PartText, Text: "look"},
				{Type: ir.PartImage, URL: "https://x.test/a/cat.JPG?x=1"},
				{Type: ir.PartFile, URL: "gs://bucket/blob"},
				{Type: ir.PartImage, MediaType: "image/webp", Data: "QUJD"},
			}},
			{Role: ir.RoleAssistant, Parts: []ir.Part{
				{Type: ir.PartThinking, Text: "hmm", Signature: "GSIG", SigSource: ir.SigGemini},
				{Type: ir.PartThinking, Text: "claude thought", Signature: "ASIG", SigSource: ir.SigAnthropic},
				{Type: ir.PartRedactedThinking, Signature: "zzz", SigSource: ir.SigAnthropic},
				{Type: ir.PartText, Text: "calling"},
				{Type: ir.PartThinking, Signature: "TEXTSIG", SigSource: ir.SigGemini},
				{Type: ir.PartToolCall, ToolID: "call_cached", ToolName: "read", Args: `{"path":"a"}`},
				{Type: ir.PartToolCall, ToolID: "call_nocache", ToolName: "grep", Args: `not json`},
			}},
			{Role: ir.RoleTool, Parts: []ir.Part{
				{Type: ir.PartToolResult, ToolID: "call_cached", Result: []ir.Part{{Type: ir.PartText, Text: "file body"}}},
			}},
			{Role: ir.RoleTool, Parts: []ir.Part{
				{Type: ir.PartToolResult, ToolID: "call_nocache", IsError: true, Text: "boom"},
				{Type: ir.PartToolResult, ToolID: "call_unknown", Text: "?"},
			}},
			{Role: ir.RoleUser, Parts: []ir.Part{{Type: ir.PartText, Text: "continue"}}},
		},
	}
	b, err := EncodeRequest(req, ir.EncodeOptions{Model: "gemini-2.5-pro"})
	if err != nil {
		t.Fatal(err)
	}
	m := mustJSON(t, b)
	if _, ok := m["model"]; ok {
		t.Error("model must not be in body")
	}
	if got := path(m, "systemInstruction", "parts", 0, "text"); got != "be terse" {
		t.Errorf("system = %v", got)
	}
	contents := path(m, "contents").([]any)
	if len(contents) != 3 {
		t.Fatalf("want 3 contents (user, model, merged user), got %d: %s", len(contents), b)
	}
	user0 := path(contents[0], "parts").([]any)
	if canon(t, user0[1]) != `{"fileData":{"fileUri":"https://x.test/a/cat.JPG?x=1","mimeType":"image/jpeg"}}` {
		t.Errorf("fileData = %s", canon(t, user0[1]))
	}
	if path(user0[2], "fileData", "mimeType") != "application/octet-stream" {
		t.Errorf("default mime = %v", user0[2])
	}
	if canon(t, user0[3]) != `{"inlineData":{"data":"QUJD","mimeType":"image/webp"}}` {
		t.Errorf("inlineData = %s", canon(t, user0[3]))
	}
	model := contents[1].(map[string]any)
	if model["role"] != "model" {
		t.Fatalf("role = %v", model["role"])
	}
	mp := model["parts"].([]any)
	want := []string{
		`{"text":"hmm","thought":true,"thoughtSignature":"GSIG"}`,
		`{"text":"calling","thoughtSignature":"TEXTSIG"}`,
		`{"functionCall":{"args":{"path":"a"},"name":"read"},"thoughtSignature":"SIG_CACHED"}`,
		`{"functionCall":{"args":{},"name":"grep"},"thoughtSignature":"skip_thought_signature_validator"}`,
	}
	if len(mp) != len(want) {
		t.Fatalf("model parts = %s", canon(t, mp))
	}
	for i, w := range want {
		if g := canon(t, mp[i]); g != w {
			t.Errorf("model part %d = %s, want %s", i, g, w)
		}
	}
	user := contents[2].(map[string]any)
	up := user["parts"].([]any)
	wantU := []string{
		`{"functionResponse":{"name":"read","response":{"result":"file body"}}}`,
		`{"functionResponse":{"name":"grep","response":{"error":"boom"}}}`,
		`{"functionResponse":{"name":"call_unknown","response":{"result":"?"}}}`,
		`{"text":"continue"}`,
	}
	if user["role"] != "user" || len(up) != len(wantU) {
		t.Fatalf("merged user = %s", canon(t, user))
	}
	for i, w := range wantU {
		if g := canon(t, up[i]); g != w {
			t.Errorf("user part %d = %s, want %s", i, g, w)
		}
	}
	if g := canon(t, path(m, "generationConfig", "thinkingConfig")); g != `{"includeThoughts":true}` {
		t.Errorf("thinkingConfig = %s", g)
	}
}

func TestThinkingConfig(t *testing.T) {
	cases := []struct {
		model string
		r     *ir.Reasoning
		want  string
	}{
		{"gemini-3-pro-preview", nil, `{"includeThoughts":true}`},
		{"gemini-3-pro-preview", &ir.Reasoning{Effort: "minimal"}, `{"includeThoughts":true,"thinkingLevel":"low"}`},
		{"gemini-3-pro-preview", &ir.Reasoning{Effort: "low"}, `{"includeThoughts":true,"thinkingLevel":"low"}`},
		{"gemini-3-flash", &ir.Reasoning{Effort: "medium"}, `{"includeThoughts":true,"thinkingLevel":"high"}`},
		{"gemini-3-pro-preview", &ir.Reasoning{Effort: "xhigh"}, `{"includeThoughts":true,"thinkingLevel":"high"}`},
		{"gemini-3-pro-preview", &ir.Reasoning{BudgetTokens: 1000}, `{"includeThoughts":true,"thinkingLevel":"low"}`},
		{"gemini-3-pro-preview", &ir.Reasoning{Disabled: true}, `{"thinkingLevel":"low"}`},
		{"gemini-2.5-pro", nil, `{"includeThoughts":true}`},
		{"gemini-2.5-flash", &ir.Reasoning{Effort: "minimal"}, `{"includeThoughts":true,"thinkingBudget":512}`},
		{"gemini-2.5-flash", &ir.Reasoning{Effort: "low"}, `{"includeThoughts":true,"thinkingBudget":2048}`},
		{"gemini-2.5-flash", &ir.Reasoning{Effort: "medium"}, `{"includeThoughts":true,"thinkingBudget":8192}`},
		{"gemini-2.5-flash", &ir.Reasoning{Effort: "high"}, `{"includeThoughts":true,"thinkingBudget":24576}`},
		{"gemini-2.5-flash", &ir.Reasoning{Effort: "xhigh"}, `{"includeThoughts":true,"thinkingBudget":32768}`},
		{"gemini-2.5-flash", &ir.Reasoning{Effort: "high", BudgetTokens: 4000}, `{"includeThoughts":true,"thinkingBudget":4000}`},
		{"gemini-2.5-flash", &ir.Reasoning{Disabled: true}, `{"thinkingBudget":0}`},
		{"gemini-2.0-flash", nil, `null`},
	}
	for _, c := range cases {
		b, err := EncodeRequest(&ir.Request{Reasoning: c.r, Messages: []ir.Message{{Role: ir.RoleUser, Parts: []ir.Part{{Type: ir.PartText, Text: "hi"}}}}}, ir.EncodeOptions{Model: c.model})
		if err != nil {
			t.Fatal(err)
		}
		if g := canon(t, path(mustJSON(t, b), "generationConfig", "thinkingConfig")); g != c.want {
			t.Errorf("%s %+v: got %s want %s", c.model, c.r, g, c.want)
		}
	}
}

func TestEncodeRequestToolsAndConfig(t *testing.T) {
	temp, topP, topK := 0.5, 0.9, 40
	schema := `{"type":"object","properties":{"q":{"type":["string","null"]}},"additionalProperties":false,"$defs":{}}`
	req := &ir.Request{
		Tools: []ir.Tool{
			{Name: "search", Description: "web", Schema: json.RawMessage(schema)},
			{Name: "patch", Kind: ir.ToolCustom},
			{Name: "noargs"},
		},
		ToolChoice:     &ir.ToolChoice{Mode: "tool", Name: "search"},
		MaxTokens:      100,
		Temperature:    &temp,
		TopP:           &topP,
		TopK:           &topK,
		Stop:           []string{"END"},
		ResponseFormat: json.RawMessage(`{"type":"json_schema","json_schema":{"name":"x","schema":{"type":"object"}}}`),
		Reasoning:      &ir.Reasoning{Disabled: true},
		Messages: []ir.Message{
			{Role: ir.RoleUser, Parts: []ir.Part{{Type: ir.PartText, Text: "hi"}}},
			{Role: ir.RoleAssistant, Parts: []ir.Part{{Type: ir.PartToolCall, ToolID: "c1", ToolName: "patch", ToolKind: ir.ToolCustom, Args: "*** Begin \"Patch\""}}},
		},
	}
	b, err := EncodeRequest(req, ir.EncodeOptions{Model: "gemini-2.5-flash"})
	if err != nil {
		t.Fatal(err)
	}
	m := mustJSON(t, b)
	decls := path(m, "tools", 0, "functionDeclarations").([]any)
	if g := canon(t, path(decls[0], "parametersJsonSchema")); g != canon(t, mustJSON(t, []byte(schema))) {
		t.Errorf("schema = %s", g)
	}
	if path(decls[0], "parameters") != nil {
		t.Error("must not use parameters")
	}
	if g := canon(t, path(decls[1], "parametersJsonSchema")); g != `{"properties":{"input":{"type":"string"}},"required":["input"],"type":"object"}` {
		t.Errorf("custom schema = %s", g)
	}
	if path(decls[2], "parametersJsonSchema") != nil {
		t.Error("noargs should have no schema")
	}
	if g := canon(t, path(m, "toolConfig")); g != `{"functionCallingConfig":{"allowedFunctionNames":["search"],"mode":"ANY"}}` {
		t.Errorf("toolConfig = %s", g)
	}
	if g := canon(t, path(m, "contents", 1, "parts", 0, "functionCall", "args")); g != `{"input":"*** Begin \"Patch\""}` {
		t.Errorf("custom args = %s", g)
	}
	gc := canon(t, path(m, "generationConfig"))
	wantGC := `{"maxOutputTokens":100,"responseJsonSchema":{"type":"object"},"responseMimeType":"application/json","stopSequences":["END"],"temperature":0.5,"thinkingConfig":{"thinkingBudget":0},"topK":40,"topP":0.9}`
	if gc != wantGC {
		t.Errorf("generationConfig = %s", gc)
	}
	for mode, want := range map[string]string{"auto": "AUTO", "none": "NONE", "required": "ANY"} {
		req.ToolChoice = &ir.ToolChoice{Mode: mode}
		b, _ := EncodeRequest(req, ir.EncodeOptions{})
		if g := path(mustJSON(t, b), "toolConfig", "functionCallingConfig", "mode"); g != want {
			t.Errorf("%s -> %v", mode, g)
		}
	}
}

const transcript = `data: {"candidates":[{"content":{"parts":[{"text":"Let me think","thought":true}],"role":"model"},"index":0}],"usageMetadata":{"promptTokenCount":100,"totalTokenCount":100},"modelVersion":"gemini-2.5-pro","responseId":"resp-1"}

data: {"candidates":[{"content":{"parts":[{"text":" harder.","thought":true}],"role":"model"},"index":0}],"modelVersion":"gemini-2.5-pro","responseId":"resp-1"}

data: {"candidates":[{"content":{"parts":[{"text":"I'll read it.","thoughtSignature":"TXTSIG"}],"role":"model"},"index":0}],"modelVersion":"gemini-2.5-pro","responseId":"resp-1"}

data: {"candidates":[{"content":{"parts":[{"functionCall":{"name":"read_file","args":{"path":"/tmp/a"}},"thoughtSignature":"FCSIG"},{"functionCall":{"id":"fc-2","name":"ls","args":{}}},{"executableCode":{"language":"PYTHON","code":"print(1)"}}],"role":"model"},"finishReason":"STOP","index":0}],"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":20,"thoughtsTokenCount":30,"cachedContentTokenCount":64,"totalTokenCount":150},"modelVersion":"gemini-2.5-pro","responseId":"resp-1"}

data: {"candidates":[{"content":{"parts":[{"text":"ignored after done"}]}}]}

`

func decodeAll(t *testing.T, d ir.StreamDecoder, stream string) []ir.Event {
	t.Helper()
	var evs []ir.Event
	r := sse.NewReader(strings.NewReader(stream), 0)
	for {
		e, err := r.Next()
		if err != nil {
			break
		}
		if err := d.Decode(e.Name, e.Data, func(ev ir.Event) { evs = append(evs, ev) }); err != nil {
			t.Fatal(err)
		}
	}
	return evs
}

func TestStreamDecodeTranscript(t *testing.T) {
	evs := decodeAll(t, NewStreamDecoder(), transcript)
	var kinds []ir.EventKind
	for _, e := range evs {
		kinds = append(kinds, e.Kind)
	}
	want := []ir.EventKind{
		ir.EvStart, ir.EvThinking, ir.EvUsage, ir.EvThinking, ir.EvText, ir.EvThinkingSignature,
		ir.EvToolStart, ir.EvToolArgs, ir.EvToolEnd, ir.EvToolStart, ir.EvToolArgs, ir.EvToolEnd,
		ir.EvText, ir.EvFinish, ir.EvUsage, ir.EvDone,
	}
	if !reflect.DeepEqual(kinds, want) {
		t.Fatalf("kinds = %v\nwant    %v", kinds, want)
	}
	if evs[0].ID != "resp-1" || evs[0].Model != "gemini-2.5-pro" {
		t.Errorf("start = %+v", evs[0])
	}
	if evs[5].Signature != "TXTSIG" || evs[5].SigSource != ir.SigGemini {
		t.Errorf("sig = %+v", evs[5])
	}
	id := evs[6].ID
	if !strings.HasPrefix(id, "call_") || len(id) < 20 || evs[6].Name != "read_file" || evs[7].Text != `{"path":"/tmp/a"}` {
		t.Errorf("tool = %+v %+v", evs[6], evs[7])
	}
	if sig, ok := LookupSignature(id); !ok || sig != "FCSIG" {
		t.Errorf("signature cache = %q %v", sig, ok)
	}
	if evs[9].ID != "fc-2" || evs[10].Text != "{}" {
		t.Errorf("tool2 = %+v %+v", evs[9], evs[10])
	}
	if evs[12].Text != "```python\nprint(1)\n```\n" {
		t.Errorf("code = %q", evs[12].Text)
	}
	if evs[13].Finish != ir.FinishToolCalls {
		t.Errorf("finish = %v", evs[13].Finish)
	}
	u := evs[14].Usage
	if *u != (ir.Usage{InputTokens: 100, OutputTokens: 50, CacheReadTokens: 64, ReasoningTokens: 30}) {
		t.Errorf("usage = %+v", u)
	}

	agg := ir.NewAggregator()
	for _, e := range evs {
		agg.Add(e)
	}
	resp := agg.Response()
	if len(resp.Content) != 6 || resp.Content[0].Text != "Let me think harder." || resp.Content[2].Signature != "TXTSIG" {
		t.Errorf("aggregate = %+v", resp.Content)
	}
}

func TestStreamDecodeFinishAndErrors(t *testing.T) {
	cases := []struct {
		data   string
		kinds  []ir.EventKind
		finish ir.FinishReason
		errTyp string
	}{
		{`{"candidates":[{"content":{"parts":[{"text":"x"}]},"finishReason":"MAX_TOKENS"}]}`, []ir.EventKind{ir.EvStart, ir.EvText, ir.EvFinish, ir.EvDone}, ir.FinishLength, ""},
		{`{"candidates":[{"finishReason":"SAFETY"}]}`, []ir.EventKind{ir.EvStart, ir.EvFinish, ir.EvDone}, ir.FinishContentFilter, ""},
		{`{"candidates":[{"finishReason":"RECITATION"}]}`, []ir.EventKind{ir.EvStart, ir.EvFinish, ir.EvDone}, ir.FinishContentFilter, ""},
		{`{"candidates":[{"finishReason":"MALFORMED_FUNCTION_CALL"}]}`, []ir.EventKind{ir.EvStart, ir.EvFinish, ir.EvDone}, ir.FinishError, ""},
		{`{"candidates":[{"finishReason":"STOP"}]}`, []ir.EventKind{ir.EvStart, ir.EvFinish, ir.EvDone}, ir.FinishStop, ""},
		{`{"error":{"code":429,"message":"quota","status":"RESOURCE_EXHAUSTED"}}`, []ir.EventKind{ir.EvError}, "", "rate_limit_error"},
		{`{"promptFeedback":{"blockReason":"PROHIBITED_CONTENT"},"usageMetadata":{"promptTokenCount":5}}`, []ir.EventKind{ir.EvStart, ir.EvError}, "", "content_filter"},
		{`{"response":{"candidates":[{"content":{"parts":[{"text":"wrapped"}]},"finishReason":"STOP"}]}}`, []ir.EventKind{ir.EvStart, ir.EvText, ir.EvFinish, ir.EvDone}, ir.FinishStop, ""},
	}
	for _, c := range cases {
		var evs []ir.Event
		if err := NewStreamDecoder().Decode(nil, []byte(c.data), func(e ir.Event) { evs = append(evs, e) }); err != nil {
			t.Fatal(err)
		}
		var kinds []ir.EventKind
		for _, e := range evs {
			kinds = append(kinds, e.Kind)
			if e.Kind == ir.EvFinish && e.Finish != c.finish {
				t.Errorf("%s: finish %v", c.data, e.Finish)
			}
			if e.Kind == ir.EvError && e.ErrType != c.errTyp {
				t.Errorf("%s: errType %v", c.data, e.ErrType)
			}
		}
		if !reflect.DeepEqual(kinds, c.kinds) {
			t.Errorf("%s: kinds %v want %v", c.data, kinds, c.kinds)
		}
		typ, _, isErr := IsErrorEvent(nil, []byte(c.data))
		if isErr != (c.errTyp != "") || typ != c.errTyp {
			t.Errorf("%s: IsErrorEvent = %q %v", c.data, typ, isErr)
		}
		if IsTerminal(nil, []byte(c.data)) != (c.finish != "") {
			t.Errorf("%s: IsTerminal wrong", c.data)
		}
	}
	if err := NewStreamDecoder().Decode(nil, []byte("{bad"), func(ir.Event) {}); err == nil {
		t.Error("want error on malformed chunk")
	}
}

func TestStreamDecodeCustomTool(t *testing.T) {
	d := NewStreamDecoderFor(&ir.Request{Tools: []ir.Tool{{Name: "patch", Kind: ir.ToolCustom}}})
	var evs []ir.Event
	d.Decode(nil, []byte(`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"patch","args":{"input":"raw\ntext"}}}]}}]}`), func(e ir.Event) { evs = append(evs, e) })
	if evs[1].ToolKind != ir.ToolCustom || evs[2].Text != "raw\ntext" {
		t.Errorf("custom = %+v %+v", evs[1], evs[2])
	}
}

func TestUsageFromEvent(t *testing.T) {
	u, ok := UsageFromEvent(nil, []byte(`{"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":3,"thoughtsTokenCount":2,"cachedContentTokenCount":1}}`))
	if !ok || *u != (ir.Usage{InputTokens: 7, OutputTokens: 5, CacheReadTokens: 1, ReasoningTokens: 2}) {
		t.Errorf("usage = %+v %v", u, ok)
	}
	if _, ok := UsageFromEvent(nil, []byte(`{"candidates":[]}`)); ok {
		t.Error("no usage expected")
	}
}

func TestClientStreamEncodeRoundTrip(t *testing.T) {
	RememberSignature("call_x", "XSIG")
	in := []ir.Event{
		{Kind: ir.EvStart, ID: "r1", Model: "upstream-model"},
		{Kind: ir.EvThinking, Text: "pondering \"quotes\"\n"},
		{Kind: ir.EvThinkingSignature, Signature: "ANTH", SigSource: ir.SigAnthropic},
		{Kind: ir.EvText, Text: "hello   <tag> \x01"},
		{Kind: ir.EvToolStart, ID: "call_x", Name: "read"},
		{Kind: ir.EvToolArgs, ID: "call_x", Text: `{"pa`},
		{Kind: ir.EvToolArgs, ID: "call_x", Text: `th":"a"}`},
		{Kind: ir.EvToolEnd, ID: "call_x"},
		{Kind: ir.EvUsage, Usage: &ir.Usage{InputTokens: 10, OutputTokens: 8, ReasoningTokens: 3, CacheReadTokens: 4}},
		{Kind: ir.EvFinish, Finish: ir.FinishToolCalls},
		{Kind: ir.EvDone},
	}
	enc := NewStreamEncoder(&ir.Request{}, "gemini-client-model")
	var out []byte
	for _, e := range in {
		out = enc.Encode(out, e)
	}
	if strings.Contains(string(out), "event:") || strings.Contains(string(out), "[DONE]") {
		t.Errorf("unexpected framing: %s", out)
	}
	r := sse.NewReader(strings.NewReader(string(out)), 0)
	n := 0
	for {
		e, err := r.Next()
		if err != nil {
			break
		}
		n++
		m := mustJSON(t, e.Data)
		if m["modelVersion"] != "gemini-client-model" || m["responseId"] != "r1" || path(m, "candidates", 0, "content", "role") != "model" {
			t.Errorf("chunk envelope = %s", e.Data)
		}
	}
	if n != 5 {
		t.Errorf("chunks = %d\n%s", n, out)
	}
	if !strings.Contains(string(out), `"finishReason":"STOP"`) || !strings.Contains(string(out), `"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"thoughtsTokenCount":3,"cachedContentTokenCount":4,"totalTokenCount":18}`) {
		t.Errorf("final chunk wrong: %s", out)
	}
	if !strings.Contains(string(out), `"thoughtSignature":"XSIG"`) {
		t.Error("functionCall should carry cached signature")
	}

	evs := decodeAll(t, NewStreamDecoder(), string(out))
	agg := ir.NewAggregator()
	for _, e := range evs {
		agg.Add(e)
	}
	resp := agg.Response()
	if resp.ID != "r1" || resp.Finish != ir.FinishToolCalls || resp.Usage != (ir.Usage{InputTokens: 10, OutputTokens: 8, ReasoningTokens: 3, CacheReadTokens: 4}) {
		t.Errorf("resp = %+v", resp)
	}
	wantContent := []ir.Part{
		{Type: ir.PartThinking, Text: "pondering \"quotes\"\n", Signature: "x-anthropic|ANTH", SigSource: ir.SigGemini},
		{Type: ir.PartText, Text: "hello   <tag> \x01"},
		{Type: ir.PartToolCall, ToolID: "call_x", ToolName: "read", Args: `{"path":"a"}`},
	}
	if !reflect.DeepEqual(resp.Content, wantContent) {
		t.Errorf("content = %+v", resp.Content)
	}
}

func TestClientEncodeErrorAndAllocs(t *testing.T) {
	enc := NewStreamEncoder(nil, "m")
	out := enc.Encode(nil, ir.Event{Kind: ir.EvError, ErrType: "rate_limit_error", Text: "slow"})
	if string(out) != `data: {"error":{"code":429,"message":"slow","status":"RESOURCE_EXHAUSTED"}}`+"\n\n" {
		t.Errorf("err = %s", out)
	}
	if got := enc.Encode(nil, ir.Event{Kind: ir.EvText, Text: "x"}); len(got) != 0 {
		t.Error("nothing after error")
	}

	enc = NewStreamEncoder(nil, "m")
	enc.Encode(nil, ir.Event{Kind: ir.EvStart, ID: "id"})
	buf := make([]byte, 0, 4096)
	ev := ir.Event{Kind: ir.EvText, Text: "some streamed text"}
	allocs := testing.AllocsPerRun(100, func() { buf = enc.Encode(buf[:0], ev) })
	if allocs != 0 {
		t.Errorf("text chunk allocs = %v", allocs)
	}
}

func TestDecodeRequest(t *testing.T) {
	body := `{
	 "systemInstruction":{"parts":[{"text":"sys"}]},
	 "contents":[
	  {"role":"user","parts":[{"text":"hi"},{"inlineData":{"mimeType":"image/png","data":"AAA"}},{"fileData":{"fileUri":"gs://b/doc.pdf"}}]},
	  {"role":"model","parts":[
	    {"text":"think ","thought":true},{"text":"more","thought":true},{"text":"","thought":true,"thoughtSignature":"x-anthropic|ASIG"},
	    {"text":"ok","thoughtSignature":"GTEXT"},
	    {"functionCall":{"name":"read","args":{"p":1}},"thoughtSignature":"RSIG"},
	    {"functionCall":{"name":"read","args":{"p":2}}},
	    {"functionCall":{"id":"own","name":"ls"}}]},
	  {"role":"user","parts":[
	    {"functionResponse":{"name":"read","response":{"output":"two"}}},
	    {"functionResponse":{"name":"read","response":{"result":{"k":1}}}},
	    {"functionResponse":{"id":"own","name":"ls","response":{"error":"denied"}}},
	    {"text":"next"}]}
	 ],
	 "tools":[{"functionDeclarations":[
	   {"name":"read","description":"d","parameters":{"type":"OBJECT","properties":{"p":{"type":"INTEGER"},"type":{"type":"STRING"}}}},
	   {"name":"ls","parametersJsonSchema":{"type":"object"}}]},{"googleSearch":{}}],
	 "toolConfig":{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["ls"]}},
	 "generationConfig":{"maxOutputTokens":50,"temperature":0.2,"topK":3,"stopSequences":["x"],"responseMimeType":"application/json","responseJsonSchema":{"type":"object"},
	   "thinkingConfig":{"thinkingBudget":8192,"includeThoughts":true}}
	}`
	req, err := DecodeRequest([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if req.Model != "" || len(req.System) != 1 || req.System[0].Text != "sys" {
		t.Errorf("system/model = %+v", req)
	}
	if len(req.Messages) != 4 {
		t.Fatalf("messages = %d", len(req.Messages))
	}
	u := req.Messages[0].Parts
	if u[1].Type != ir.PartImage || u[1].Data != "AAA" || u[2].Type != ir.PartFile || u[2].MediaType != "application/pdf" {
		t.Errorf("user media = %+v", u)
	}
	a := req.Messages[1].Parts
	wantA := []ir.Part{
		{Type: ir.PartThinking, Text: "think more", Signature: "ASIG", SigSource: ir.SigAnthropic},
		{Type: ir.PartText, Text: "ok"},
		{Type: ir.PartThinking, Signature: "GTEXT", SigSource: ir.SigGemini},
		{Type: ir.PartToolCall, ToolID: "call_read_0", ToolName: "read", Args: `{"p":1}`},
		{Type: ir.PartToolCall, ToolID: "call_read_1", ToolName: "read", Args: `{"p":2}`},
		{Type: ir.PartToolCall, ToolID: "own", ToolName: "ls", Args: `{}`},
	}
	if !reflect.DeepEqual(a, wantA) {
		t.Errorf("assistant = %+v", a)
	}
	if sig, _ := LookupSignature("call_read_0"); sig != "RSIG" {
		t.Errorf("decode should cache functionCall signature, got %q", sig)
	}
	tm := req.Messages[2]
	if tm.Role != ir.RoleTool || len(tm.Parts) != 3 ||
		tm.Parts[0].ToolID != "call_read_1" || tm.Parts[0].Result[0].Text != "two" ||
		tm.Parts[1].ToolID != "call_read_0" || tm.Parts[1].Result[0].Text != `{"k":1}` ||
		tm.Parts[2].ToolID != "own" || !tm.Parts[2].IsError || tm.Parts[2].Result[0].Text != "denied" {
		t.Errorf("tool results = %+v", tm)
	}
	if req.Messages[3].Role != ir.RoleUser || req.Messages[3].Parts[0].Text != "next" {
		t.Errorf("trailing user = %+v", req.Messages[3])
	}
	if len(req.Tools) != 2 || canon(t, json.RawMessage(req.Tools[0].Schema)) != `{"properties":{"p":{"type":"integer"},"type":{"type":"string"}},"type":"object"}` {
		t.Errorf("tools = %s", req.Tools[0].Schema)
	}
	if *req.ToolChoice != (ir.ToolChoice{Mode: "tool", Name: "ls"}) {
		t.Errorf("toolChoice = %+v", req.ToolChoice)
	}
	if req.MaxTokens != 50 || *req.Temperature != 0.2 || *req.TopK != 3 || req.Stop[0] != "x" {
		t.Errorf("gen = %+v", req)
	}
	if string(req.ResponseFormat) != `{"type":"json_schema","json_schema":{"name":"response","schema":{"type":"object"}}}` {
		t.Errorf("rf = %s", req.ResponseFormat)
	}
	if *req.Reasoning != (ir.Reasoning{BudgetTokens: 8192, Effort: "medium", Summary: "auto"}) {
		t.Errorf("reasoning = %+v", req.Reasoning)
	}

	for tc, want := range map[string]ir.Reasoning{
		`{"thinkingBudget":0}`:                           {Disabled: true},
		`{"thinkingLevel":"LOW","includeThoughts":true}`: {Effort: "low", Summary: "auto"},
		`{"thinkingBudget":-1,"includeThoughts":true}`:   {},
	} {
		r, err := DecodeRequest([]byte(`{"contents":[],"generationConfig":{"thinkingConfig":` + tc + `}}`))
		if err != nil {
			t.Fatal(err)
		}
		got := ir.Reasoning{}
		if r.Reasoning != nil {
			got = *r.Reasoning
		}
		if got != want {
			t.Errorf("%s -> %+v", tc, got)
		}
	}

	// Re-encoding to a Gemini upstream replays the gemini signatures only.
	b, err := EncodeRequest(req, ir.EncodeOptions{Model: "gemini-3-pro"})
	if err != nil {
		t.Fatal(err)
	}
	m := mustJSON(t, b)
	parts := path(m, "contents", 1, "parts").([]any)
	if canon(t, parts[0]) != `{"text":"ok","thoughtSignature":"GTEXT"}` ||
		path(parts[1], "thoughtSignature") != "RSIG" ||
		path(parts[2], "thoughtSignature") != DummySignature {
		t.Errorf("re-encode = %s", canon(t, parts))
	}
}

func TestEncodeResponse(t *testing.T) {
	resp := &ir.Response{
		ID: "r", Model: "m", Finish: ir.FinishLength,
		Content: []ir.Part{
			{Type: ir.PartThinking, Text: "t", Signature: "S", SigSource: ir.SigGemini},
			{Type: ir.PartText, Text: "answer"},
			{Type: ir.PartToolCall, ToolID: "c", ToolName: "f", Args: `{"a":1}`},
			{Type: ir.PartToolCall, ToolID: "d", ToolName: "patch", Args: "raw"},
		},
		Usage: ir.Usage{InputTokens: 3, OutputTokens: 2},
	}
	b, err := EncodeResponse(&ir.Request{Tools: []ir.Tool{{Name: "patch", Kind: ir.ToolCustom}}}, resp)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"candidates":[{"content":{"role":"model","parts":[{"text":"t","thought":true,"thoughtSignature":"S"},{"text":"answer"},{"functionCall":{"id":"c","name":"f","args":{"a":1}}},{"functionCall":{"id":"d","name":"patch","args":{"input":"raw"}}}]},"finishReason":"MAX_TOKENS","index":0}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2,"totalTokenCount":5},"modelVersion":"m","responseId":"r"}`
	if string(b) != want {
		t.Errorf("got  %s\nwant %s", b, want)
	}
	b, _ = EncodeResponse(nil, &ir.Response{ID: "e"})
	mustJSON(t, b)
	if !strings.Contains(string(b), `"parts":[{"text":""}]`) {
		t.Errorf("empty = %s", b)
	}
}

func TestErrorHelpers(t *testing.T) {
	cases := map[int]string{400: "INVALID_ARGUMENT", 401: "UNAUTHENTICATED", 403: "PERMISSION_DENIED", 404: "NOT_FOUND", 429: "RESOURCE_EXHAUSTED", 500: "INTERNAL", 503: "UNAVAILABLE", 418: "INVALID_ARGUMENT"}
	for code, st := range cases {
		m := mustJSON(t, ErrorBody(code, "x", "m\"sg"))
		if path(m, "error", "status") != st || path(m, "error", "code") != float64(code) || path(m, "error", "message") != "m\"sg" {
			t.Errorf("%d -> %v", code, m)
		}
	}
	ev := StreamErrorEvent("authentication_error", "bad key")
	if !strings.HasPrefix(string(ev), "data: ") || !strings.HasSuffix(string(ev), "\n\n") {
		t.Errorf("frame = %q", ev)
	}
	typ, msg, ok := IsErrorEvent(nil, ev[6:len(ev)-2])
	if !ok || typ != "authentication_error" || msg != "bad key" {
		t.Errorf("roundtrip = %q %q %v", typ, msg, ok)
	}
	if Ping() != nil {
		t.Error("Ping must be nil")
	}
	if _, _, ok := IsErrorEvent(nil, []byte(`{"candidates":[{"content":{"parts":[{"text":"\"error\""}]}}]}`)); ok {
		t.Error("text mentioning error is not an error")
	}
}

func TestSigLRU(t *testing.T) {
	c := newSigLRU(3, time.Hour)
	now := time.Unix(1000, 0)
	c.now = func() time.Time { return now }
	c.put("a", "1")
	c.put("b", "2")
	c.put("c", "3")
	if _, ok := c.get("a"); !ok {
		t.Fatal("a missing")
	}
	c.put("d", "4") // evicts b (least recently used)
	if _, ok := c.get("b"); ok {
		t.Error("b should be evicted")
	}
	for _, k := range []string{"a", "c", "d"} {
		if _, ok := c.get(k); !ok {
			t.Errorf("%s missing", k)
		}
	}
	c.put("a", "1b")
	if v, _ := c.get("a"); v != "1b" || c.len() != 3 {
		t.Errorf("update: %q len %d", v, c.len())
	}
	now = now.Add(time.Hour)
	if _, ok := c.get("a"); ok {
		t.Error("a should be expired")
	}
	if c.len() != 2 {
		t.Errorf("expired entry not removed, len %d", c.len())
	}

	big := newSigLRU(sigCacheMax, sigCacheTTL)
	for i := 0; i < sigCacheMax+10; i++ {
		big.put(strings.Repeat("k", 1)+string(rune('a'+i%26))+itoa(i), "v")
	}
	if big.len() != sigCacheMax {
		t.Errorf("bounded len = %d", big.len())
	}

	done := make(chan bool)
	for g := 0; g < 8; g++ {
		go func(g int) {
			for i := 0; i < 1000; i++ {
				RememberSignature(itoa(g*1000+i), "s")
				LookupSignature(itoa(i))
			}
			done <- true
		}(g)
	}
	for g := 0; g < 8; g++ {
		<-done
	}
	RememberSignature("dummy", DummySignature)
	if _, ok := LookupSignature("dummy"); ok {
		t.Error("dummy signature must not be cached")
	}
}

func itoa(i int) string { return string(appendInt(nil, int64(i))) }

func TestAppendString(t *testing.T) {
	cases := []string{"", "plain", "q\"b\\s/", "ctl\x00\x01\x1f\n\r\t", "uni ✓ 日本", "bad\xff\xfe utf8", "ls ps ", "<>&"}
	for _, s := range cases {
		b := appendString(nil, s)
		var got string
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatalf("%q -> %s: %v", s, b, err)
		}
		want := strings.ToValidUTF8(s, "�")
		if s == "bad\xff\xfe utf8" {
			want = "bad�� utf8"
		}
		if got != want {
			t.Errorf("%q -> %q", s, got)
		}
		if strings.ContainsRune(string(b), ' ') || strings.ContainsRune(string(b), ' ') {
			t.Errorf("raw line separator in %s", b)
		}
	}
}
