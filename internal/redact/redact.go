// Package redact masks credentials that upstream error messages sometimes
// echo back (e.g. "invalid api key sk-..."), before such text is shown to
// clients, stored in the request log or exposed by the admin API.
package redact

import "regexp"

var patterns = regexp.MustCompile(`(?i)` +
	`(bearer\s+)[A-Za-z0-9._~+/=-]{8,}` + // Authorization header values
	`|\b(?:sk|rk|pk)-[A-Za-z0-9_-]{8,}` + // OpenAI, Anthropic (sk-ant-), OpenRouter (sk-or-), clirouter (sk-clr-)
	`|\bAIza[0-9A-Za-z_-]{20,}` + // Google API keys
	`|\bya29\.[0-9A-Za-z._-]{10,}` + // Google OAuth access tokens
	`|\b1//[0-9A-Za-z_-]{20,}` + // Google refresh tokens
	`|\bgh[pousr]_[A-Za-z0-9]{20,}` + // GitHub tokens
	`|\bgithub_pat_[A-Za-z0-9_]{20,}` +
	`|\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]+` + // JWTs
	`|((?:api[_-]?key|access_token|refresh_token|key)=)[^&\s"']{8,}`) // query strings

// String replaces anything that looks like a credential with "[REDACTED]".
func String(s string) string {
	if len(s) < 8 {
		return s
	}
	return patterns.ReplaceAllString(s, "${1}${2}[REDACTED]")
}

// Bytes is String for byte slices; it returns b unchanged when nothing matched.
func Bytes(b []byte) []byte {
	if len(b) < 8 || !patterns.Match(b) {
		return b
	}
	return patterns.ReplaceAll(b, []byte("${1}${2}[REDACTED]"))
}
