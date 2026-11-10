// Package cloudauth implements the cloud-provider credential schemes that
// are not a plain API key: AWS Signature Version 4 (Amazon Bedrock with IAM
// credentials) and the Google OAuth2 JWT-bearer flow for service accounts
// (Vertex AI). Only the standard library is used for crypto.
package cloudauth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

// AWSCredentials are IAM credentials. SessionToken is set for temporary
// (STS) credentials.
type AWSCredentials struct {
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
}

// ErrNoAWSCredentials is returned for "aws:env" when the environment holds
// no access key.
var ErrNoAWSCredentials = errors.New("aws:env: AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY are not set")

// ParseAWSKey parses a configured key of the form
// "aws:<ACCESS_KEY_ID>:<SECRET_ACCESS_KEY>[:<SESSION_TOKEN>]" or "aws:env".
// ok is false when key is not an "aws:" key (e.g. a Bedrock API key, which
// is sent as a bearer token). Errors never contain the key.
func ParseAWSKey(key string) (c AWSCredentials, ok bool, err error) {
	rest, ok := strings.CutPrefix(key, "aws:")
	if !ok {
		return c, false, nil
	}
	if rest == "env" {
		c = AWSCredentials{AccessKeyID: os.Getenv("AWS_ACCESS_KEY_ID"), SecretAccessKey: os.Getenv("AWS_SECRET_ACCESS_KEY"), SessionToken: os.Getenv("AWS_SESSION_TOKEN")}
		if c.AccessKeyID == "" || c.SecretAccessKey == "" {
			return c, true, ErrNoAWSCredentials
		}
		return c, true, nil
	}
	id, rest, _ := strings.Cut(rest, ":")
	secret, token, _ := strings.Cut(rest, ":")
	if id == "" || secret == "" {
		return c, true, errors.New("aws credentials: want aws:<ACCESS_KEY_ID>:<SECRET_ACCESS_KEY>[:<SESSION_TOKEN>]")
	}
	return AWSCredentials{AccessKeyID: id, SecretAccessKey: secret, SessionToken: token}, true, nil
}

const (
	amzDateFormat = "20060102T150405Z"
	sigV4Algo     = "AWS4-HMAC-SHA256"
)

// SignV4 signs req in place with AWS Signature Version 4 for service in
// region, over the given payload (the exact request body). It sets
// X-Amz-Date, X-Amz-Security-Token (temporary credentials) and
// Authorization. Signed headers are Host, Content-Type and every X-Amz-*
// header, so headers changed after signing must not be among those.
//
// The canonical URI encodes each segment of the already-escaped path once
// more, as AWS requires for every service except S3 (a Bedrock model id
// "...-v1:0" is sent as "...-v1%3A0" and signed as "...-v1%253A0").
func SignV4(req *http.Request, payload []byte, c AWSCredentials, region, service string, now time.Time) {
	now = now.UTC()
	amzDate := now.Format(amzDateFormat)
	date := amzDate[:8]
	req.Header.Set("X-Amz-Date", amzDate)
	if c.SessionToken != "" {
		req.Header.Set("X-Amz-Security-Token", c.SessionToken)
	}

	host := req.Host
	if host == "" {
		host = req.URL.Host
	}
	type kv struct{ k, v string }
	hdrs := []kv{{"host", canonicalHeaderValue([]string{host})}}
	for k, vs := range req.Header {
		lk := strings.ToLower(k)
		if lk == "content-type" || strings.HasPrefix(lk, "x-amz-") {
			hdrs = append(hdrs, kv{lk, canonicalHeaderValue(vs)})
		}
	}
	sort.Slice(hdrs, func(i, j int) bool { return hdrs[i].k < hdrs[j].k })

	var canon, signed strings.Builder
	canon.Grow(256 + len(req.URL.RawQuery))
	canon.WriteString(req.Method)
	canon.WriteByte('\n')
	canon.WriteString(canonicalURI(req.URL.EscapedPath()))
	canon.WriteByte('\n')
	canon.WriteString(canonicalQuery(req.URL.RawQuery))
	canon.WriteByte('\n')
	for i, h := range hdrs {
		canon.WriteString(h.k)
		canon.WriteByte(':')
		canon.WriteString(h.v)
		canon.WriteByte('\n')
		if i > 0 {
			signed.WriteByte(';')
		}
		signed.WriteString(h.k)
	}
	canon.WriteByte('\n')
	canon.WriteString(signed.String())
	canon.WriteByte('\n')
	payloadHash := sha256.Sum256(payload)
	canon.WriteString(hex.EncodeToString(payloadHash[:]))

	scope := date + "/" + region + "/" + service + "/aws4_request"
	creqHash := sha256.Sum256([]byte(canon.String()))
	sts := sigV4Algo + "\n" + amzDate + "\n" + scope + "\n" + hex.EncodeToString(creqHash[:])

	k := hmacSHA256([]byte("AWS4"+c.SecretAccessKey), date)
	k = hmacSHA256(k, region)
	k = hmacSHA256(k, service)
	k = hmacSHA256(k, "aws4_request")
	sig := hex.EncodeToString(hmacSHA256(k, sts))

	req.Header.Set("Authorization", sigV4Algo+" Credential="+c.AccessKeyID+"/"+scope+", SignedHeaders="+signed.String()+", Signature="+sig)
}

