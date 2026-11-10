package upstream

import (
	"bytes"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"github.com/Project-by-Myself/clirouter/internal/redact"
	"github.com/Project-by-Myself/clirouter/internal/router"
)

// Action tells the proxy what to do after a failed attempt.
type Action uint8

const (
	NextAccount Action = iota // same target, another account
	NextTarget                // skip remaining accounts of this provider
	Fatal                     // the request itself is wrong: return to the client
	RefreshAuth               // refresh OAuth token and retry the same account once
)

type Failure struct {
	Status     int
	Kind       router.FailureKind
	Action     Action
	RetryAfter time.Duration
	Message    string
	Type       string
}

// Classify interprets a non-2xx upstream response.
func Classify(status int, h http.Header, body []byte, oauth bool) Failure {
	f := Failure{Status: status, Message: ErrorMessage(body)}
	if status == 429 || status == 402 {
		f.RetryAfter = RetryAfter(h, body)
	} else {
		// Rate-limit reset headers (x-ratelimit-reset-*, anthropic-ratelimit-*)
		// come with every response and describe quota windows, not this
		// error: honoring them would bench an account for minutes after a
		// single 5xx. Only an explicit Retry-After asks to back off.
		f.RetryAfter = explicitRetryAfter(h)
	}
	if f.Message == "" {
		f.Message = http.StatusText(status)
	}
	lower := strings.ToLower(f.Message)
	if aws := awsErrorType(h); aws != "" {
		// AWS names the error in X-Amzn-ErrorType ("ThrottlingException:…");
		// some of them use a status that would be misread (a quota error is 400).
		switch {
		case strings.HasPrefix(aws, "throttling"), strings.HasPrefix(aws, "servicequotaexceeded"):
			f.Kind, f.Action, f.Type = router.FailRateLimit, NextAccount, "rate_limit_error"
			return f
		case strings.HasPrefix(aws, "accessdenied"), strings.HasPrefix(aws, "unrecognizedclient"),
			strings.HasPrefix(aws, "expiredtoken"), strings.HasPrefix(aws, "invalidsignature"),
			strings.HasPrefix(aws, "incompletesignature"):
			f.Kind, f.Action, f.Type = router.FailAuth, NextAccount, "authentication_error"
			return f
		case strings.HasPrefix(aws, "resourcenotfound"):
			f.Kind, f.Action, f.Type = router.FailModel, NextTarget, "not_found_error"
			return f
		case strings.HasPrefix(aws, "modelnotready"), strings.HasPrefix(aws, "modelerror"), strings.HasPrefix(aws, "modeltimeout"):
			f.Kind, f.Action, f.Type = router.FailServer, NextAccount, "api_error"
			return f
		case strings.HasPrefix(aws, "serviceunavailable"):
			f.Kind, f.Action, f.Type = router.FailServer, NextTarget, "overloaded_error"
			return f
		}
	}
	switch {
	case status == 401 || status == 403:
		f.Kind, f.Action, f.Type = router.FailAuth, NextAccount, "authentication_error"
		if oauth && status == 401 {
			f.Action = RefreshAuth
		}
	case status == 402:
		f.Kind, f.Action, f.Type = router.FailQuota, NextAccount, "rate_limit_error"
	case status == 429:
		f.Type = "rate_limit_error"
		f.Kind, f.Action = router.FailRateLimit, NextAccount
		if containsAny(lower, "quota", "billing", "credit", "usage limit", "insufficient") {
			f.Kind = router.FailQuota
		}
	case status == 404:
		f.Kind, f.Action, f.Type = router.FailModel, NextTarget, "not_found_error"
	case status == 400 || status == 422:
		f.Action, f.Type = Fatal, "invalid_request_error"
		if containsAny(lower, "model not found", "does not exist", "unsupported model", "not supported model", "model_not_found", "invalid model", "no such model", "not available for",
			"model identifier is invalid", "deploymentnotfound", "deployment for this resource does not exist") {
			f.Kind, f.Action = router.FailModel, NextTarget
		}
	case status == 413:
		f.Action, f.Type = Fatal, "request_too_large"
	case status == 408 || status == 409 || status == 425:
		f.Kind, f.Action, f.Type = router.FailServer, NextAccount, "api_error"
	case status == 529 || status == 503:
		f.Kind, f.Action, f.Type = router.FailServer, NextTarget, "overloaded_error"
	case status >= 500:
		f.Kind, f.Action, f.Type = router.FailServer, NextAccount, "api_error"
	default:
		f.Action, f.Type = Fatal, "invalid_request_error"
	}
	return f
}

