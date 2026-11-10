package redact

import (
	"strings"
	"testing"
)

func TestString(t *testing.T) {
	secrets := []string{
		"sk-ant-api03-AAAAAAAAAAAAAAAAAAAAAAAA",
		"sk-proj-abcdefghijklmnop1234",
		"AIzaSyA1234567890abcdefghijklmnop",
		"ya29.a0AfH6SMBxxxxxxxxxxxxxxxx",
		"ghu_abcdefghijklmnopqrstuvwxyz0123",
		"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.sig",
		"AKIAIOSFODNN7EXAMPLE",
		"ABSKQmVkcm9ja0FQSUtleS1leGFtcGxlLWtleQ==",
		"-----BEGIN PRIVATE KEY-----\\nMIIEvQIBADANBgkqhkiG9w0BAQEFAASC\\n-----END PRIVATE KEY-----",
	}
	for _, sec := range secrets {
		in := `{"error":{"message":"Incorrect API key provided: ` + sec + `. See docs."}}`
		out := String(in)
		if strings.Contains(out, sec) || !strings.Contains(out, "[REDACTED]") {
			t.Errorf("not redacted: %s", out)
		}
		if b := Bytes([]byte(in)); strings.Contains(string(b), sec) {
			t.Errorf("bytes not redacted: %s", b)
		}
	}
	if got := String("Authorization: Bearer abcdefghijklmnop"); got != "Authorization: Bearer [REDACTED]" {
		t.Errorf("bearer: %q", got)
	}
	if got := String("GET /x?key=supersecretvalue&alt=sse"); got != "GET /x?key=[REDACTED]&alt=sse" {
		t.Errorf("query: %q", got)
	}
	for _, ok := range []string{"model not found: claude-sonnet-4", "rate limit exceeded, retry in 12s", "task-management-service"} {
		if String(ok) != ok {
			t.Errorf("false positive: %q -> %q", ok, String(ok))
		}
	}
}
