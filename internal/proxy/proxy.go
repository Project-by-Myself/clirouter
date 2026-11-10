// Package proxy is the data plane: it runs one client request through
// routing, upstream attempts with pre-first-byte failover, and streaming.
package proxy

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"github.com/Project-by-Myself/clirouter/internal/alerts"
	"github.com/Project-by-Myself/clirouter/internal/config"
	"github.com/Project-by-Myself/clirouter/internal/ir"
	"github.com/Project-by-Myself/clirouter/internal/pricing"
	"github.com/Project-by-Myself/clirouter/internal/redact"
	"github.com/Project-by-Myself/clirouter/internal/router"
	"github.com/Project-by-Myself/clirouter/internal/sse"
	"github.com/Project-by-Myself/clirouter/internal/store"
	"github.com/Project-by-Myself/clirouter/internal/translate"
	"github.com/Project-by-Myself/clirouter/internal/upstream"
	"github.com/Project-by-Myself/clirouter/internal/usage"
)

type Runtime struct {
	Cfg     *config.Config
	Router  *router.Router
	Clients *upstream.Clients
	Pricing *pricing.Table               // nil: no cost estimates
	Keys    map[string]*config.ClientKey // client keys by name
}

// NewRuntime builds the per-config runtime (pricing table and key index).
func NewRuntime(cfg *config.Config, r *router.Router, clients *upstream.Clients) *Runtime {
	rt := &Runtime{Cfg: cfg, Router: r, Clients: clients, Pricing: pricing.New(cfg.Pricing), Keys: make(map[string]*config.ClientKey, len(cfg.APIKeys))}
	for i := range cfg.APIKeys {
		rt.Keys[cfg.APIKeys[i].Name] = &cfg.APIKeys[i]
	}
	return rt
}

type Handler struct {
	rt      atomic.Pointer[Runtime]
	Store   *store.Store
	Metrics *Metrics
	Usage   *usage.Tracker
	Alerts  *alerts.Notifier // optional
}

func NewHandler(rt *Runtime, st *store.Store) *Handler {
	h := &Handler{Store: st, Metrics: NewMetrics(), Usage: usage.New(nil)}
	h.rt.Store(rt)
	return h
}

// admitErr is a request refused by a client key's policy.
type admitErr struct {
	status     int
	typ, msg   string
	retryAfter time.Duration
}

// admit applies the client key's policy (disabled, expiry, allowed models,
// budget, rpm, concurrency). On success the returned release must be called
// when the request ends.
func (h *Handler) admit(rt *Runtime, keyName, model string) (func(), *admitErr) {
	k := rt.Keys[keyName]
	if k == nil {
		return func() {}, nil
	}
	if k.Disabled {
		return nil, &admitErr{status: http.StatusUnauthorized, typ: "authentication_error", msg: fmt.Sprintf("API key %q is disabled", k.Name)}
	}
	if exp := k.Expiry(); !exp.IsZero() && time.Now().After(exp) {
		return nil, &admitErr{status: http.StatusUnauthorized, typ: "authentication_error", msg: fmt.Sprintf("API key %q expired at %s", k.Name, exp.Format(time.RFC3339))}
	}
	if !k.AllowsModel(model) {
		return nil, &admitErr{status: http.StatusForbidden, typ: "permission_error", msg: fmt.Sprintf("model %q is not allowed for API key %q (allowed: %s)", model, k.Name, strings.Join(k.Models, ", "))}
	}
	if h.Usage.OverBudget(usage.Key, k.Name, k.BudgetUSD, k.BudgetTokens) {
		return nil, &admitErr{status: http.StatusPaymentRequired, typ: "billing_error", msg: fmt.Sprintf("API key %q reached its %s budget", k.Name, config.NormPeriod(k.BudgetPeriod))}
	}
	release, wait, lim := h.Usage.Acquire(usage.Key, k.Name, k.RPM, k.TPM, k.MaxParallel)
	if lim != usage.LimitNone {
		var msg string
		switch lim {
		case usage.LimitRPM:
			msg = fmt.Sprintf("API key %q is limited to %d requests per minute", k.Name, k.RPM)
		case usage.LimitTPM:
			msg = fmt.Sprintf("API key %q is limited to %d tokens per minute", k.Name, k.TPM)
		default:
			msg = fmt.Sprintf("API key %q is limited to %d concurrent requests", k.Name, k.MaxParallel)
		}
		return nil, &admitErr{status: http.StatusTooManyRequests, typ: "rate_limit_error", msg: msg, retryAfter: wait}
	}
	return release, nil
}

