package auth

import (
	"context"
	"errors"
	"fmt"
	"html"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	ModeRedirect  = "redirect"
	ModePasteCode = "paste_code"
	ModeDevice    = "device"

	StatePending = "pending"
	StateDone    = "done"
	StateError   = "error"
)

// StartRequest is the body of POST start.
type StartRequest struct {
	Type      string `json:"type"`
	AccountID string `json:"account_id,omitempty"`
	ProjectID string `json:"project_id,omitempty"` // gemini-cli: optional explicit Google Cloud project
}

// StartResponse is returned by POST start.
type StartResponse struct {
	FlowID          string    `json:"flow_id"`
	Type            string    `json:"type"`
	Mode            string    `json:"mode"`
	AuthURL         string    `json:"auth_url,omitempty"`
	UserCode        string    `json:"user_code,omitempty"`
	VerificationURI string    `json:"verification_uri,omitempty"`
	ExpiresAt       time.Time `json:"expires_at"`
}

// FlowStatus is returned by GET status.
type FlowStatus struct {
	State     string       `json:"state"`
	Type      string       `json:"type"`
	AccountID string       `json:"account_id,omitempty"`
	Error     string       `json:"error,omitempty"`
	Account   *AccountInfo `json:"account,omitempty"`
}

type flow struct {
	id, typ, accountID, mode     string
	authURL, userCode, verifURI  string
	verifier, state, redirectURI string
	wantProject                  string
	created                      time.Time

	ctx    context.Context
	cancel context.CancelFunc

	mu     sync.Mutex
	st     string
	busy   bool
	err    string
	result *AccountInfo
}

func (f *flow) status() FlowStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	fs := FlowStatus{State: f.st, Type: f.typ, Error: f.err, Account: f.result}
	if f.result != nil {
		fs.AccountID = f.result.AccountID
	}
	return fs
}

// StartFlow begins a login flow.
func (m *Manager) StartFlow(req StartRequest) (*StartResponse, error) {
	if !validType(req.Type) {
		return nil, fmt.Errorf("unknown type %q", req.Type)
	}
	if req.AccountID != "" && !ValidAccountID(req.AccountID) {
		return nil, fmt.Errorf("invalid account_id %q", req.AccountID)
	}
	m.expireFlows()
	f := &flow{id: randHex(16), typ: req.Type, accountID: req.AccountID, wantProject: req.ProjectID,
		created: m.now(), st: StatePending, state: randHex(16)}
	f.ctx, f.cancel = context.WithTimeout(context.Background(), m.flowTTL)
	var challenge string
	f.verifier, challenge = newPKCE()

	var err error
	switch req.Type {
	case TypeCodex:
		f.redirectURI = CodexRedirectURI
		q := url.Values{
			"response_type":              {"code"},
			"client_id":                  {CodexClientID},
			"redirect_uri":               {f.redirectURI},
			"scope":                      {CodexScope},
			"code_challenge":             {challenge},
			"code_challenge_method":      {"S256"},
			"id_token_add_organizations": {"true"},
			"codex_cli_simplified_flow":  {"true"},
			"state":                      {f.state},
		}
		f.authURL = CodexAuthorizeURL + "?" + q.Encode()
		f.mode = ModePasteCode
		if m.listenCallback(f, CodexCallbackAddr, callbackPath(CodexRedirectURI)) == nil {
			f.mode = ModeRedirect
		}
	case TypeClaude:
		f.redirectURI = ClaudeRedirectURI
		q := url.Values{
			"code":                  {"true"},
			"client_id":             {ClaudeClientID},
			"response_type":         {"code"},
			"redirect_uri":          {f.redirectURI},
			"scope":                 {ClaudeScope},
			"code_challenge":        {challenge},
			"code_challenge_method": {"S256"},
			"state":                 {f.state},
		}
		f.authURL = ClaudeAuthorizeURL + "?" + q.Encode()
		f.mode = ModePasteCode
	case TypeGeminiCLI:
		ln, lerr := net.Listen("tcp", net.JoinHostPort(GeminiCallbackHost, "0"))
		if lerr != nil {
			err = fmt.Errorf("open loopback listener: %w", lerr)
			break
		}
		port := ln.Addr().(*net.TCPAddr).Port
		f.redirectURI = "http://" + net.JoinHostPort(GeminiCallbackHost, strconv.Itoa(port)) + "/oauth2callback"
		m.serveCallback(f, []net.Listener{ln}, "/oauth2callback")
		q := url.Values{
			"response_type":         {"code"},
			"client_id":             {GeminiClientID},
			"redirect_uri":          {f.redirectURI},
			"scope":                 {GeminiScope},
			"access_type":           {"offline"},
			"prompt":                {"consent"},
			"code_challenge":        {challenge},
			"code_challenge_method": {"S256"},
			"state":                 {f.state},
		}
		f.authURL = GoogleAuthURL + "?" + q.Encode()
		f.mode = ModeRedirect
	case TypeCopilot:
		err = m.startDevice(f)
	}
	if err != nil {
		f.cancel()
		return nil, err
	}
	m.mu.Lock()
	m.flows[f.id] = f
	m.mu.Unlock()
	return &StartResponse{FlowID: f.id, Type: f.typ, Mode: f.mode, AuthURL: f.authURL, UserCode: f.userCode,
		VerificationURI: f.verifURI, ExpiresAt: f.created.Add(m.flowTTL)}, nil
}

