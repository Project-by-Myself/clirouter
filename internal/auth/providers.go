package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// refreshCredential returns an updated copy of c. It must not mutate c.
func (m *Manager) refreshCredential(ctx context.Context, c *Credential) (*Credential, error) {
	switch c.Type {
	case TypeCodex:
		return m.refreshCodex(ctx, c)
	case TypeClaude:
		return m.refreshClaude(ctx, c)
	case TypeCopilot:
		return m.refreshCopilot(ctx, c)
	case TypeGeminiCLI:
		return m.refreshGoogle(ctx, c)
	}
	return nil, fmt.Errorf("unknown credential type %q", c.Type)
}

type tokenResp struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Scope        string `json:"scope"`
}

func (m *Manager) expiry(t tokenResp) time.Time {
	if t.ExpiresIn > 0 {
		return m.now().Add(time.Duration(t.ExpiresIn) * time.Second)
	}
	if e := jwtExpiry(t.AccessToken); !e.IsZero() {
		return e
	}
	return m.now().Add(time.Hour)
}

func needRefreshToken(c *Credential) error {
	if c.RefreshToken == "" {
		return errors.New("no refresh token stored; log in again")
	}
	return nil
}

// ---- Codex / ChatGPT ----
//
// The resulting token is for the ChatGPT backend used by Codex. OpenAI may
// restrict that backend to its own clients; requests from clirouter carry an
// honest User-Agent and can be rejected.

func (m *Manager) applyCodex(c *Credential, t tokenResp) error {
	if t.AccessToken == "" {
		return errors.New("token response without access_token")
	}
	c.AccessToken = t.AccessToken
	if t.RefreshToken != "" {
		c.RefreshToken = t.RefreshToken
	}
	if t.IDToken != "" {
		c.IDToken = t.IDToken
		email, acct, plan := codexIdentity(t.IDToken)
		if email != "" {
			c.Email = email
		}
		if acct != "" {
			c.AccountID = acct
		}
		if plan != "" {
			c.Plan = plan
		}
	}
	c.ExpiresAt = m.expiry(t)
	return nil
}

func (m *Manager) exchangeCodex(ctx context.Context, code, verifier string) (*Credential, error) {
	var t tokenResp
	err := m.doJSON(ctx, http.MethodPost, CodexTokenURL, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {CodexRedirectURI},
		"client_id":     {CodexClientID},
		"code_verifier": {verifier},
	}, &t)
	if err != nil {
		return nil, err
	}
	c := &Credential{Type: TypeCodex}
	if err := m.applyCodex(c, t); err != nil {
		return nil, err
	}
	return c, nil
}

func (m *Manager) refreshCodex(ctx context.Context, old *Credential) (*Credential, error) {
	if err := needRefreshToken(old); err != nil {
		return nil, err
	}
	var t tokenResp
	err := m.doJSON(ctx, http.MethodPost, CodexTokenURL, map[string]string{
		"client_id":     CodexClientID,
		"grant_type":    "refresh_token",
		"refresh_token": old.RefreshToken,
		"scope":         "openid profile email",
	}, &t)
	if err != nil {
		return nil, err
	}
	c := old.clone()
	return c, m.applyCodex(c, t)
}

// ---- Claude subscription ----
//
// Endpoints are unverified public knowledge. Anthropic may restrict
// subscription OAuth tokens to its own clients (Claude Code); clirouter does
// not disguise itself, so such requests may be refused upstream.

type claudeTokenResp struct {
	tokenResp
	Account struct {
		UUID         string `json:"uuid"`
		EmailAddress string `json:"email_address"`
	} `json:"account"`
	Organization struct {
		UUID string `json:"uuid"`
		Name string `json:"name"`
	} `json:"organization"`
}

func (m *Manager) applyClaude(c *Credential, t claudeTokenResp) error {
	if t.AccessToken == "" {
		return errors.New("token response without access_token")
	}
	c.AccessToken = t.AccessToken
	if t.RefreshToken != "" {
		c.RefreshToken = t.RefreshToken
	}
	c.ExpiresAt = m.expiry(t.tokenResp)
	if t.Account.EmailAddress != "" {
		c.Email = t.Account.EmailAddress
	}
	if t.Organization.UUID != "" || t.Organization.Name != "" {
		if c.Extra == nil {
			c.Extra = map[string]string{}
		}
		if t.Organization.UUID != "" {
			c.Extra["organization_id"] = t.Organization.UUID
		}
		if t.Organization.Name != "" {
			c.Extra["organization"] = t.Organization.Name
		}
	}
	return nil
}

