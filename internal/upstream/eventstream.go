package upstream

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"

	"github.com/Project-by-Myself/clirouter/internal/redact"
	"github.com/Project-by-Myself/clirouter/internal/translate/anthropic"
)

// EventStreamType is the Content-Type of AWS event-stream responses
// (Bedrock InvokeModelWithResponseStream).
const EventStreamType = "application/vnd.amazon.eventstream"

// IsEventStream reports whether resp carries AWS event-stream framing.
func IsEventStream(resp *http.Response) bool {
	return strings.HasPrefix(resp.Header.Get("Content-Type"), EventStreamType)
}

// maxEventStreamMessage bounds one frame (AWS caps messages at 16 MB).
const maxEventStreamMessage = 24 << 20

var (
	ErrEventStreamCRC   = errors.New("eventstream: checksum mismatch")
	ErrEventStreamFrame = errors.New("eventstream: malformed frame")
)

// Message is one decoded event-stream frame. Its slices are only valid
// until the next call to Next.
type Message struct {
	MessageType   []byte // :message-type (event | exception | error)
	EventType     []byte // :event-type
	ExceptionType []byte // :exception-type
	ErrorCode     []byte // :error-code
	ErrorMessage  []byte // :error-message
	Payload       []byte
}

// EventStreamDecoder reads AWS event-stream frames:
//
//	prelude:  total_len u32 | headers_len u32 | prelude_crc u32
//	headers:  name_len u8 | name | type u8 | value ...
//	payload
//	message_crc u32 (CRC32/IEEE of everything before it)
//
// It reuses one buffer, so steady-state decoding does not allocate.
type EventStreamDecoder struct {
	br  *bufio.Reader
	buf []byte
}

func NewEventStreamDecoder(r io.Reader) *EventStreamDecoder {
	return &EventStreamDecoder{br: bufio.NewReaderSize(r, 32<<10)}
}

// Next returns the next frame, io.EOF at a clean end of stream, or an error
// for a truncated, oversized or corrupt frame.
func (d *EventStreamDecoder) Next() (Message, error) {
	var m Message
	if cap(d.buf) < 12 {
		d.buf = make([]byte, 0, 4096)
	}
	pre := d.buf[:12]
	if _, err := io.ReadFull(d.br, pre); err != nil {
		if err == io.ErrUnexpectedEOF {
			return m, fmt.Errorf("%w: truncated prelude", ErrEventStreamFrame)
		}
		return m, err
	}
	total := binary.BigEndian.Uint32(pre[0:4])
	hlen := binary.BigEndian.Uint32(pre[4:8])
	if crc32.ChecksumIEEE(pre[:8]) != binary.BigEndian.Uint32(pre[8:12]) {
		return m, fmt.Errorf("%w (prelude)", ErrEventStreamCRC)
	}
	if total < 16 || total > maxEventStreamMessage || hlen > total-16 {
		return m, fmt.Errorf("%w: total %d, headers %d", ErrEventStreamFrame, total, hlen)
	}
	if cap(d.buf) < int(total) {
		nb := make([]byte, total, max(int(total), 2*cap(d.buf)))
		copy(nb, pre)
		d.buf = nb
	}
	msg := d.buf[:total]
	if _, err := io.ReadFull(d.br, msg[12:]); err != nil {
		return m, fmt.Errorf("%w: truncated message", ErrEventStreamFrame)
	}
	if crc32.ChecksumIEEE(msg[:total-4]) != binary.BigEndian.Uint32(msg[total-4:]) {
		return m, fmt.Errorf("%w (message)", ErrEventStreamCRC)
	}
	if err := parseHeaders(msg[12:12+hlen], &m); err != nil {
		return m, err
	}
	m.Payload = msg[12+hlen : total-4]
	return m, nil
}

