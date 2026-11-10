package upstream

import (
	"bytes"
	"errors"
	"hash/crc32"
	"io"
	"strings"
	"testing"
)

var bedrockEvents = []string{
	`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-x","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":1}}}`,
	`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
	`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`,
	`{"type":"content_block_stop","index":0}`,
	`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`,
	`{"type":"message_stop","amazon-bedrock-invocationMetrics":{"inputTokenCount":10,"outputTokenCount":5}}`,
}

func bedrockStream(events ...string) []byte {
	var b []byte
	for _, e := range events {
		b = AppendBedrockChunk(b, []byte(e))
	}
	return b
}

func wantSSE(events ...string) string {
	var sb strings.Builder
	for _, e := range events {
		typ := e[len(`{"type":"`):]
		typ = typ[:strings.IndexByte(typ, '"')]
		sb.WriteString("event: " + typ + "\ndata: " + e + "\n\n")
	}
	return sb.String()
}

func TestEventStreamDecoderRoundTrip(t *testing.T) {
	raw := AppendEventStreamMessage(nil, [][2]string{{":message-type", "event"}, {":event-type", "chunk"}}, []byte("payload"))
	d := NewEventStreamDecoder(bytes.NewReader(raw))
	m, err := d.Next()
	if err != nil {
		t.Fatal(err)
	}
	if string(m.MessageType) != "event" || string(m.EventType) != "chunk" || string(m.Payload) != "payload" {
		t.Fatalf("%+v", m)
	}
	if _, err := d.Next(); err != io.EOF {
		t.Fatalf("want EOF, got %v", err)
	}
}

func TestEventStreamDecoderSkipsNonStringHeaders(t *testing.T) {
	// A frame with a bool, an int32 and a timestamp header before the
	// string headers (hand-built: the test encoder only writes strings).
	var hdr []byte
	hdr = append(hdr, 4, 'f', 'l', 'a', 'g', 0)
	hdr = append(hdr, 3, 'n', 'u', 'm', 4, 0, 0, 0, 7)
	hdr = append(hdr, 2, 't', 's', 8, 0, 0, 0, 0, 0, 0, 0, 1)
	str := AppendEventStreamMessage(nil, [][2]string{{":message-type", "event"}}, nil)
	strHdr := str[12 : len(str)-4]
	frame := buildFrame(append(hdr, strHdr...), []byte("x"))
	m, err := NewEventStreamDecoder(bytes.NewReader(frame)).Next()
	if err != nil || string(m.MessageType) != "event" || string(m.Payload) != "x" {
		t.Fatalf("%+v %v", m, err)
	}
}

func buildFrame(headers, payload []byte) []byte {
	total := 12 + len(headers) + len(payload) + 4
	b := []byte{byte(total >> 24), byte(total >> 16), byte(total >> 8), byte(total), byte(len(headers) >> 24), byte(len(headers) >> 16), byte(len(headers) >> 8), byte(len(headers))}
	b = appendCRC(b, b)
	b = append(b, headers...)
	b = append(b, payload...)
	return appendCRC(b, b)
}

func appendCRC(dst, over []byte) []byte {
	c := crc32.ChecksumIEEE(over)
	return append(dst, byte(c>>24), byte(c>>16), byte(c>>8), byte(c))
}

func TestEventStreamCRCFailures(t *testing.T) {
	good := AppendBedrockChunk(nil, []byte(bedrockEvents[2]))

	badMsg := bytes.Clone(good)
	badMsg[len(badMsg)-10] ^= 0xff // corrupt the payload
	if _, err := NewEventStreamDecoder(bytes.NewReader(badMsg)).Next(); !errors.Is(err, ErrEventStreamCRC) {
		t.Fatalf("payload corruption: %v", err)
	}

	badPrelude := bytes.Clone(good)
	badPrelude[3] ^= 0x01 // total length
	if _, err := NewEventStreamDecoder(bytes.NewReader(badPrelude)).Next(); !errors.Is(err, ErrEventStreamCRC) {
		t.Fatalf("prelude corruption: %v", err)
	}

	if _, err := NewEventStreamDecoder(bytes.NewReader(good[:len(good)-3])).Next(); !errors.Is(err, ErrEventStreamFrame) {
		t.Fatalf("truncated: %v", err)
	}

	// Through the SSE converter, the frames before the bad one come out and
	// then the read fails, so the proxy reports a broken stream.
	stream := append(AppendBedrockChunk(nil, []byte(bedrockEvents[0])), badMsg...)
	out, err := io.ReadAll(NewBedrockSSE(bytes.NewReader(stream)))
	if !errors.Is(err, ErrEventStreamCRC) || string(out) != wantSSE(bedrockEvents[0]) {
		t.Fatalf("out %q err %v", out, err)
	}
}