func (m *Manager) exchangeClaude(ctx context.Context, code, state, verifier string) (*Credential, error) {
	var t claudeTokenResp
	err := m.doJSON(ctx, http.MethodPost, ClaudeTokenURL, map[string]string{
		"grant_type":    "authorization_code",
		"code":          code,
		"state":         state,
		"client_id":     ClaudeClientID,
		"redirect_uri":  ClaudeRedirectURI,
		"code_verifier": verifier,
	}, &t)
	if err != nil {
		return nil, err
	}
	c := &Credential{Type: TypeClaude}
	return c, m.applyClaude(c, t)
}

func (m *Manager) refreshClaude(ctx context.Context, old *Credential) (*Credential, error) {
	if err := needRefreshToken(old); err != nil {
		return nil, err
	}
	var t claudeTokenResp
	err := m.doJSON(ctx, http.MethodPost, ClaudeTokenURL, map[string]string{
		"grant_type":    "refresh_token",
		"refresh_token": old.RefreshToken,
		"client_id":     ClaudeClientID,
	}, &t)
	if err != nil {
		return nil, err
	}
	c := old.clone()
	return c, m.applyClaude(c, t)
}

// ---- GitHub Copilot ----
//
// The GitHub OAuth token is stored as RefreshToken; the short-lived Copilot
// token is the AccessToken. copilot_internal is an undocumented endpoint and
// the Copilot API may reject clients that do not present a known editor
// identity. clirouter sends Editor-Version: clirouter/<version> and nothing else.

type copilotTokenResp struct {
	Token     string `json:"token"`
	ExpiresAt int64  `json:"expires_at"`
	RefreshIn int64  `json:"refresh_in"`
	Endpoints struct {
		API string `json:"api"`
	} `json:"endpoints"`
	SKU string `json:"sku"`
}

func (m *Manager) refreshCopilot(ctx context.Context, old *Credential) (*Credential, error) {
	if err := needRefreshToken(old); err != nil {
		return nil, err
	}
	var t copilotTokenResp
	err := m.doJSON(ctx, http.MethodGet, CopilotTokenURL, nil, &t,
		withHeader("Authorization", "token "+old.RefreshToken))
	if err != nil {
		var he *HTTPError
		if errors.As(err, &he) && (he.Status == 401 || he.Status == 403 || he.Status == 404) {
			return nil, fmt.Errorf("copilot token: %w (GitHub token revoked or no Copilot subscription)", err)
		}
		return nil, fmt.Errorf("copilot token: %w", err)
	}
	if t.Token == "" {
		return nil, errors.New("copilot token: empty token in response")
	}
	c := old.clone()
	c.AccessToken = t.Token
	if t.ExpiresAt > 0 {
		c.ExpiresAt = time.Unix(t.ExpiresAt, 0)
	} else {
		c.ExpiresAt = m.now().Add(25 * time.Minute)
	}
	if c.Extra == nil {
		c.Extra = map[string]string{}
	}
	if t.Endpoints.API != "" {
		c.Extra["api_base"] = strings.TrimRight(t.Endpoints.API, "/")
	}
	if t.SKU != "" {
		c.Plan = t.SKU
	}
	return c, nil
}

// ---- Google (Gemini CLI / Code Assist) ----
//
// Code Assist (cloudcode-pa) is an internal Google API used by gemini-cli;
// Google may restrict it to its own clients.

func (m *Manager) exchangeGoogle(ctx context.Context, code, verifier, redirectURI string) (*Credential, error) {
	var t tokenResp
	err := m.doJSON(ctx, http.MethodPost, GoogleTokenURL, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {GeminiClientID},
		"client_secret": {GeminiClientSecret},
		"code_verifier": {verifier},
	}, &t)
	if err != nil {
		return nil, err
	}
	if t.AccessToken == "" {
		return nil, errors.New("token response without access_token")
	}
	return &Credential{Type: TypeGeminiCLI, AccessToken: t.AccessToken, RefreshToken: t.RefreshToken,
		IDToken: t.IDToken, ExpiresAt: m.expiry(t)}, nil
}