func callbackPath(redirect string) string {
	if u, err := url.Parse(redirect); err == nil && u.Path != "" {
		return u.Path
	}
	return "/"
}

// listenCallback binds the loopback callback address. The redirect URI says
// "localhost", so [::1] is also tried best-effort. Failure means paste mode.
func (m *Manager) listenCallback(f *flow, addr, path string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		m.Logger.Info("oauth: callback port busy, using paste_code mode", "addr", addr, "err", err)
		return err
	}
	lns := []net.Listener{ln}
	if _, port, _ := net.SplitHostPort(addr); port != "0" {
		if ln6, err := net.Listen("tcp", net.JoinHostPort("::1", port)); err == nil {
			lns = append(lns, ln6)
		}
	}
	m.serveCallback(f, lns, path)
	return nil
}

func (m *Manager) serveCallback(f *flow, lns []net.Listener, path string) {
	mux := http.NewServeMux()
	mux.Handle(path, m.callbackHandler(f))
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	for _, ln := range lns {
		go srv.Serve(ln)
	}
	go func() {
		<-f.ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(sctx)
	}()
}

// callbackHandler receives the browser redirect for redirect-mode flows.
func (m *Manager) callbackHandler(f *flow) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		page := func(code int, title, msg string) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(code)
			fmt.Fprintf(w, "<!doctype html><meta charset=utf-8><title>clirouter</title><h2>%s</h2><p>%s</p>",
				html.EscapeString(title), html.EscapeString(msg))
		}
		if q.Get("state") != f.state {
			page(http.StatusBadRequest, "Login failed", "State mismatch. Start the login again from the dashboard.")
			return
		}
		if e := q.Get("error"); e != "" {
			msg := e
			if d := q.Get("error_description"); d != "" {
				msg += ": " + d
			}
			m.fail(f, errors.New(msg))
			page(http.StatusBadRequest, "Login failed", msg)
			return
		}
		code := q.Get("code")
		if code == "" {
			page(http.StatusBadRequest, "Login failed", "Missing authorization code.")
			return
		}
		if _, err := m.exchangeFlow(f, code, f.state); err != nil {
			page(http.StatusBadGateway, "Login failed", err.Error())
			return
		}
		page(http.StatusOK, "Login complete", "clirouter is now signed in. You can close this tab.")
	})
}

// parseCodeInput accepts a full redirect URL, a query string, "code#state" or a bare code.
func parseCodeInput(s string) (code, state string) {
	s = strings.TrimSpace(s)
	if strings.Contains(s, "code=") {
		raw := s
		if i := strings.IndexByte(raw, '?'); i >= 0 {
			raw = raw[i+1:]
		}
		if i := strings.IndexByte(raw, '#'); i >= 0 && !strings.Contains(raw[:i], "code=") {
			raw = raw[i+1:]
		}
		if v, err := url.ParseQuery(raw); err == nil && v.Get("code") != "" {
			code, state = v.Get("code"), v.Get("state")
			if c, st, ok := strings.Cut(code, "#"); ok && state == "" {
				code, state = c, st
			}
			return code, state
		}
	}
	if c, st, ok := strings.Cut(s, "#"); ok {
		return c, st
	}
	return s, ""
}

