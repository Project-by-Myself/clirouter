// Package auth manages OAuth/subscription accounts: login flows, an encrypted
// credential store and token refresh. After login every request identifies as
// clirouter (see UserAgent); nothing here imitates an official client.
package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Version is reported in the User-Agent of every request this package makes.
var Version = "0.1.0"

// UserAgent is the honest client identifier used after (and during) login.
func UserAgent() string { return "clirouter/" + Version }

const (
	TypeCodex     = "codex"
	TypeClaude    = "claude"
	TypeCopilot   = "copilot"
	TypeGeminiCLI = "gemini-cli"
)

func validType(t string) bool {
	switch t {
	case TypeCodex, TypeClaude, TypeCopilot, TypeGeminiCLI:
		return true
	}
	return false
}

// Credential is the persisted state of one OAuth account. Its fields are
// secrets; never log a Credential.
type Credential struct {
	Type         string            `json:"type"`
	AccessToken  string            `json:"access_token,omitempty"`
	RefreshToken string            `json:"refresh_token,omitempty"`
	ExpiresAt    time.Time         `json:"expires_at,omitzero"`
	IDToken      string            `json:"id_token,omitempty"`
	Email        string            `json:"email,omitempty"`
	AccountID    string            `json:"account_id,omitempty"` // ChatGPT account id (codex)
	Plan         string            `json:"plan,omitempty"`
	ProjectID    string            `json:"project_id,omitempty"` // Google Cloud project (gemini-cli)
	Extra        map[string]string `json:"extra,omitempty"`
	LastRefresh  time.Time         `json:"last_refresh,omitzero"`
}

func (c *Credential) clone() *Credential {
	if c == nil {
		return nil
	}
	n := *c
	n.Extra = maps.Clone(c.Extra)
	return &n
}

// ErrNotFound is returned by Store.Load for an unknown account.
var ErrNotFound = errors.New("credential not found")

var idRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@-]{0,127}$`)

// ValidAccountID reports whether id is safe to use as a file name.
func ValidAccountID(id string) bool { return idRe.MatchString(id) && !strings.Contains(id, "..") }

// Store persists credentials as <dir>/<id>.json encrypted with AES-256-GCM.
// The key lives in <dir>/.key and is created on first use.
type Store struct {
	dir  string
	mu   sync.Mutex
	aead cipher.AEAD
}

func NewStore(dataDir string) *Store { return &Store{dir: filepath.Join(dataDir, "auth")} }

func (s *Store) Dir() string { return s.dir }

func (s *Store) path(id string) string { return filepath.Join(s.dir, id+".json") }

func (s *Store) cipher() (cipher.AEAD, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.aead != nil {
		return s.aead, nil
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return nil, err
	}
	key, err := s.loadKey()
	if err != nil {
		return nil, err
	}
	blk, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	s.aead, err = cipher.NewGCM(blk)
	return s.aead, err
}

func (s *Store) loadKey() ([]byte, error) {
	kp := filepath.Join(s.dir, ".key")
	for range 3 {
		key, err := os.ReadFile(kp)
		if err == nil {
			if len(key) != 32 {
				return nil, fmt.Errorf("auth key %s: want 32 bytes, got %d", kp, len(key))
			}
			return key, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
		tmp, err := writeTemp(s.dir, ".key-*", key)
		if err != nil {
			return nil, err
		}
		// Link fails if another process created the key first; then re-read theirs.
		err = os.Link(tmp, kp)
		os.Remove(tmp)
		if err == nil {
			syncDir(s.dir)
			return key, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
	}
	return nil, errors.New("auth key: could not create or read key")
}

// Save encrypts and atomically writes the credential.
func (s *Store) Save(id string, c *Credential) error {
	if !ValidAccountID(id) {
		return fmt.Errorf("invalid account id %q", id)
	}
	aead, err := s.cipher()
	if err != nil {
		return err
	}
	plain, err := json.Marshal(c)
	if err != nil {
		return err
	}
	nonce := make([]byte, aead.NonceSize(), aead.NonceSize()+len(plain)+aead.Overhead())
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	// The account id is bound as associated data so files cannot be swapped.
	sealed := aead.Seal(nonce, nonce, plain, []byte(id))
	env, _ := json.Marshal(struct {
		V    int    `json:"v"`
		Data []byte `json:"data"`
	}{1, sealed})
	return atomicWrite(s.path(id), env)
}

// Load decrypts a credential. Missing files yield ErrNotFound.
func (s *Store) Load(id string) (*Credential, error) {
	if !ValidAccountID(id) {
		return nil, fmt.Errorf("invalid account id %q", id)
	}
	raw, err := os.ReadFile(s.path(id))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	aead, err := s.cipher()
	if err != nil {
		return nil, err
	}
	var env struct {
		V    int    `json:"v"`
		Data []byte `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil || env.V != 1 {
		return nil, fmt.Errorf("credential %s: unrecognized file format", id)
	}
	ns := aead.NonceSize()
	if len(env.Data) < ns+aead.Overhead() {
		return nil, fmt.Errorf("credential %s: truncated", id)
	}
	plain, err := aead.Open(nil, env.Data[:ns], env.Data[ns:], []byte(id))
	if err != nil {
		return nil, fmt.Errorf("credential %s: decryption failed (tampered or wrong key)", id)
	}
	var c Credential
	if err := json.Unmarshal(plain, &c); err != nil {
		return nil, fmt.Errorf("credential %s: %w", id, err)
	}
	return &c, nil
}

func (s *Store) Delete(id string) error {
	if !ValidAccountID(id) {
		return fmt.Errorf("invalid account id %q", id)
	}
	err := os.Remove(s.path(id))
	if errors.Is(err, os.ErrNotExist) {
		return ErrNotFound
	}
	if err == nil {
		syncDir(s.dir)
	}
	return err
}

// IDs lists stored account ids, sorted.
func (s *Store) IDs() ([]string, error) {
	ents, err := os.ReadDir(s.dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, e := range ents {
		if id, ok := strings.CutSuffix(e.Name(), ".json"); ok && e.Type().IsRegular() && ValidAccountID(id) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids, nil
}

func writeTemp(dir, pattern string, data []byte) (string, error) {
	f, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return "", err
	}
	name := f.Name()
	fail := func(err error) (string, error) {
		f.Close()
		os.Remove(name)
		return "", err
	}
	if err := f.Chmod(0o600); err != nil {
		return fail(err)
	}
	if _, err := f.Write(data); err != nil {
		return fail(err)
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		os.Remove(name)
		return "", err
	}
	return name, nil
}

// atomicWrite writes data to path via temp file + fsync + rename, mode 0600.
func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := writeTemp(dir, "."+filepath.Base(path)+".tmp-*", data)
	if err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	syncDir(dir)
	return nil
}

func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
}