func (m *Manager) refreshGoogle(ctx context.Context, old *Credential) (*Credential, error) {
	if err := needRefreshToken(old); err != nil {
		return nil, err
	}
	var t tokenResp
	err := m.doJSON(ctx, http.MethodPost, GoogleTokenURL, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {old.RefreshToken},
		"client_id":     {GeminiClientID},
		"client_secret": {GeminiClientSecret},
	}, &t)
	if err != nil {
		return nil, err
	}
	if t.AccessToken == "" {
		return nil, errors.New("token response without access_token")
	}
	c := old.clone()
	c.AccessToken = t.AccessToken
	if t.RefreshToken != "" {
		c.RefreshToken = t.RefreshToken
	}
	if t.IDToken != "" {
		c.IDToken = t.IDToken
	}
	c.ExpiresAt = m.expiry(t)
	return c, nil
}

func (m *Manager) googleEmail(ctx context.Context, token string) string {
	var u struct {
		Email string `json:"email"`
	}
	if err := m.doJSON(ctx, http.MethodGet, GoogleUserInfoURL, nil, &u,
		withHeader("Authorization", "Bearer "+token)); err != nil {
		return ""
	}
	return u.Email
}

// projectRef accepts either "proj-id" or {"id":"proj-id",...}.
type projectRef string

func (p *projectRef) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		*p = projectRef(s)
		return nil
	}
	var o struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(b, &o); err != nil {
		return err
	}
	*p = projectRef(o.ID)
	return nil
}

type codeAssistTier struct {
	ID        string `json:"id"`
	IsDefault bool   `json:"isDefault"`
}

// setupCodeAssist resolves the Code Assist project via loadCodeAssist, and
// onboards the user to a tier when no project is assigned yet. Returns
// (project, tier).
func (m *Manager) setupCodeAssist(ctx context.Context, token, wantProject string) (string, string, error) {
	auth := withHeader("Authorization", "Bearer "+token)
	meta := map[string]any{"pluginType": "GEMINI"}
	if wantProject != "" {
		meta["duetProject"] = wantProject
	}
	loadReq := map[string]any{"metadata": meta}
	if wantProject != "" {
		loadReq["cloudaicompanionProject"] = wantProject
	}
	var lr struct {
		CurrentTier             *codeAssistTier  `json:"currentTier"`
		AllowedTiers            []codeAssistTier `json:"allowedTiers"`
		CloudaicompanionProject projectRef       `json:"cloudaicompanionProject"`
	}
	if err := m.doJSON(ctx, http.MethodPost, CodeAssistBaseURL+"/v1internal:loadCodeAssist", loadReq, &lr, auth); err != nil {
		return "", "", fmt.Errorf("loadCodeAssist: %w", err)
	}
	if lr.CurrentTier != nil && lr.CloudaicompanionProject != "" {
		return string(lr.CloudaicompanionProject), lr.CurrentTier.ID, nil
	}
	tier := GeminiFreeTierID
	for _, t := range lr.AllowedTiers {
		if t.IsDefault && t.ID != "" {
			tier = t.ID
		}
	}
	if lr.CurrentTier != nil && lr.CurrentTier.ID != "" {
		tier = lr.CurrentTier.ID
	}
	onReq := map[string]any{"tierId": tier, "metadata": meta}
	if wantProject != "" {
		onReq["cloudaicompanionProject"] = wantProject
	}
	for {
		var op struct {
			Name     string `json:"name"`
			Done     bool   `json:"done"`
			Response struct {
				CloudaicompanionProject projectRef `json:"cloudaicompanionProject"`
			} `json:"response"`
		}
		if err := m.doJSON(ctx, http.MethodPost, CodeAssistBaseURL+"/v1internal:onboardUser", onReq, &op, auth); err != nil {
			return "", "", fmt.Errorf("onboardUser: %w", err)
		}
		if op.Done {
			p := string(op.Response.CloudaicompanionProject)
			if p == "" {
				p = wantProject
			}
			if p == "" {
				return "", tier, errors.New("onboardUser finished without a project; set a Google Cloud project and log in again")
			}
			return p, tier, nil
		}
		select {
		case <-ctx.Done():
			return "", tier, ctx.Err()
		case <-time.After(5 * m.pollUnit):
		}
	}
}