// BudgetAlerts announces the highest budget threshold crossed by a key or
// provider in the current period. With prime, thresholds already crossed are
// only marked as sent (after a restart or config change).
func (h *Handler) BudgetAlerts(rt *Runtime, keyName, provider string, prime bool) {
	levels := rt.Cfg.Alerts.Levels()
	check := func(kind usage.Kind, name, what string, budgetUSD float64, budgetTokens int64) {
		if budgetUSD <= 0 && budgetTokens <= 0 {
			return
		}
		snap := h.Usage.Snapshot(kind, name)
		pct := alerts.Pct(snap.Cost, snap.Input+snap.Output, budgetUSD, budgetTokens)
		top := -1
		for i, l := range levels {
			if pct >= float64(l) {
				top = i
			}
		}
		if top < 0 {
			return
		}
		keyOf := func(l int) string {
			return fmt.Sprintf("budget|%c|%s|%s|%d", kind, name, snap.PeriodStart.Format(time.RFC3339), l)
		}
		for i := 0; i < top; i++ {
			h.Alerts.Mark(keyOf(levels[i]))
		}
		if prime {
			h.Alerts.Mark(keyOf(levels[top]))
			return
		}
		used := fmt.Sprintf("$%.2f", snap.Cost)
		limit := fmt.Sprintf("$%.2f", budgetUSD)
		if budgetUSD <= 0 {
			used, limit = fmt.Sprintf("%d tokens", snap.Input+snap.Output), fmt.Sprintf("%d tokens", budgetTokens)
		}
		reset := "never (total budget)"
		if snap.PeriodEnd != nil {
			reset = snap.PeriodEnd.Format("2006-01-02 15:04")
		}
		icon, tail := "⚠️", "Resets "+reset+"."
		if pct >= 100 {
			icon, tail = "⛔", "Requests are refused until "+reset+"."
			if kind == usage.Provider {
				tail = "The router now skips this provider until " + reset + "."
			}
		}
		ev := alerts.Event{Kind: "budget", Subject: name, Threshold: levels[top],
			Text: fmt.Sprintf("%s clirouter: %s %q has used %.0f%% of its %s budget (%s / %s). %s", icon, what, name, pct, snap.Period, used, limit, tail),
			Data: map[string]any{"kind": what, "period": snap.Period, "pct": pct, "cost_usd": snap.Cost, "tokens": snap.Input + snap.Output, "budget_usd": budgetUSD, "budget_tokens": budgetTokens, "period_end": snap.PeriodEnd}}
		if kind == usage.Provider {
			ev.Kind = "provider_budget"
		}
		h.Alerts.Once(keyOf(levels[top]), ev)
	}
	if k := rt.Keys[keyName]; k != nil {
		check(usage.Key, k.Name, "key", k.BudgetUSD, k.BudgetTokens)
	}
	if provider != "" {
		for i := range rt.Cfg.Providers {
			if p := &rt.Cfg.Providers[i]; p.ID == provider {
				check(usage.Provider, p.ID, "provider", p.BudgetUSD, 0)
			}
		}
	}
}

// withinBudget drops targets whose provider has reached its spend cap.
func (h *Handler) withinBudget(targets []router.Target) []router.Target {
	out := targets[:0:0]
	for _, t := range targets {
		if !h.Usage.OverBudget(usage.Provider, t.Provider.Cfg.ID, t.Provider.Cfg.BudgetUSD, 0) {
			out = append(out, t)
		}
	}
	return out
}

func (h *Handler) Runtime() *Runtime     { return h.rt.Load() }
func (h *Handler) SetRuntime(r *Runtime) { h.rt.Store(r) }

// maxCooldownWait is how long a request may wait for an account whose
// cooldown is about to expire instead of failing immediately.
const maxCooldownWait = 5 * time.Second

