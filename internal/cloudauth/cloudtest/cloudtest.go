// Package cloudtest provides test doubles for cloud credentials: a fake
// Google service-account key and a local OAuth2 token endpoint that checks
// the JWT-bearer assertion. Only tests import it.
package cloudtest

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
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

var (
	keyOnce sync.Once
	key     *rsa.PrivateKey
)

// Key returns a process-wide RSA test key.
func Key() *rsa.PrivateKey {
	keyOnce.Do(func() {
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			panic(err)
		}
		key = k
	})
	return key
}

// TokenServer is a fake OAuth2 token endpoint. It answers a valid
// JWT-bearer grant signed with Key() with access token "ya29.test-<n>".
type TokenServer struct {
	*httptest.Server
	Hits atomic.Int64
}

func NewTokenServer(t testing.TB) *TokenServer {
	ts := &TokenServer{}
	ts.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := ts.Hits.Add(1)
		if err := r.ParseForm(); err != nil || r.Form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:jwt-bearer" {
			http.Error(w, `{"error":"unsupported_grant_type"}`, 400)
			return
		}
		if err := verify(r.Form.Get("assertion"), &Key().PublicKey); err != nil {
			http.Error(w, `{"error":"invalid_grant","error_description":"`+err.Error()+`"}`, 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"access_token":"ya29.test-` + strconv.FormatInt(n, 10) + `","expires_in":3600,"token_type":"Bearer"}`))
	}))
	t.Cleanup(ts.Close)
	return ts
}

// TokenURL is the token_uri to put in the service-account JSON.
func (ts *TokenServer) TokenURL() string { return ts.URL + "/token" }

func verify(jwt string, pub *rsa.PublicKey) error {
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		return errors.New("malformed JWT")
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return err
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	return rsa.VerifyPKCS1v15(pub, crypto.SHA256, sum[:], sig)
}

// ServiceAccountJSON returns a service-account key for project whose
// token_uri is tokenURI.
func ServiceAccountJSON(project, tokenURI string) []byte {
	der, _ := x509.MarshalPKCS8PrivateKey(Key())
	b, _ := json.Marshal(map[string]string{
		"type": "service_account", "project_id": project, "private_key_id": "kid-1",
		"private_key":  string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
		"client_email": "svc@" + project + ".iam.gserviceaccount.com", "token_uri": tokenURI,
	})
	return b
}

// WriteServiceAccount writes ServiceAccountJSON to a temp file and returns
// its path.
func WriteServiceAccount(t testing.TB, project, tokenURI string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "sa.json")
	if err := os.WriteFile(p, ServiceAccountJSON(project, tokenURI), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}