func hmacSHA256(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}

// canonicalHeaderValue joins values with ',' after trimming them and
// collapsing inner runs of spaces.
func canonicalHeaderValue(vs []string) string {
	out := make([]string, len(vs))
	for i, v := range vs {
		out[i] = strings.Join(strings.Fields(v), " ")
	}
	return strings.Join(out, ",")
}

// canonicalURI URI-encodes every segment of an escaped path.
func canonicalURI(escapedPath string) string {
	if escapedPath == "" {
		return "/"
	}
	segs := strings.Split(escapedPath, "/")
	for i, s := range segs {
		segs[i] = URIEncode(s)
	}
	return strings.Join(segs, "/")
}

// canonicalQuery sorts the query parameters by name, then value, with both
// URI-encoded.
func canonicalQuery(raw string) string {
	if raw == "" {
		return ""
	}
	type kv struct{ k, v string }
	var ps []kv
	for _, p := range strings.Split(raw, "&") {
		if p == "" {
			continue
		}
		k, v, _ := strings.Cut(p, "=")
		ps = append(ps, kv{URIEncode(unescape(k)), URIEncode(unescape(v))})
	}
	sort.Slice(ps, func(i, j int) bool {
		if ps[i].k != ps[j].k {
			return ps[i].k < ps[j].k
		}
		return ps[i].v < ps[j].v
	})
	var b strings.Builder
	for i, p := range ps {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(p.k)
		b.WriteByte('=')
		b.WriteString(p.v)
	}
	return b.String()
}

func unescape(s string) string {
	if !strings.ContainsAny(s, "%+") {
		return s
	}
	var b []byte
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '+':
			b = append(b, ' ')
		case c == '%' && i+2 < len(s) && isHex(s[i+1]) && isHex(s[i+2]):
			b = append(b, unhex(s[i+1])<<4|unhex(s[i+2]))
			i += 2
		default:
			b = append(b, c)
		}
	}
	return string(b)
}

func isHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

func unhex(c byte) byte {
	switch {
	case c >= 'a':
		return c - 'a' + 10
	case c >= 'A':
		return c - 'A' + 10
	}
	return c - '0'
}

// URIEncode percent-encodes everything except the RFC 3986 unreserved
// characters (A-Z a-z 0-9 - _ . ~), with upper-case hex, as SigV4 requires.
func URIEncode(s string) string {
	n := 0
	for i := 0; i < len(s); i++ {
		if !unreserved(s[i]) {
			n++
		}
	}
	if n == 0 {
		return s
	}
	const hexd = "0123456789ABCDEF"
	b := make([]byte, 0, len(s)+2*n)
	for i := 0; i < len(s); i++ {
		c := s[i]
		if unreserved(c) {
			b = append(b, c)
		} else {
			b = append(b, '%', hexd[c>>4], hexd[c&15])
		}
	}
	return string(b)
}

func unreserved(c byte) bool {
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.' || c == '~'
}