func parseHeaders(h []byte, m *Message) error {
	for len(h) > 0 {
		nl := int(h[0])
		if len(h) < 1+nl+1 {
			return fmt.Errorf("%w: header name", ErrEventStreamFrame)
		}
		name := h[1 : 1+nl]
		typ := h[1+nl]
		h = h[2+nl:]
		var val []byte
		var n int
		switch typ {
		case 0, 1: // bool true / false
		case 2: // byte
			n = 1
		case 3: // int16
			n = 2
		case 4: // int32
			n = 4
		case 5, 8: // int64, timestamp
			n = 8
		case 9: // uuid
			n = 16
		case 6, 7: // bytes, string: u16 length prefix
			if len(h) < 2 {
				return fmt.Errorf("%w: header value", ErrEventStreamFrame)
			}
			l := int(binary.BigEndian.Uint16(h))
			if len(h) < 2+l {
				return fmt.Errorf("%w: header value", ErrEventStreamFrame)
			}
			val, h = h[2:2+l], h[2+l:]
			if typ == 7 {
				switch string(name) {
				case ":message-type":
					m.MessageType = val
				case ":event-type":
					m.EventType = val
				case ":exception-type":
					m.ExceptionType = val
				case ":error-code":
					m.ErrorCode = val
				case ":error-message":
					m.ErrorMessage = val
				}
			}
			continue
		default:
			return fmt.Errorf("%w: header type %d", ErrEventStreamFrame, typ)
		}
		if len(h) < n {
			return fmt.Errorf("%w: header value", ErrEventStreamFrame)
		}
		h = h[n:]
	}
	return nil
}

// bedrockSSE converts a Bedrock response stream into the Anthropic SSE the
// anthropic codec reads: each "chunk" event carries one base64-encoded
// Anthropic stream event, which becomes "event: <type>\ndata: <json>\n\n".
// Exceptions become Anthropic "error" events, so the proxy's in-band error
// handling (failover before content, error frame after) applies unchanged.
type bedrockSSE struct {
	dec *EventStreamDecoder
	out []byte
	off int
	b64 []byte
	err error
}

// NewBedrockSSE wraps a Bedrock event-stream body.
func NewBedrockSSE(r io.Reader) io.Reader { return &bedrockSSE{dec: NewEventStreamDecoder(r)} }

func (b *bedrockSSE) Read(p []byte) (int, error) {
	for b.off >= len(b.out) {
		if b.err != nil {
			return 0, b.err
		}
		b.out, b.off = b.out[:0], 0
		b.err = b.next()
	}
	n := copy(p, b.out[b.off:])
	b.off += n
	return n, nil
}

// next appends the SSE for the next frame that produces output.
func (b *bedrockSSE) next() error {
	for len(b.out) == 0 {
		m, err := b.dec.Next()
		if err != nil {
			return err
		}
		switch string(m.MessageType) {
		case "event", "":
			if string(m.EventType) != "chunk" {
				continue // e.g. initial-response; nothing to forward
			}
			if err := b.chunk(m.Payload); err != nil {
				return err
			}
		case "exception":
			msg := gjson.GetBytes(m.Payload, "message").Str
			if msg == "" {
				msg = gjson.GetBytes(m.Payload, "Message").Str
			}
			b.out = append(b.out, anthropic.StreamErrorEvent(bedrockErrType(string(m.ExceptionType)), bedrockErrText(string(m.ExceptionType), msg))...)
		case "error":
			b.out = append(b.out, anthropic.StreamErrorEvent(bedrockErrType(string(m.ErrorCode)), bedrockErrText(string(m.ErrorCode), string(m.ErrorMessage)))...)
		}
	}
	return nil
}

var (
	bytesField = []byte(`"bytes":"`)
	typePrefix = []byte(`{"type":"`)
)