// nonStreamFirstByte is the minimum wait for response headers of a
// non-streaming upstream request (the official SDKs default to 10 minutes).
var nonStreamFirstByte = 10 * time.Minute

type call struct {
	h          *Handler
	rt         *Runtime
	r          *http.Request
	cf         ir.Format
	codec      *translate.Codec
	body       []byte
	model      string
	stream     bool
	sessionKey string
	ireq       *ir.Request
	out        *clientWriter
	rec        store.Record
	start      time.Time
	usage      ir.Usage
	errMsg     string
	clientGone bool
	admitted   bool
}

// Serve handles one inference request in client format cf. For Gemini the
// model and stream flag come from the URL.
func (h *Handler) Serve(w http.ResponseWriter, r *http.Request, cf ir.Format, urlModel string, urlStream bool, clientKey string) {
	rt := h.rt.Load()
	c := &call{h: h, rt: rt, r: r, cf: cf, codec: translate.For(cf), start: time.Now()}
	c.rec = store.Record{TS: c.start, ClientKey: clientKey, ClientFormat: string(cf)}
	h.Metrics.inflight.Add(1)
	defer h.Metrics.inflight.Add(-1)

	body, err := readBody(w, r, int64(rt.Cfg.Routing.MaxBodyMB)<<20)
	if err != nil {
		c.out = newClientWriter(w, c.codec, false)
		status := http.StatusBadRequest
		if errors.Is(err, errTooLarge) {
			status = http.StatusRequestEntityTooLarge
		}
		c.out.fail(status, "invalid_request_error", err.Error(), 0, nil)
		c.finish()
		c.out.stop()
		return
	}
	c.body = body
	if cf == ir.FormatGemini {
		c.model, c.stream = urlModel, urlStream
	} else {
		res := gjson.GetManyBytes(body, "model", "stream")
		c.model, c.stream = res[0].String(), res[1].Bool()
	}
	c.rec.Model, c.rec.Stream = c.model, c.stream
	c.out = newClientWriter(w, c.codec, c.stream)
	defer c.out.stop()
	defer c.finish()

	if c.model == "" {
		c.out.fail(http.StatusBadRequest, "invalid_request_error", "model is required", 0, nil)
		return
	}
	release, aerr := h.admit(rt, clientKey, c.model)
	if aerr != nil {
		c.errMsg = aerr.msg
		c.out.fail(aerr.status, aerr.typ, aerr.msg, aerr.retryAfter, nil)
		return
	}
	defer release()
	c.admitted = true
	targets, err := rt.Router.Resolve(c.model)
	if err != nil {
		c.out.fail(http.StatusNotFound, "not_found_error", err.Error(), 0, nil)
		return
	}
	if within := h.withinBudget(targets); len(within) < len(targets) {
		if len(within) == 0 {
			msg := fmt.Sprintf("every provider for %q has reached its budget", c.model)
			c.errMsg = msg
			c.out.fail(http.StatusPaymentRequired, "billing_error", msg, 0, nil)
			return
		}
		targets = within
	}
	if rt.Cfg.Routing.SessionAffinity {
		c.sessionKey = sessionKey(r, body, cf)
	}
	ctx, cancel := context.WithTimeout(r.Context(), rt.Cfg.Timeouts.Total.D())
	defer cancel()
	if c.stream {
		c.out.keepalive(rt.Cfg.Timeouts.EarlyPing.D(), rt.Cfg.Timeouts.Keepalive.D())
	}
	c.run(ctx, targets)
}

// exclusion marks an account that already failed during this request. Most
// failures are specific to the upstream model (rate limit, model missing,
// overloaded), so a combo falling back to another model may still use the
// same account; model == "" rules the account out for every model.
type exclusion struct {
	a     *router.Account
	model string
}

func excludedFor(ex map[exclusion]bool, model string) map[*router.Account]bool {
	out := make(map[*router.Account]bool, len(ex))
	for k := range ex {
		if k.model == "" || k.model == model {
			out[k.a] = true
		}
	}
	return out
}

