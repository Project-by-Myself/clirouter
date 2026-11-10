// Package anthropic translates the Anthropic Messages API wire format
// (POST /v1/messages) to and from the canonical IR, both as a client format
// and as an upstream format.
package anthropic

import (
	"crypto/rand"
	"strings"
	"unicode/utf8"

	"github.com/Project-by-Myself/clirouter/internal/ir"
)

const hexDigits = "0123456789abcdef"

// appendJSONString appends s as a quoted JSON string. Invalid UTF-8 becomes
// U+FFFD; U+2028/U+2029 are escaped so the output is safe to embed anywhere.
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
			case '"':
				dst = append(dst, '\\', '"')
			case '\\':
				dst = append(dst, '\\', '\\')
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
				dst = append(dst, '\\', 'u', '0', '0', hexDigits[c>>4], hexDigits[c&0xF])
			}
			i++
			start = i
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			dst = append(dst, s[start:i]...)
			dst = append(dst, '\\', 'u', 'f', 'f', 'f', 'd')
			i += size
			start = i
			continue
		}
		if r == 0x2028 || r == 0x2029 {
			dst = append(dst, s[start:i]...)
			dst = append(dst, '\\', 'u', '2', '0', '2', hexDigits[r&0xF])
			i += size
			start = i
			continue
		}
		i += size
	}
	dst = append(dst, s[start:]...)
	return append(dst, '"')
}

const idAlphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

func newID(prefix string) string {
	var b [24]byte
	_, _ = rand.Read(b[:])
	out := make([]byte, len(prefix)+len(b))
	copy(out, prefix)
	for i, c := range b {
		out[len(prefix)+i] = idAlphabet[int(c)%len(idAlphabet)]
	}
	return string(out)
}

// Signatures from non-Anthropic providers are tunnelled through Anthropic
// clients with this prefix so they round-trip with the right SigSource.
const sigPrefix = "clirouter."

func encodeSig(src, sig string) string {
	if src == "" || src == ir.SigAnthropic || sig == "" {
		return sig
	}
	return sigPrefix + src + "." + sig
}

func decodeSig(s string) (sig, src string) {
	if rest, ok := strings.CutPrefix(s, sigPrefix); ok {
		if i := strings.IndexByte(rest, '.'); i > 0 {
			return rest[i+1:], rest[:i]
		}
	}
	return s, ir.SigAnthropic
}

func firstNonSpace(b []byte) byte {
	for _, c := range b {
		switch c {
		case ' ', '\t', '\n', '\r':
			continue
		}
		return c
	}
	return 0
}
