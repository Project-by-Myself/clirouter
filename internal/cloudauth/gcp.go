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
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Project-by-Myself/clirouter/internal/redact"
)

// CloudPlatformScope is the OAuth2 scope Vertex AI requires.
const CloudPlatformScope = "https://www.googleapis.com/auth/cloud-platform"

const defaultTokenURI = "https://oauth2.googleapis.com/token"

// refreshMargin renews a token this long before it expires.
const refreshMargin = 60 * time.Second

// serviceAccountKey is the subset of a Google service-account JSON key file
// that the JWT-bearer flow needs.
type serviceAccountKey struct {
	Type         string `json:"type"`
	ProjectID    string `json:"project_id"`
	PrivateKeyID string `json:"private_key_id"`
	PrivateKey   string `json:"private_key"`
	ClientEmail  string `json:"client_email"`
	TokenURI     string `json:"token_uri"`
}

// ServiceAccount is a token source for a Google service account. Access
// tokens are cached until 60 s before expiry; concurrent callers share one
// refresh. It satisfies router.TokenSource.
type ServiceAccount struct {
	ref    string // "<path>" or "env:<VAR>", never the key itself
	client *http.Client
	now    func() time.Time

	mu      sync.Mutex
	key     *rsa.PrivateKey
	sa      serviceAccountKey
	meta    map[string]string
	loadErr error
	token   string
	expiry  time.Time
	flight  chan struct{} // non-nil while a refresh is running
	lastErr error
}

var (
	saMu   sync.Mutex
	saPool = map[string]*ServiceAccount{}
)

// SharedServiceAccount returns the token source for ref ("<path>" or
// "env:<VAR>", i.e. a vertex key without its "sa:" prefix). Sources are
// shared process-wide per (ref, proxy), so a config reload keeps cached
// tokens. The key is (re)read when a token is fetched, so a rotated key
// file is picked up at the next refresh.
func SharedServiceAccount(ref, proxy string) *ServiceAccount {
	saMu.Lock()
	defer saMu.Unlock()
	k := ref + "\x00" + proxy
	if s := saPool[k]; s != nil {
		return s
	}
	s := NewServiceAccount(ref, tokenClient(proxy))
	saPool[k] = s
	return s
}

func tokenClient(proxy string) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if proxy != "" {
		if u, err := url.Parse(proxy); err == nil {
			tr.Proxy = http.ProxyURL(u)
		}
	}
	return &http.Client{Transport: tr, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// NewServiceAccount builds an unshared token source (tests, tools).
func NewServiceAccount(ref string, client *http.Client) *ServiceAccount {
	if client == nil {
		client = tokenClient("")
	}
	s := &ServiceAccount{ref: ref, client: client, now: time.Now}
	s.mu.Lock()
	s.reloadLocked()
	s.mu.Unlock()
	return s
}

// reloadLocked reads and parses the key. On failure the previous key (if
// any) stays in use.
func (s *ServiceAccount) reloadLocked() {
	sa, key, err := loadServiceAccount(s.ref)
	if err != nil {
		s.loadErr = err
		return
	}
	s.sa, s.key, s.loadErr = sa, key, nil
	s.meta = map[string]string{"project": sa.ProjectID, "email": sa.ClientEmail}
}

func loadServiceAccount(ref string) (serviceAccountKey, *rsa.PrivateKey, error) {
	var sa serviceAccountKey
	var raw []byte
	if name, ok := strings.CutPrefix(ref, "env:"); ok {
		v := os.Getenv(name)
		if v == "" {
			return sa, nil, fmt.Errorf("service account: environment variable %s is empty", name)
		}
		raw = []byte(v)
	} else {
		path := ref
		if rest, ok := strings.CutPrefix(path, "~/"); ok {
			if home, err := os.UserHomeDir(); err == nil {
				path = filepath.Join(home, rest)
			}
		}
		b, err := os.ReadFile(path)
		if err != nil {
			// The error names only the path, which is not secret.
			return sa, nil, fmt.Errorf("service account: %w", err)
		}
		raw = b
	}
	if err := json.Unmarshal(raw, &sa); err != nil {
		return sa, nil, errors.New("service account: invalid JSON key")
	}
	if sa.Type != "" && sa.Type != "service_account" {
		return sa, nil, fmt.Errorf("service account: key type is %q, want service_account", sa.Type)
	}
	if sa.ClientEmail == "" || sa.PrivateKey == "" {
		return sa, nil, errors.New("service account: client_email and private_key are required")
	}
	if sa.TokenURI == "" {
		sa.TokenURI = defaultTokenURI
	}
	key, err := ParseRSAPrivateKey([]byte(sa.PrivateKey))
	if err != nil {
		return sa, nil, err
	}
	return sa, key, nil
}

// ParseRSAPrivateKey parses a PEM PKCS#8 or PKCS#1 RSA private key.
func ParseRSAPrivateKey(pemBytes []byte) (*rsa.PrivateKey, error) {
	blk, _ := pem.Decode(pemBytes)
	if blk == nil {
		return nil, errors.New("service account: private_key is not PEM")
	}
	if k, err := x509.ParsePKCS8PrivateKey(blk.Bytes); err == nil {
		rk, ok := k.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("service account: private_key is not an RSA key")
		}
		return rk, nil
	}
	if k, err := x509.ParsePKCS1PrivateKey(blk.Bytes); err == nil {
		return k, nil
	}
	return nil, errors.New("service account: cannot parse private_key")
}