// CompleteFlow finishes a paste_code (or redirect, as fallback) flow with the
// user-supplied code or redirected URL.
func (m *Manager) CompleteFlow(ctx context.Context, flowID, input string) (*AccountInfo, error) {
	f := m.flow(flowID)
	if f == nil {
		return nil, errors.New("unknown or expired flow")
	}
	if f.mode == ModeDevice {
		if st := f.status(); st.State == StateDone {
			return st.Account, nil
		}
		return nil, errors.New("device flow completes automatically; poll status")
	}
	code, state := parseCodeInput(input)
	if code == "" {
		if st := f.status(); st.State == StateDone {
			return st.Account, nil
		}
		return nil, errors.New("code is required")
	}
	if state != "" && state != f.state {
		return nil, errors.New("state mismatch: the code belongs to a different login attempt")
	}
	if state == "" {
		state = f.state
	}
	return m.exchangeFlow(f, code, state)
}

func (m *Manager) exchangeFlow(f *flow, code, state string) (*AccountInfo, error) {
	f.mu.Lock()
	switch {
	case f.st == StateDone:
		r := f.result
		f.mu.Unlock()
		return r, nil
	case f.st == StateError:
		e := f.err
		f.mu.Unlock()
		return nil, errors.New(e)
	case f.busy:
		f.mu.Unlock()
		return nil, errors.New("login is already being completed")
	}
	f.busy = true
	f.mu.Unlock()

	ctx, cancel := context.WithTimeout(f.ctx, 3*time.Minute)
	defer cancel()
	var c *Credential
	var err error
	switch f.typ {
	case TypeCodex:
		c, err = m.exchangeCodex(ctx, code, f.verifier)
	case TypeClaude:
		c, err = m.exchangeClaude(ctx, code, state, f.verifier)
	case TypeGeminiCLI:
		c, err = m.exchangeGoogle(ctx, code, f.verifier, f.redirectURI)
		if err == nil {
			c.Email = m.googleEmail(ctx, c.AccessToken)
			c.ProjectID, c.Plan, err = m.setupCodeAssist(ctx, c.AccessToken, f.wantProject)
		}
	default:
		err = fmt.Errorf("flow type %s does not take a code", f.typ)
	}
	if err != nil {
		f.mu.Lock()
		f.busy = false
		f.mu.Unlock()
		// Token endpoint rejections are final (codes are single-use); network errors may be retried.
		var he *HTTPError
		if errors.As(err, &he) {
			m.fail(f, err)
		}
		return nil, err
	}
	return m.finish(f, c)
}

func (m *Manager) finish(f *flow, c *Credential) (*AccountInfo, error) {
	c.LastRefresh = m.now()
	id := f.accountID
	if id == "" {
		id = deriveAccountID(c.Type, c.Email)
	}
	if err := m.put(id, c); err != nil {
		m.fail(f, fmt.Errorf("save credential: %w", err))
		return nil, err
	}
	info := &AccountInfo{AccountID: id, Type: c.Type, Email: c.Email, Plan: c.Plan, ProjectID: c.ProjectID}
	if !c.ExpiresAt.IsZero() {
		t := c.ExpiresAt
		info.ExpiresAt = &t
	}
	lr := c.LastRefresh
	info.LastRefresh = &lr
	f.mu.Lock()
	f.st, f.busy, f.result = StateDone, false, info
	f.mu.Unlock()
	f.cancel()
	m.Logger.Info("oauth: login complete", "type", c.Type, "account", id)
	return info, nil
}

func (m *Manager) fail(f *flow, err error) {
	f.mu.Lock()
	if f.st == StatePending {
		f.st, f.err, f.busy = StateError, err.Error(), false
	}
	f.mu.Unlock()
	f.cancel()
	m.Logger.Warn("oauth: login failed", "type", f.typ, "err", err)
}