func (c *call) run(ctx context.Context, targets []router.Target) {
	maxAttempts := max(c.rt.Cfg.Routing.MaxAttempts, 1)
	excluded := map[exclusion]bool{}
	var last *failure
	attempts := 0
	waited := false
	for {
		var minWait time.Duration = -1
	targetsLoop:
		for _, t := range targets {
			for attempts < maxAttempts {
				a, wait := c.rt.Router.Pick(t, c.sessionKey, excludedFor(excluded, t.Model))
				if a == nil {
					if wait > 0 && (minWait < 0 || wait < minWait) {
						minWait = wait
					}
					continue targetsLoop
				}
				attempts++
				c.rec.Attempts = attempts
				f, done := c.attempt(ctx, t, a)
				if done {
					return
				}
				if c.clientGone {
					return
				}
				last = f
				ex := exclusion{a: a, model: t.Model}
				if f.Kind == router.FailAuth || f.Kind == router.FailQuota {
					ex.model = ""
				}
				excluded[ex] = true
				switch f.Action {
				case upstream.Fatal:
					c.errMsg = f.Message
					c.out.fail(f.Status, f.Type, f.Message, 0, f.raw)
					return
				case upstream.NextTarget:
					continue targetsLoop
				}
			}
			break
		}
		// Every account is cooling down. If one frees up shortly, wait for it
		// instead of bouncing the agent with a 429.
		if !waited && last == nil && minWait > 0 && minWait <= maxCooldownWait && attempts < maxAttempts {
			waited = true
			select {
			case <-time.After(minWait + 50*time.Millisecond):
				continue
			case <-ctx.Done():
				c.clientGone = c.r.Context().Err() != nil
				return
			}
		}
		if last == nil {
			msg := fmt.Sprintf("all accounts for %q are cooling down", c.model)
			c.errMsg = msg
			c.out.fail(http.StatusTooManyRequests, "rate_limit_error", msg, max(minWait, time.Second), nil)
			return
		}
		c.errMsg = last.Message
		status := last.Status
		if status == 0 {
			status = http.StatusBadGateway
		}
		c.out.fail(status, last.Type, last.Message, last.RetryAfter, nil)
		return
	}
}

// failure extends upstream.Failure with the raw upstream body for fatal
// errors in passthrough mode, so the client sees the provider's own error.
type failure struct {
	upstream.Failure
	raw []byte
}

