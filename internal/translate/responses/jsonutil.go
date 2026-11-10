package responses

import (
	"bytes"
	"encoding/json"
	"math/rand/v2"
	"strconv"
	"unicode/utf8"
)

const hexDigits = "0123456789abcdef"

func appendJSONString(dst []byte, s string) []byte {
	dst = append(dst, '"')
	start := 0
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			if c >= 0x20 && c != '"' && c != '\\' {
				i++
				continue
			}
			dst = append(dst, s[start:i]...)
			switch c {
			case '"', '\\':
				dst = append(dst, '\\', c)
			case '\n':
				dst = append(dst, '\\', 'n')
			case '\r':
				dst = append(dst, '\\', 'r')
			case '\t':
				dst = append(dst, '\\', 't')
			case '\b':
				dst = append(dst, '\\', 'b')
			case '\f':
				dst = append(dst, '\\', 'f')
			default:
				dst = append(dst, '\\', 'u', '0', '0', hexDigits[c>>4], hexDigits[c&0xf])
			}
			i++
			start = i
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			dst = append(dst, s[start:i]...)
			dst = append(dst, `�`...)
			i++
			start = i
			continue
		}
		if r == ' ' || r == ' ' {
			dst = append(dst, s[start:i]...)
			dst = append(dst, '\\', 'u', '2', '0', '2', hexDigits[r&0xf])
			i += size
			start = i
			continue
		}
		i += size
	}
	dst = append(dst, s[start:]...)
	return append(dst, '"')
}

func appendKey(dst []byte, key string) []byte {
	dst = append(dst, ',', '"')
	dst = append(dst, key...)
	return append(dst, '"', ':')
}

func appendStrField(dst []byte, key, val string) []byte {
	return appendJSONString(appendKey(dst, key), val)
}

func appendIntField(dst []byte, key string, v int64) []byte {
	return strconv.AppendInt(appendKey(dst, key), v, 10)
}

func newID(prefix string) string {
	var b [32]byte
	n := copy(b[:], prefix)
	for _, u := range [2]uint64{rand.Uint64(), rand.Uint64()} {
		for j := 0; j < 12 && n < len(b); j++ {
			b[n] = hexDigits[u&0xf]
			u >>= 4
			n++
		}
	}
	return string(b[:n])
}

// rawString decodes a JSON string value without allocating a decoder for the
// common escape-free case. Non-string values yield "".
func rawString(r json.RawMessage) string {
	if len(r) < 2 || r[0] != '"' {
		return ""
	}
	if bytes.IndexByte(r, '\\') < 0 {
		return string(r[1 : len(r)-1])
	}
	var s string
	if json.Unmarshal(r, &s) != nil {
		return ""
	}
	return s
}

// eventType returns the "type" of an event, preferring the data payload.
func eventType(name, data []byte) string {
	const prefix = `{"type":"`
	if bytes.HasPrefix(data, []byte(prefix)) {
		rest := data[len(prefix):]
		if i := bytes.IndexByte(rest, '"'); i >= 0 && bytes.IndexByte(rest[:i], '\\') < 0 {
			return string(rest[:i])
		}
	}
	var t struct {
		Type string `json:"type"`
	}
	if len(data) > 0 && json.Unmarshal(data, &t) == nil && t.Type != "" {
		return t.Type
	}
	return string(name)
}