// awsErrorType returns the lower-cased AWS error code from X-Amzn-ErrorType
// ("ValidationException:http://…" -> "validationexception"), or "".
func awsErrorType(h http.Header) string {
	v := h.Get("X-Amzn-Errortype")
	if v == "" {
		return ""
	}
	v, _, _ = strings.Cut(v, ":")
	return strings.ToLower(v)
}

func containsAny(s string, subs ...string) bool {
	for _, x := range subs {
		if strings.Contains(s, x) {
			return true
		}
	}
	return false
}

// ErrorMessage extracts a human message from the common error shapes.
func ErrorMessage(body []byte) string { return redact.String(errorMessage(body)) }

func errorMessage(body []byte) string {
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return ""
	}
	if body[0] == '[' { // Gemini sometimes wraps errors in an array
		body = []byte(gjson.GetBytes(body, "0").Raw)
	}
	for _, p := range []string{"error.message", "message", "Message", "error.msg", "detail", "error"} {
		if v := gjson.GetBytes(body, p); v.Exists() && v.Type == gjson.String && v.Str != "" {
			return v.Str
		}
	}
	s := string(body)
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}

// explicitRetryAfter reads Retry-After / Retry-After-Ms only.
func explicitRetryAfter(h http.Header) time.Duration {
	if v := h.Get("Retry-After"); v != "" {
		if s, err := strconv.ParseFloat(v, 64); err == nil && s > 0 {
			return time.Duration(s * float64(time.Second))
		}
		if t, err := http.ParseTime(v); err == nil {
			if d := time.Until(t); d > 0 {
				return d
			}
		}
	}
	if v := h.Get("Retry-After-Ms"); v != "" {
		if ms, err := strconv.ParseFloat(v, 64); err == nil && ms > 0 {
			return time.Duration(ms * float64(time.Millisecond))
		}
	}
	return 0
}

// RetryAfter reads standard and provider-specific reset hints.
func RetryAfter(h http.Header, body []byte) time.Duration {
	if d := explicitRetryAfter(h); d > 0 {
		return d
	}
	for _, k := range []string{"Anthropic-Ratelimit-Unified-Reset", "Anthropic-Ratelimit-Requests-Reset", "Anthropic-Ratelimit-Tokens-Reset"} {
		if v := h.Get(k); v != "" {
			if t, err := time.Parse(time.RFC3339, v); err == nil {
				if d := time.Until(t); d > 0 {
					return d
				}
			} else if sec, err := strconv.ParseInt(v, 10, 64); err == nil && sec > 1e9 {
				if d := time.Until(time.Unix(sec, 0)); d > 0 {
					return d
				}
			}
		}
	}
	for _, k := range []string{"X-Ratelimit-Reset-Requests", "X-Ratelimit-Reset-Tokens"} {
		if v := h.Get(k); v != "" {
			if d, err := time.ParseDuration(v); err == nil && d > 0 {
				return d
			}
		}
	}
	// Google RetryInfo: {"error":{"details":[{"@type":"...RetryInfo","retryDelay":"12s"}]}}
	if len(body) > 0 {
		for _, d := range gjson.GetBytes(body, "error.details").Array() {
			if rd := d.Get("retryDelay").Str; rd != "" {
				if dur, err := time.ParseDuration(rd); err == nil {
					return dur
				}
			}
		}
		// OpenAI/Codex usage limits: "resets_in_seconds" / "resets_at"
		if s := gjson.GetBytes(body, "error.resets_in_seconds").Int(); s > 0 {
			return time.Duration(s) * time.Second
		}
		if at := gjson.GetBytes(body, "error.resets_at").Int(); at > 1e9 {
			if d := time.Until(time.Unix(at, 0)); d > 0 {
				return d
			}
		}
	}
	return 0
}