// attempt runs one upstream call. It returns done=true when the client got
// a response (success or a mid-stream error that cannot be retried).
func (c *call) attempt(ctx context.Context, t router.Target, a *router.Account) (*failure, bool) {
	ptype := t.Provider.Cfg.Type
	upFmt, pass := upstream.Choose(ptype, c.cf)
	var body []byte
	var err error
	if pass {
		body = c.body
		if c.cf != ir.FormatGemini && t.Model != c.model {
			if body, err = sjson.SetBytes(c.body, "model", t.Model); err != nil {
				return &failure{Failure: upstream.Failure{Status: 400, Action: upstream.Fatal, Type: "invalid_request_error", Message: "invalid JSON body"}}, false
			}
		}
	} else {
		if c.ireq == nil {
			req, err := c.codec.DecodeRequest(c.body)
			if err != nil {
				return &failure{Failure: upstream.Failure{Status: 400, Action: upstream.Fatal, Type: "invalid_request_error", Message: "invalid request: " + err.Error()}}, false
			}
			if c.cf == ir.FormatGemini {
				req.Model = c.model
			}
			req.Stream = c.stream
			c.ireq = req
		}
		body, err = translate.For(upFmt).EncodeRequest(c.ireq, ir.EncodeOptions{Model: t.Model, ProviderType: ptype})
		if err != nil {
			return &failure{Failure: upstream.Failure{Status: 400, Action: upstream.Fatal, Type: "invalid_request_error", Message: "cannot translate request: " + err.Error()}}, false
		}
	}
	upStream := c.stream || !pass

	client, err := c.rt.Clients.Get(t.Provider.Cfg.Proxy)
	if err != nil {
		return &failure{Failure: upstream.Failure{Status: 502, Action: upstream.NextTarget, Type: "api_error", Message: err.Error()}}, false
	}
	firstByte := c.rt.Cfg.Timeouts.FirstByte.D()
	if d := t.Provider.Cfg.FirstByte.D(); d > 0 {
		firstByte = d
	}
	if !upStream {
		// A non-streaming upstream sends nothing until the whole completion
		// is ready, so the first-byte timeout would cap generation time and
		// re-run a slow but healthy generation on another account.
		firstByte = max(firstByte, nonStreamFirstByte)
	}
	idle := c.rt.Cfg.Timeouts.Idle.D()
	if d := t.Provider.Cfg.Idle.D(); d > 0 {
		idle = d
	}

	refreshed := false
	for {
		actx, acancel := context.WithCancelCause(ctx)
		req, err := upstream.Build(actx, a, upFmt, t.Model, body, upStream, c.r.Header)
		if err != nil {
			acancel(nil)
			a.MarkFailure(t.Model, router.FailAuth, 0, err.Error())
			return &failure{Failure: upstream.Failure{Status: 401, Action: upstream.NextAccount, Type: "authentication_error", Message: err.Error()}}, false
		}
		wd := newWatchdog(firstByte, errFirstByte, acancel)
		a.Begin()
		resp, err := client.Do(req)
		if err != nil {
			wd.stop()
			a.End()
			cause := context.Cause(actx)
			acancel(nil)
			if c.r.Context().Err() != nil {
				c.clientGone = true
				return &failure{}, false
			}
			msg := err.Error()
			status := http.StatusBadGateway
			if errors.Is(cause, errFirstByte) {
				msg, status = fmt.Sprintf("%s did not respond within %s", t.Provider.Cfg.ID, firstByte), http.StatusGatewayTimeout
			} else if ctx.Err() != nil {
				return &failure{Failure: upstream.Failure{Status: 504, Action: upstream.Fatal, Type: "api_error", Message: "request exceeded total timeout"}}, false
			}
			a.MarkFailure(t.Model, router.FailNetwork, 0, msg)
			return &failure{Failure: upstream.Failure{Status: status, Kind: router.FailNetwork, Action: upstream.NextTarget, Type: "api_error", Message: msg}}, false
		}
		a.ObserveQuota(resp.Header)
		if resp.StatusCode >= 400 {
			wd.stop()
			raw, _ := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
			resp.Body.Close()
			a.End()
			acancel(nil)
			f := upstream.Classify(resp.StatusCode, resp.Header, raw, a.OAuth != nil)
			if f.Action == upstream.RefreshAuth {
				if !refreshed && a.OAuth.Refresh() == nil {
					refreshed = true
					continue
				}
				f.Action = upstream.NextAccount
			}
			out := &failure{Failure: f}
			if f.Action == upstream.Fatal {
				if pass {
					out.raw = redact.Bytes(raw)
				}
			} else {
				a.MarkFailure(t.Model, f.Kind, f.RetryAfter, f.Message)
			}
			return out, false
		}

		c.rec.Provider, c.rec.Account, c.rec.UpstreamModel, c.rec.Passthrough = t.Provider.Cfg.ID, a.ID, t.Model, pass
		wr := &watchedReader{r: resp.Body, wd: wd, idle: idle}
		var f *failure
		var done bool
		switch {
		case pass && !upStream:
			f, done = c.copyJSON(resp, wr)
		case pass:
			f, done = c.pipePassthrough(wr)
		default:
			f, done = c.pipeTranslated(wr, upFmt)
		}
		wd.stop()
		cause := context.Cause(actx)
		resp.Body.Close()
		a.End()
		acancel(nil)
		if c.r.Context().Err() != nil {
			c.clientGone = true
			return &failure{}, true
		}
		if done {
			if c.errMsg == "" {
				a.MarkSuccess(t.Model)
				c.rt.Router.Bind(c.sessionKey, a)
			} else {
				a.MarkFailure(t.Model, router.FailServer, 0, c.errMsg)
			}
			return nil, true
		}
		if errors.Is(cause, errFirstByte) || errors.Is(cause, errIdle) {
			f.Message = fmt.Sprintf("%s: %v", t.Provider.Cfg.ID, cause)
			f.Status = http.StatusGatewayTimeout
		}
		a.MarkFailure(t.Model, f.Kind, f.RetryAfter, f.Message)
		return f, false
	}
}

