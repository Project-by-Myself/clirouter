package cloudauth

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// AWS SigV4 test suite, "get-vanilla" (aws-sig-v4-test-suite).
func TestSignV4GetVanilla(t *testing.T) {
	req, _ := http.NewRequest("GET", "https://example.amazonaws.com/", nil)
	req.Header = http.Header{}
	c := AWSCredentials{AccessKeyID: "AKIDEXAMPLE", SecretAccessKey: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"}
	SignV4(req, nil, c, "us-east-1", "service", time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC))
	want := "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/service/aws4_request, SignedHeaders=host;x-amz-date, Signature=5fa00fa31553b73ebf1942676e86291e8372ff2a2260956d9b8aae1d763fbf31"
	if got := req.Header.Get("Authorization"); got != want {
		t.Fatalf("Authorization\n got %s\nwant %s", got, want)
	}
	if req.Header.Get("X-Amz-Date") != "20150830T123600Z" {
		t.Fatalf("X-Amz-Date = %q", req.Header.Get("X-Amz-Date"))
	}
}

// The worked example of the AWS General Reference ("Create a signed AWS API
// request"): IAM ListUsers with a query string and a Content-Type header.
func TestSignV4IAMExample(t *testing.T) {
	req, _ := http.NewRequest("GET", "https://iam.amazonaws.com/?Action=ListUsers&Version=2010-05-08", nil)
	req.Header = http.Header{"Content-Type": {"application/x-www-form-urlencoded; charset=utf-8"}}
	c := AWSCredentials{AccessKeyID: "AKIDEXAMPLE", SecretAccessKey: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"}
	SignV4(req, nil, c, "us-east-1", "iam", time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC))
	want := "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/iam/aws4_request, SignedHeaders=content-type;host;x-amz-date, Signature=5d672d79c15b13162d9279b0855cfba6789a8edb4c82c400e06b5924a6f2b5d7"
	if got := req.Header.Get("Authorization"); got != want {
		t.Fatalf("Authorization\n got %s\nwant %s", got, want)
	}
}

// A Bedrock invoke: the ':' of the model id is escaped in the URL and the
// escaped path is encoded again for signing; the session token is signed.
// Known answer computed with this implementation and cross-checked against
// the canonical request below.
func TestSignV4BedrockPathAndSessionToken(t *testing.T) {
	req, _ := http.NewRequest("POST", "https://bedrock-runtime.us-west-2.amazonaws.com/model/us.anthropic.claude-sonnet-4-5-20250929-v1%3A0/invoke", nil)
	req.Header = http.Header{"Content-Type": {"application/json"}, "Accept": {"application/json"}}
	c := AWSCredentials{AccessKeyID: "AKIDEXAMPLE", SecretAccessKey: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY", SessionToken: "session-token"}
	body := []byte(`{"anthropic_version":"bedrock-2023-05-31","max_tokens":10,"messages":[]}`)
	SignV4(req, body, c, "us-west-2", "bedrock", time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC))
	auth := req.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20250102/us-west-2/bedrock/aws4_request, SignedHeaders=content-type;host;x-amz-date;x-amz-security-token, Signature=") {
		t.Fatalf("Authorization = %s", auth)
	}
	if req.Header.Get("X-Amz-Security-Token") != "session-token" {
		t.Fatal("session token header missing")
	}
	if got := canonicalURI(req.URL.EscapedPath()); got != "/model/us.anthropic.claude-sonnet-4-5-20250929-v1%253A0/invoke" {
		t.Fatalf("canonical URI = %s", got)
	}
	// Recompute the signature independently from the documented steps.
	ph := sha256.Sum256(body)
	creq := "POST\n/model/us.anthropic.claude-sonnet-4-5-20250929-v1%253A0/invoke\n\n" +
		"content-type:application/json\nhost:bedrock-runtime.us-west-2.amazonaws.com\nx-amz-date:20250102T030405Z\nx-amz-security-token:session-token\n\n" +
		"content-type;host;x-amz-date;x-amz-security-token\n" + hexs(ph[:])
	ch := sha256.Sum256([]byte(creq))
	sts := "AWS4-HMAC-SHA256\n20250102T030405Z\n20250102/us-west-2/bedrock/aws4_request\n" + hexs(ch[:])
	k := hmacSHA256([]byte("AWS4"+c.SecretAccessKey), "20250102")
	k = hmacSHA256(hmacSHA256(hmacSHA256(k, "us-west-2"), "bedrock"), "aws4_request")
	if want := hexs(hmacSHA256(k, sts)); !strings.HasSuffix(auth, "Signature="+want) {
		t.Fatalf("signature mismatch: %s, want %s", auth, want)
	}
}