func TestBedrockSSEConversion(t *testing.T) {
	// One-byte reads exercise frames split across reads.
	out, err := io.ReadAll(NewBedrockSSE(oneByte{bytes.NewReader(bedrockStream(bedrockEvents...))}))
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != wantSSE(bedrockEvents...) {
		t.Fatalf("got\n%s\nwant\n%s", out, wantSSE(bedrockEvents...))
	}
}

type oneByte struct{ r io.Reader }

func (o oneByte) Read(p []byte) (int, error) {
	if len(p) > 1 {
		p = p[:1]
	}
	return o.r.Read(p)
}

func TestBedrockSSEExceptions(t *testing.T) {
	cases := []struct{ exc, typ string }{
		{"throttlingException", "rate_limit_error"},
		{"serviceUnavailableException", "overloaded_error"},
		{"validationException", "invalid_request_error"},
		{"modelStreamErrorException", "api_error"},
	}
	for _, c := range cases {
		stream := AppendBedrockException(nil, c.exc, "boom sk-ant-api03-AAAAAAAAAAAAAAAAAAAA")
		out, err := io.ReadAll(NewBedrockSSE(bytes.NewReader(stream)))
		if err != nil {
			t.Fatal(err)
		}
		s := string(out)
		if !strings.HasPrefix(s, "event: error\ndata: ") || !strings.Contains(s, `"type":"`+c.typ+`"`) || !strings.Contains(s, c.exc+": boom") {
			t.Fatalf("%s: %q", c.exc, s)
		}
		if strings.Contains(s, "sk-ant-api03") {
			t.Fatalf("secret leaked: %q", s)
		}
	}
	// Generic "error" frames use headers instead of a payload.
	raw := AppendEventStreamMessage(nil, [][2]string{{":message-type", "error"}, {":error-code", "InternalFailure"}, {":error-message", "oops"}}, nil)
	out, _ := io.ReadAll(NewBedrockSSE(bytes.NewReader(raw)))
	if !strings.Contains(string(out), "InternalFailure: oops") {
		t.Fatalf("%q", out)
	}
}

func TestBedrockSSESkipsUnknownEvents(t *testing.T) {
	raw := AppendEventStreamMessage(nil, [][2]string{{":message-type", "event"}, {":event-type", "initial-response"}}, []byte("{}"))
	raw = AppendBedrockChunk(raw, []byte(bedrockEvents[3]))
	out, err := io.ReadAll(NewBedrockSSE(bytes.NewReader(raw)))
	if err != nil || string(out) != wantSSE(bedrockEvents[3]) {
		t.Fatalf("%q %v", out, err)
	}
}

// cycle replays the same bytes forever.
type cycle struct {
	b   []byte
	off int
}

func (c *cycle) Read(p []byte) (int, error) {
	n := copy(p, c.b[c.off:])
	c.off = (c.off + n) % len(c.b)
	return n, nil
}

func TestBedrockSSEZeroAllocSteadyState(t *testing.T) {
	ev := bedrockEvents[2]
	frame := AppendBedrockChunk(nil, []byte(ev))
	r := NewBedrockSSE(&cycle{b: frame})
	n := len(wantSSE(ev))
	buf := make([]byte, n)
	for i := 0; i < 10; i++ { // warm up buffers
		io.ReadFull(r, buf)
	}
	if allocs := testing.AllocsPerRun(200, func() { io.ReadFull(r, buf) }); allocs != 0 {
		t.Fatalf("%.1f allocs per text chunk", allocs)
	}
	if string(buf) != wantSSE(ev) {
		t.Fatalf("%q", buf)
	}
}

func BenchmarkBedrockSSE(b *testing.B) {
	frame := AppendBedrockChunk(nil, []byte(bedrockEvents[2]))
	r := NewBedrockSSE(&cycle{b: frame})
	buf := make([]byte, len(wantSSE(bedrockEvents[2])))
	b.ReportAllocs()
	b.SetBytes(int64(len(frame)))
	for b.Loop() {
		io.ReadFull(r, buf)
	}
}