// copyJSON forwards a non-streaming passthrough response.
func (c *call) copyJSON(resp *http.Response, r io.Reader) (*failure, bool) {
	b, err := io.ReadAll(io.LimitReader(r, 256<<20))
	if err != nil {
		return &failure{Failure: upstream.Failure{Status: 502, Kind: router.FailNetwork, Action: upstream.NextAccount, Type: "api_error", Message: "reading upstream response: " + err.Error()}}, false
	}
	if u, ok := c.codec.UsageFromEvent(nil, b); ok {
		c.usage.Merge(u)
	}
	c.out.writeJSON(resp.StatusCode, resp.Header.Get("Content-Type"), b)
	return nil, true
}

// pipePassthrough copies upstream events verbatim. It holds back preamble
// events until real content arrives, so an in-band error at the start can
// still fail over to another account.
func (c *call) pipePassthrough(r io.Reader) (*failure, bool) {
	rd := sse.NewReader(r, 0)
	codec := c.codec
	marker := []byte(codec.UsageMarker)
	var held []byte
	committed, finished := false, false
	for {
		ev, err := rd.Next()
		if err != nil {
			if !committed {
				return streamBroke(err), false
			}
			if err == io.EOF && finished {
				// Some OpenAI-compatible servers close the stream after the
				// finish_reason chunk without sending "data: [DONE]".
				_ = c.out.writeEvents(doneEvent, true)
				return nil, true
			}
			c.errMsg = "upstream stream ended unexpectedly: " + errText(err)
			_ = c.out.writeEvents(codec.StreamErrorEvent("api_error", c.errMsg), true)
			return nil, true
		}
		if bytes.Contains(ev.Data, marker) {
			if u, ok := codec.UsageFromEvent(ev.Name, ev.Data); ok {
				c.usage.Merge(u)
			}
		}
		if c.cf == ir.FormatChat && !finished && mayFinish(ev.Data) {
			finished = chatFinished(ev.Data)
		}
		if !committed {
			if typ, msg, ok := codec.IsErrorEvent(ev.Name, ev.Data); ok {
				return inbandFailure(typ, msg), false
			}
			if codec.Preamble(ev.Name, ev.Data) {
				held = append(held, ev.Raw...)
				continue
			}
			committed = true
			if len(held) > 0 {
				held = append(held, ev.Raw...)
				if c.out.writeEvents(held, rd.Buffered() == 0) != nil {
					return nil, true
				}
				continue
			}
		} else if typ, msg, ok := codec.IsErrorEvent(ev.Name, ev.Data); ok {
			c.errMsg = typ + ": " + msg
		}
		terminal := codec.IsTerminal(ev.Name, ev.Data)
		if c.out.writeEvents(ev.Raw, terminal || rd.Buffered() == 0) != nil {
			return nil, true
		}
		if terminal {
			return nil, true
		}
	}
}