func (b *bedrockSSE) chunk(payload []byte) error {
	enc := fieldString(payload, bytesField)
	if enc == nil {
		enc = []byte(gjson.GetBytes(payload, "bytes").Str)
	}
	var err error
	b.b64, err = base64.StdEncoding.AppendDecode(b.b64[:0], enc)
	if err != nil {
		return fmt.Errorf("%w: chunk is not base64", ErrEventStreamFrame)
	}
	data := b.b64
	if bytes.IndexByte(data, '\n') >= 0 {
		var c bytes.Buffer
		if json.Compact(&c, data) != nil {
			return fmt.Errorf("%w: chunk is not JSON", ErrEventStreamFrame)
		}
		data = c.Bytes()
	}
	var name []byte
	if bytes.HasPrefix(data, typePrefix) {
		name = fieldString(data[:min(len(data), 64)], typePrefix[1:])
	}
	if name == nil {
		name = []byte(gjson.GetBytes(data, "type").Str)
	}
	if len(name) > 0 {
		b.out = append(b.out, "event: "...)
		b.out = append(b.out, name...)
		b.out = append(b.out, '\n')
	}
	b.out = append(b.out, "data: "...)
	b.out = append(b.out, data...)
	b.out = append(b.out, '\n', '\n')
	return nil
}

// fieldString returns the unescaped string value after key (which ends
// with the opening quote), or nil when absent or escaped.
func fieldString(b, key []byte) []byte {
	i := bytes.Index(b, key)
	if i < 0 {
		return nil
	}
	v := b[i+len(key):]
	j := bytes.IndexByte(v, '"')
	if j < 0 || bytes.IndexByte(v[:j], '\\') >= 0 {
		return nil
	}
	return v[:j]
}

// bedrockErrType maps a Bedrock exception name to an Anthropic error type
// the proxy's in-band classification understands.
func bedrockErrType(exc string) string {
	e := strings.ToLower(exc)
	switch {
	case strings.Contains(e, "throttling"), strings.Contains(e, "servicequotaexceeded"):
		return "rate_limit_error"
	case strings.Contains(e, "serviceunavailable"), strings.Contains(e, "modelnotready"):
		return "overloaded_error"
	case strings.Contains(e, "validation"):
		return "invalid_request_error"
	case strings.Contains(e, "accessdenied"), strings.Contains(e, "unrecognizedclient"), strings.Contains(e, "expiredtoken"):
		return "authentication_error"
	}
	return "api_error"
}

func bedrockErrText(exc, msg string) string {
	msg = redact.String(msg)
	switch {
	case exc == "":
		return msg
	case msg == "":
		return exc
	}
	return exc + ": " + msg
}

// AppendEventStreamMessage appends one event-stream frame with string
// headers (name, value pairs). It is the inverse of EventStreamDecoder.Next,
// used by tests and mock upstreams.
func AppendEventStreamMessage(dst []byte, headers [][2]string, payload []byte) []byte {
	hlen := 0
	for _, h := range headers {
		hlen += 1 + len(h[0]) + 1 + 2 + len(h[1])
	}
	total := 12 + hlen + len(payload) + 4
	start := len(dst)
	dst = binary.BigEndian.AppendUint32(dst, uint32(total))
	dst = binary.BigEndian.AppendUint32(dst, uint32(hlen))
	dst = binary.BigEndian.AppendUint32(dst, crc32.ChecksumIEEE(dst[start:start+8]))
	for _, h := range headers {
		dst = append(dst, byte(len(h[0])))
		dst = append(dst, h[0]...)
		dst = append(dst, 7)
		dst = binary.BigEndian.AppendUint16(dst, uint16(len(h[1])))
		dst = append(dst, h[1]...)
	}
	dst = append(dst, payload...)
	return binary.BigEndian.AppendUint32(dst, crc32.ChecksumIEEE(dst[start:]))
}

// AppendBedrockChunk appends a Bedrock "chunk" event carrying one Anthropic
// stream event (JSON).
func AppendBedrockChunk(dst []byte, event []byte) []byte {
	payload := append(append([]byte(`{"bytes":"`), base64.StdEncoding.EncodeToString(event)...), `"}`...)
	return AppendEventStreamMessage(dst, [][2]string{{":event-type", "chunk"}, {":content-type", "application/json"}, {":message-type", "event"}}, payload)
}

// AppendBedrockException appends a Bedrock exception frame.
func AppendBedrockException(dst []byte, exception, message string) []byte {
	payload, _ := json.Marshal(map[string]string{"message": message})
	return AppendEventStreamMessage(dst, [][2]string{{":exception-type", exception}, {":content-type", "application/json"}, {":message-type", "exception"}}, payload)
}