func hexs(b []byte) string {
	const d = "0123456789abcdef"
	out := make([]byte, 0, 2*len(b))
	for _, c := range b {
		out = append(out, d[c>>4], d[c&15])
	}
	return string(out)
}

func TestCanonicalQuerySorts(t *testing.T) {
	if got := canonicalQuery("b=2&a=x%20y&a=1"); got != "a=1&a=x%20y&b=2" {
		t.Fatalf("got %s", got)
	}
}

func TestParseAWSKey(t *testing.T) {
	if _, ok, _ := ParseAWSKey("ABSKexample"); ok {
		t.Fatal("bearer key parsed as aws")
	}
	c, ok, err := ParseAWSKey("aws:AKID:se/cr+et:tok")
	if !ok || err != nil || c.AccessKeyID != "AKID" || c.SecretAccessKey != "se/cr+et" || c.SessionToken != "tok" {
		t.Fatalf("%+v %v %v", c, ok, err)
	}
	if _, _, err := ParseAWSKey("aws:AKID"); err == nil || strings.Contains(err.Error(), "AKID") {
		t.Fatalf("bad key error must not echo the key: %v", err)
	}
	t.Setenv("AWS_ACCESS_KEY_ID", "AKENV")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "SECENV")
	t.Setenv("AWS_SESSION_TOKEN", "")
	if c, _, err := ParseAWSKey("aws:env"); err != nil || c.AccessKeyID != "AKENV" || c.SessionToken != "" {
		t.Fatalf("%+v %v", c, err)
	}
}

var (
	testKeyOnce sync.Once
	testKey     *rsa.PrivateKey
)

func rsaKey(t *testing.T) *rsa.PrivateKey {
	testKeyOnce.Do(func() {
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			panic(err)
		}
		testKey = k
	})
	return testKey
}

// WriteTestServiceAccount writes a service-account JSON key for tokenURI and
// returns its path.
func writeSA(t *testing.T, tokenURI string) string {
	t.Helper()
	der, _ := x509.MarshalPKCS8PrivateKey(rsaKey(t))
	sa := map[string]string{
		"type": "service_account", "project_id": "proj-1", "private_key_id": "kid-1",
		"private_key":  string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
		"client_email": "svc@proj-1.iam.gserviceaccount.com", "token_uri": tokenURI,
	}
	b, _ := json.Marshal(sa)
	p := filepath.Join(t.TempDir(), "sa.json")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSignJWTVerifies(t *testing.T) {
	k := rsaKey(t)
	jwt, err := SignJWT(k, "kid-1", map[string]any{"iss": "a", "iat": 1})
	if err != nil {
		t.Fatal(err)
	}
	verifyJWT(t, jwt, &k.PublicKey)
}

func verifyJWT(t *testing.T, jwt string, pub *rsa.PublicKey) (hdr, claims map[string]any) {
	t.Helper()
	hdr, claims, err := parseJWT(jwt, pub)
	if err != nil {
		t.Fatal(err)
	}
	return hdr, claims
}

func parseJWT(jwt string, pub *rsa.PublicKey) (hdr, claims map[string]any, err error) {
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		return nil, nil, fmt.Errorf("jwt has %d parts", len(parts))
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, nil, err
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, sum[:], sig); err != nil {
		return nil, nil, fmt.Errorf("bad signature: %v", err)
	}
	hb, _ := base64.RawURLEncoding.DecodeString(parts[0])
	cb, _ := base64.RawURLEncoding.DecodeString(parts[1])
	json.Unmarshal(hb, &hdr)
	json.Unmarshal(cb, &claims)
	if hdr["alg"] != "RS256" || hdr["typ"] != "JWT" {
		return nil, nil, fmt.Errorf("header %v", hdr)
	}
	return hdr, claims, nil
}

type tokenServer struct {
	*httptest.Server
	hits      atomic.Int64
	expiresIn int64
	delay     time.Duration
	fail      atomic.Bool
}