// pipeTranslated decodes upstream events into IR and re-encodes them for the
// client (or aggregates them for a non-streaming client).
func (c *call) pipeTranslated(r io.Reader, upFmt ir.Format) (*failure, bool) {
	rd := sse.NewReader(r, 0)
	dec := ir.UnwrapCustomTools(translate.For(upFmt).NewStreamDecoder(), c.ireq)
	var enc ir.StreamEncoder
	var agg *ir.Aggregator
	if c.stream {
		enc = c.codec.NewStreamEncoder(c.ireq, c.model)
	} else {
		agg = ir.NewAggregator()
	}
	var buf []byte
	committed, done, sawFinish := false, false, false
	var preErr *ir.Event
	emit := func(ev ir.Event) {
		if done {
			return
		}
		switch ev.Kind {
		case ir.EvUsage:
			c.usage.Merge(ev.Usage)
		case ir.EvError:
			if !committed {
				e := ev
				preErr = &e
				return
			}
			c.errMsg = ev.Text
		case ir.EvFinish:
			sawFinish = true
		case ir.EvDone:
			done = true
		}
		if ev.Kind != ir.EvStart && ev.Kind != ir.EvUsage {
			committed = true
		}
		if enc != nil {
			buf = enc.Encode(buf, ev)
		} else {
			agg.Add(ev)
		}
	}
	for !done {
		ev, err := rd.Next()
		if err != nil {
			if err == io.EOF && sawFinish {
				emit(ir.Event{Kind: ir.EvDone})
				break
			}
			if !committed {
				return streamBroke(err), false
			}
			c.errMsg = "upstream stream ended unexpectedly: " + errText(err)
			emit(ir.Event{Kind: ir.EvError, Text: c.errMsg, ErrType: "api_error"})
			emit(ir.Event{Kind: ir.EvDone})
			break
		}
		if derr := dec.Decode(ev.Name, ev.Data, emit); derr != nil && !committed {
			return &failure{Failure: upstream.Failure{Status: 502, Kind: router.FailServer, Action: upstream.NextAccount, Type: "api_error", Message: "bad upstream event: " + derr.Error()}}, false
		}
		if preErr != nil {
			return inbandFailure(preErr.ErrType, preErr.Text), false
		}
		if enc != nil && committed && len(buf) > 0 && (done || rd.Buffered() == 0) {
			if c.out.writeEvents(buf, true) != nil {
				return nil, true
			}
			buf = buf[:0]
		}
	}
	if enc != nil {
		if len(buf) > 0 {
			_ = c.out.writeEvents(buf, true)
		}
		return nil, true
	}
	if agg.ErrMsg != "" && c.errMsg != "" {
		c.out.fail(http.StatusBadGateway, agg.ErrType, agg.ErrMsg, 0, nil)
		return nil, true
	}
	b, err := c.codec.EncodeResponse(c.ireq, agg.Response())
	if err != nil {
		c.errMsg = err.Error()
		c.out.fail(http.StatusInternalServerError, "api_error", err.Error(), 0, nil)
		return nil, true
	}
	c.out.writeJSON(http.StatusOK, "application/json", b)
	return nil, true
}

var doneEvent = []byte("data: [DONE]\n\n")

// mayFinish cheaply skips the common `"finish_reason":null` chunks so
// passthrough does not parse every event.
func mayFinish(data []byte) bool {
	return bytes.Contains(data, []byte(`"finish_reason":"`)) || bytes.Contains(data, []byte(`"finish_reason": "`))
}

// chatFinished reports whether a chat.completion.chunk carries a finish_reason.
func chatFinished(data []byte) bool {
	for _, r := range gjson.GetBytes(data, "choices.#.finish_reason").Array() {
		if r.Type == gjson.String && r.Str != "" {
			return true
		}
	}
	return false
}

func streamBroke(err error) *failure {
	return &failure{Failure: upstream.Failure{Status: 502, Kind: router.FailNetwork, Action: upstream.NextAccount, Type: "api_error", Message: "upstream stream failed before any content: " + errText(err)}}
}

func inbandFailure(typ, msg string) *failure {
	f := &failure{Failure: upstream.Failure{Status: 502, Kind: router.FailServer, Action: upstream.NextAccount, Type: typ, Message: msg}}
	l := strings.ToLower(typ + " " + msg)
	switch {
	case strings.Contains(l, "overload"), strings.Contains(l, "capacity"), strings.Contains(l, "unavailable"):
		f.Status, f.Action = 529, upstream.NextTarget
	case strings.Contains(l, "rate"), strings.Contains(l, "quota"), strings.Contains(l, "usage limit"):
		f.Status, f.Kind = 429, router.FailRateLimit
	case strings.Contains(l, "invalid_request"), strings.Contains(l, "invalid request"):
		f.Status, f.Action = 400, upstream.Fatal
	}
	if f.Type == "" {
		f.Type = "api_error"
	}
	return f
}

func errText(err error) string {
	if err == io.EOF {
		return "connection closed"
	}
	return err.Error()
}