// Kind labels these accounts in the dashboard.
func (s *ServiceAccount) Kind() string { return "service_account" }

// Meta returns {"project", "email"} of the key. The map is shared and must
// not be modified.
func (s *ServiceAccount) Meta() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.meta
}

// Token returns a cached access token, fetching a new one when it is
// missing or within 60 s of expiry.
func (s *ServiceAccount) Token() (string, error) { return s.get(false) }

// Refresh discards the cached token and fetches a new one (after a 401).
func (s *ServiceAccount) Refresh() error {
	_, err := s.get(true)
	return err
}

func (s *ServiceAccount) get(force bool) (string, error) {
	s.mu.Lock()
	for {
		if !force && s.token != "" && s.now().Before(s.expiry.Add(-refreshMargin)) {
			t := s.token
			s.mu.Unlock()
			return t, nil
		}
		if s.flight == nil {
			break
		}
		// Someone else is refreshing: wait for it and use its result.
		ch := s.flight
		s.mu.Unlock()
		<-ch
		s.mu.Lock()
		if s.lastErr != nil {
			err := s.lastErr
			s.mu.Unlock()
			return "", err
		}
		force = false
	}
	ch := make(chan struct{})
	s.flight = ch
	s.reloadLocked()
	key, sa, loadErr := s.key, s.sa, s.loadErr
	s.mu.Unlock()

	var tok string
	var exp time.Time
	var err error
	if key == nil {
		err = loadErr
	} else {
		tok, exp, err = s.fetch(sa, key)
	}

	s.mu.Lock()
	s.flight = nil
	s.lastErr = err
	if err == nil {
		s.token, s.expiry = tok, exp
	}
	close(ch)
	s.mu.Unlock()
	return tok, err
}

func (s *ServiceAccount) fetch(sa serviceAccountKey, key *rsa.PrivateKey) (string, time.Time, error) {
	now := s.now()
	assertion, err := SignJWT(key, sa.PrivateKeyID, map[string]any{
		"iss":   sa.ClientEmail,
		"scope": CloudPlatformScope,
		"aud":   sa.TokenURI,
		"iat":   now.Unix(),
		"exp":   now.Add(time.Hour).Unix(),
	})
	if err != nil {
		return "", time.Time{}, err
	}
	form := url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"}, "assertion": {assertion}}
	resp, err := s.client.PostForm(sa.TokenURI, form)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("service account token: %s", redact.String(err.Error()))
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var tr struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
		Error       string `json:"error"`
		Desc        string `json:"error_description"`
	}
	_ = json.Unmarshal(body, &tr)
	if resp.StatusCode != http.StatusOK || tr.AccessToken == "" {
		msg := strings.TrimSpace(tr.Error + ": " + tr.Desc)
		if tr.Error == "" {
			msg = http.StatusText(resp.StatusCode)
		}
		return "", time.Time{}, fmt.Errorf("service account token: HTTP %d %s", resp.StatusCode, redact.String(msg))
	}
	if tr.ExpiresIn <= 0 {
		tr.ExpiresIn = 3600
	}
	return tr.AccessToken, now.Add(time.Duration(tr.ExpiresIn) * time.Second), nil
}

// SignJWT returns an RS256-signed compact JWT with the given claims.
func SignJWT(key *rsa.PrivateKey, kid string, claims map[string]any) (string, error) {
	hdr := map[string]string{"alg": "RS256", "typ": "JWT"}
	if kid != "" {
		hdr["kid"] = kid
	}
	hb, err := json.Marshal(hdr)
	if err != nil {
		return "", err
	}
	cb, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	enc := base64.RawURLEncoding
	signing := enc.EncodeToString(hb) + "." + enc.EncodeToString(cb)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return signing + "." + enc.EncodeToString(sig), nil
}