func deriveAccountID(typ, email string) string {
	if email == "" {
		return typ + "-" + randHex(4)
	}
	var b strings.Builder
	for _, r := range strings.ToLower(email) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	id := typ + "-" + strings.Trim(b.String(), "-.")
	if len(id) > 120 {
		id = id[:120]
	}
	if !ValidAccountID(id) {
		return typ + "-" + randHex(4)
	}
	return id
}

func (m *Manager) flow(id string) *flow {
	m.mu.Lock()
	defer m.mu.Unlock()
	f := m.flows[id]
	if f != nil && m.now().Sub(f.created) > m.flowTTL {
		delete(m.flows, id)
		f.cancel()
		return nil
	}
	return f
}

// FlowStatus reports the state of a login flow.
func (m *Manager) FlowStatus(id string) (FlowStatus, bool) {
	f := m.flow(id)
	if f == nil {
		return FlowStatus{}, false
	}
	return f.status(), true
}

func (m *Manager) expireFlows() {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	for id, f := range m.flows {
		if now.Sub(f.created) > m.flowTTL {
			delete(m.flows, id)
			f.cancel()
		}
	}
}

func (m *Manager) closeFlows() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, f := range m.flows {
		delete(m.flows, id)
		f.cancel()
	}
}

// ---- GitHub device flow ----

type deviceCodeResp struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

func (m *Manager) startDevice(f *flow) error {
	var d deviceCodeResp
	err := m.doJSON(f.ctx, http.MethodPost, GitHubDeviceCodeURL, url.Values{
		"client_id": {GitHubClientID},
		"scope":     {GitHubScope},
	}, &d)
	if err != nil {
		return fmt.Errorf("github device code: %w", err)
	}
	if d.DeviceCode == "" || d.UserCode == "" {
		return errors.New("github device code: incomplete response")
	}
	f.mode, f.userCode, f.verifURI = ModeDevice, d.UserCode, d.VerificationURI
	go m.pollDevice(f, d)
	return nil
}

func (m *Manager) pollDevice(f *flow, d deviceCodeResp) {
	interval := max(d.Interval, 5)
	ctx := f.ctx
	if d.ExpiresIn > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(d.ExpiresIn)*m.pollUnit)
		defer cancel()
	}
	for {
		select {
		case <-ctx.Done():
			m.fail(f, errors.New("device code expired before authorization"))
			return
		case <-time.After(time.Duration(interval) * m.pollUnit):
		}
		var r struct {
			AccessToken string `json:"access_token"`
			Error       string `json:"error"`
			ErrorDesc   string `json:"error_description"`
			Interval    int    `json:"interval"`
		}
		err := m.doJSON(ctx, http.MethodPost, GitHubAccessTokenURL, url.Values{
			"client_id":   {GitHubClientID},
			"device_code": {d.DeviceCode},
			"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		}, &r)
		if err != nil {
			if ctx.Err() != nil {
				continue
			}
			var he *HTTPError
			if errors.As(err, &he) && he.Status >= 400 && he.Status < 500 && he.Code != "" {
				r.Error, r.ErrorDesc = he.Code, he.Msg
			} else {
				m.Logger.Warn("oauth: device poll error", "err", err)
				continue
			}
		}
		switch r.Error {
		case "":
			if r.AccessToken == "" {
				m.fail(f, errors.New("github: empty access token"))
				return
			}
			c, err := m.refreshCopilot(ctx, &Credential{Type: TypeCopilot, RefreshToken: r.AccessToken})
			if err != nil {
				m.fail(f, err)
				return
			}
			var u struct {
				Login string `json:"login"`
				Email string `json:"email"`
			}
			if m.doJSON(ctx, http.MethodGet, GitHubUserURL, nil, &u,
				withHeader("Authorization", "token "+r.AccessToken)) == nil {
				c.Email = u.Email
				if c.Email == "" {
					c.Email = u.Login
				}
				if c.Extra == nil {
					c.Extra = map[string]string{}
				}
				c.Extra["github_login"] = u.Login
			}
			m.finish(f, c)
			return
		case "authorization_pending":
		case "slow_down":
			if r.Interval > interval {
				interval = r.Interval
			} else {
				interval += 5
			}
		default:
			msg := r.Error
			if r.ErrorDesc != "" {
				msg += ": " + r.ErrorDesc
			}
			m.fail(f, errors.New(msg))
			return
		}
	}
}
