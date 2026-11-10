package auth

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// newPKCE returns a 43-char verifier and its S256 challenge (RFC 7636).
func newPKCE() (verifier, challenge string) {
	b := make([]byte, 32)
	rand.Read(b)
	verifier = base64.RawURLEncoding.EncodeToString(b)
	return verifier, pkceChallenge(verifier)
}

func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func randHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// jwtClaims decodes a JWT payload WITHOUT verifying the signature. The result
// is for display/routing hints only and must not drive security decisions.
func jwtClaims(tok string) (map[string]any, error) {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return nil, errors.New("jwt: malformed")
	}
	p := strings.TrimRight(parts[1], "=")
	raw, err := base64.RawURLEncoding.DecodeString(p)
	if err != nil {
		if raw, err = base64.RawStdEncoding.DecodeString(p); err != nil {
			return nil, fmt.Errorf("jwt: %w", err)
		}
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("jwt: %w", err)
	}
	return m, nil
}

func jwtExpiry(tok string) time.Time {
	m, err := jwtClaims(tok)
	if err != nil {
		return time.Time{}
	}
	if f, ok := m["exp"].(float64); ok && f > 0 {
		return time.Unix(int64(f), 0)
	}
	return time.Time{}
}

// codexIdentity extracts email, ChatGPT account id and plan from an OpenAI id_token.
func codexIdentity(idToken string) (email, accountID, plan string) {
	m, err := jwtClaims(idToken)
	if err != nil {
		return
	}
	email, _ = m["email"].(string)
	if email == "" {
		if p, ok := m["https://api.openai.com/profile"].(map[string]any); ok {
			email, _ = p["email"].(string)
		}
	}
	if a, ok := m["https://api.openai.com/auth"].(map[string]any); ok {
		accountID, _ = a["chatgpt_account_id"].(string)
		plan, _ = a["chatgpt_plan_type"].(string)
	}
	return
}

// HTTPError is a non-2xx response from a token/identity endpoint. Body holds
// only the OAuth error fields (or a short excerpt), never tokens.
type HTTPError struct {
	Status int
	Code   string
	Msg    string
}

func (e *HTTPError) Error() string {
	s := fmt.Sprintf("http %d", e.Status)
	if e.Code != "" {
		s += " " + e.Code
	}
	if e.Msg != "" {
		s += ": " + e.Msg
	}
	return s
}

func oauthErrorCode(err error) string {
	var he *HTTPError
	if errors.As(err, &he) {
		return he.Code
	}
	return ""
}

func newHTTPError(status int, body []byte) *HTTPError {
	e := &HTTPError{Status: status}
	var m map[string]any
	if json.Unmarshal(body, &m) == nil {
		switch v := m["error"].(type) {
		case string:
			e.Code = v
		case map[string]any:
			e.Code, _ = v["type"].(string)
			if e.Code == "" {
				e.Code, _ = v["status"].(string)
			}
			e.Msg, _ = v["message"].(string)
		}
		if d, ok := m["error_description"].(string); ok {
			e.Msg = d
		}
		if e.Msg == "" {
			e.Msg, _ = m["message"].(string)
		}
	} else if v, err := url.ParseQuery(string(body)); err == nil && v.Get("error") != "" {
		e.Code, e.Msg = v.Get("error"), v.Get("error_description")
	} else {
		s := strings.TrimSpace(string(body))
		if len(s) > 200 {
			s = s[:200] + "..."
		}
		e.Msg = s
	}
	if len(e.Msg) > 300 {
		e.Msg = e.Msg[:300] + "..."
	}
	return e
}

type reqOpt func(*http.Request)

func withHeader(k, v string) reqOpt { return func(r *http.Request) { r.Header.Set(k, v) } }

// doJSON sends a request and decodes a JSON response into out (may be nil).
// body may be url.Values (form), []byte/nil, or any value (JSON-encoded).
func (m *Manager) doJSON(ctx context.Context, method, u string, body any, out any, opts ...reqOpt) error {
	var rd io.Reader
	ctype := ""
	switch b := body.(type) {
	case nil:
	case url.Values:
		rd, ctype = strings.NewReader(b.Encode()), "application/x-www-form-urlencoded"
	default:
		buf, err := json.Marshal(b)
		if err != nil {
			return err
		}
		rd, ctype = bytes.NewReader(buf), "application/json"
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return err
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", UserAgent())
	for _, o := range opts {
		o(req)
	}
	resp, err := m.hc.Do(req)
	if err != nil {
		return redactURLError(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		return newHTTPError(resp.StatusCode, raw)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%s: decode response: %w", req.URL.Host, err)
	}
	return nil
}

// redactURLError drops the query string from *url.Error so codes never leak into logs.
func redactURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		if p, perr := url.Parse(ue.URL); perr == nil {
			p.RawQuery = ""
			return fmt.Errorf("%s %s: %w", ue.Op, p.String(), ue.Err)
		}
	}
	return err
}