func (c *call) finish() {
	_, contentSent, status, first := c.out.state()
	c.rec.DurationMs = time.Since(c.start).Milliseconds()
	if !first.IsZero() {
		c.rec.TTFBms = first.Sub(c.start).Milliseconds()
	}
	c.rec.Status = status
	switch {
	case c.clientGone && !contentSent:
		c.rec.Status = 499
		c.rec.Error = "client closed request"
	case c.errMsg != "":
		if c.rec.Status < 400 {
			c.rec.Status = 502
		}
		c.rec.Error = redact.String(c.errMsg)
	}
	if c.rec.Status == 0 {
		c.rec.Status = 499
	}
	c.rec.InputTokens, c.rec.OutputTokens = c.usage.InputTokens, c.usage.OutputTokens
	c.rec.CacheRead, c.rec.CacheWrite, c.rec.Reasoning = c.usage.CacheReadTokens, c.usage.CacheWriteTokens, c.usage.ReasoningTokens
	if c.rt.Pricing != nil {
		c.rec.Cost = c.rt.Pricing.Cost(c.rec.Provider, c.rec.UpstreamModel, c.rec.Model, pricing.Usage{Input: c.rec.InputTokens, Output: c.rec.OutputTokens, CacheRead: c.rec.CacheRead, CacheWrite: c.rec.CacheWrite})
	}
	if c.admitted && c.h.Usage != nil {
		u := usage.Totals{Requests: 1, Input: c.rec.InputTokens, Output: c.rec.OutputTokens, Cost: c.rec.Cost}
		c.h.Usage.Add(usage.Key, c.rec.ClientKey, u)
		if c.rec.Provider != "" {
			c.h.Usage.Add(usage.Provider, c.rec.Provider, u)
		}
		if c.h.Alerts != nil && c.rt.Cfg.Alerts.Enabled() && u.Cost+float64(u.Input+u.Output) > 0 {
			c.h.BudgetAlerts(c.rt, c.rec.ClientKey, c.rec.Provider, false)
		}
	}
	c.h.Metrics.observe(&c.rec)
	if c.h.Store != nil {
		c.h.Store.Add(c.rec)
	}
}

var errTooLarge = errors.New("request body too large")

// bodyReadTimeout bounds how long a client may take to send its request
// body (slow-body / slowloris). Headers are bounded by ReadHeaderTimeout.
var bodyReadTimeout = 2 * time.Minute

func readBody(w http.ResponseWriter, r *http.Request, limit int64) (_ []byte, err error) {
	if limit <= 0 {
		limit = 64 << 20
	}
	// The deadline must be cleared before streaming: net/http's background
	// read would otherwise hit it and cancel the request mid-response. On
	// failure it is left in place so net/http's drain of the unread body
	// cannot hang either; that connection is not reused.
	rc := http.NewResponseController(w)
	if rc.SetReadDeadline(time.Now().Add(bodyReadTimeout)) == nil {
		defer func() {
			if err == nil {
				rc.SetReadDeadline(time.Time{})
			}
		}()
	}
	var src io.Reader = r.Body
	switch strings.ToLower(r.Header.Get("Content-Encoding")) {
	case "", "identity":
	case "gzip":
		gz, err := gzip.NewReader(io.LimitReader(r.Body, limit))
		if err != nil {
			return nil, fmt.Errorf("bad gzip body: %w", err)
		}
		defer gz.Close()
		src = gz
	case "zstd":
		zr, err := zstd.NewReader(io.LimitReader(r.Body, limit), zstd.WithDecoderMaxMemory(uint64(limit)), zstd.WithDecoderConcurrency(1))
		if err != nil {
			return nil, fmt.Errorf("bad zstd body: %w", err)
		}
		defer zr.Close()
		src = zr
	default:
		return nil, fmt.Errorf("unsupported Content-Encoding %q", r.Header.Get("Content-Encoding"))
	}
	b, err := io.ReadAll(io.LimitReader(src, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, errTooLarge
	}
	if len(b) == 0 {
		return nil, errors.New("empty request body")
	}
	return b, nil
}

// sessionKey identifies a conversation so its turns stick to one account
// (keeping the provider prompt cache warm). It only reads headers and one or
// two top-level JSON fields.
func sessionKey(r *http.Request, body []byte, f ir.Format) string {
	for _, h := range []string{"X-Claude-Code-Session-Id", "Session_id", "Session-Id", "Conversation_id", "X-Session-Id"} {
		if v := r.Header.Get(h); v != "" {
			return v
		}
	}
	var paths []string
	switch f {
	case ir.FormatAnthropic:
		paths = []string{"metadata.user_id"}
	case ir.FormatResponses, ir.FormatChat:
		paths = []string{"prompt_cache_key", "user"}
	}
	for _, p := range paths {
		if v := gjson.GetBytes(body, p).String(); v != "" {
			return v
		}
	}
	return ""
}