func newTokenServer(t *testing.T) *tokenServer {
	ts := &tokenServer{expiresIn: 3600}
	ts.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := ts.hits.Add(1)
		time.Sleep(ts.delay)
		if ts.fail.Load() {
			w.WriteHeader(400)
			w.Write([]byte(`{"error":"invalid_grant","error_description":"Invalid JWT Signature."}`))
			return
		}
		if err := r.ParseForm(); err != nil || r.Form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:jwt-bearer" {
			http.Error(w, "bad grant", 400)
			return
		}
		hdr, claims, err := parseJWT(r.Form.Get("assertion"), &rsaKey(t).PublicKey)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if hdr["kid"] != "kid-1" || claims["iss"] != "svc@proj-1.iam.gserviceaccount.com" || claims["scope"] != CloudPlatformScope || claims["aud"] != ts.URL+"/token" {
			http.Error(w, "bad claims", 400)
			return
		}
		if exp, iat := claims["exp"].(float64), claims["iat"].(float64); exp-iat != 3600 {
			http.Error(w, "bad lifetime", 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"access_token": "ya29.test-" + string(rune('a'+n-1)), "expires_in": ts.expiresIn, "token_type": "Bearer"})
	}))
	t.Cleanup(ts.Close)
	return ts
}

func TestServiceAccountTokenCachingAndSingleFlight(t *testing.T) {
	ts := newTokenServer(t)
	ts.delay = 50 * time.Millisecond
	sa := NewServiceAccount(writeSA(t, ts.URL+"/token"), ts.Client())
	now := time.Unix(1_700_000_000, 0)
	var mu sync.Mutex
	sa.now = func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	if m := sa.Meta(); m["project"] != "proj-1" || m["email"] == "" {
		t.Fatalf("meta %v", m)
	}

	var wg sync.WaitGroup
	toks := make([]string, 20)
	for i := range toks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tok, err := sa.Token()
			if err != nil {
				t.Error(err)
			}
			toks[i] = tok
		}()
	}
	wg.Wait()
	if ts.hits.Load() != 1 {
		t.Fatalf("single-flight: %d token requests", ts.hits.Load())
	}
	for _, tok := range toks {
		if tok != "ya29.test-a" {
			t.Fatalf("token %q", tok)
		}
	}
	// Cached until 60 s before expiry.
	mu.Lock()
	now = now.Add(3600*time.Second - 61*time.Second)
	mu.Unlock()
	if tok, _ := sa.Token(); tok != "ya29.test-a" || ts.hits.Load() != 1 {
		t.Fatalf("expected cached token, got %q after %d hits", tok, ts.hits.Load())
	}
	mu.Lock()
	now = now.Add(2 * time.Second)
	mu.Unlock()
	if tok, _ := sa.Token(); tok != "ya29.test-b" || ts.hits.Load() != 2 {
		t.Fatalf("expected refreshed token, got %q after %d hits", tok, ts.hits.Load())
	}
	// Refresh forces a new token.
	if err := sa.Refresh(); err != nil || ts.hits.Load() != 3 {
		t.Fatalf("refresh: %v, hits %d", err, ts.hits.Load())
	}
	if tok, _ := sa.Token(); tok != "ya29.test-c" {
		t.Fatalf("token after refresh %q", tok)
	}
}

func TestServiceAccountErrors(t *testing.T) {
	ts := newTokenServer(t)
	ts.fail.Store(true)
	sa := NewServiceAccount(writeSA(t, ts.URL+"/token"), ts.Client())
	_, err := sa.Token()
	if err == nil || !strings.Contains(err.Error(), "invalid_grant") || strings.Contains(err.Error(), "PRIVATE KEY") {
		t.Fatalf("err = %v", err)
	}

	missing := NewServiceAccount(filepath.Join(t.TempDir(), "nope.json"), nil)
	if _, err := missing.Token(); err == nil || !strings.Contains(err.Error(), "nope.json") {
		t.Fatalf("missing file: %v", err)
	}

	t.Setenv("SA_JSON_BAD", `{"type":"service_account","client_email":"x","private_key":"-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----\n"}`)
	bad := NewServiceAccount("env:SA_JSON_BAD", nil)
	if _, err := bad.Token(); err == nil || strings.Contains(err.Error(), "AAAA") {
		t.Fatalf("bad key: %v", err)
	}
}

func TestServiceAccountFromEnv(t *testing.T) {
	ts := newTokenServer(t)
	b, _ := os.ReadFile(writeSA(t, ts.URL+"/token"))
	t.Setenv("SA_JSON_TEST", string(b))
	sa := NewServiceAccount("env:SA_JSON_TEST", ts.Client())
	if tok, err := sa.Token(); err != nil || tok != "ya29.test-a" {
		t.Fatalf("%q %v", tok, err)
	}
	if SharedServiceAccount("env:SA_JSON_TEST", "") != SharedServiceAccount("env:SA_JSON_TEST", "") {
		t.Fatal("shared sources must be reused")
	}
}
