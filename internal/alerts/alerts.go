// Package alerts delivers notifications (budget thresholds, expiring keys,
// low upstream quota) to a webhook and/or Telegram. Delivery happens on a
// background goroutine; the request path only enqueues and never blocks.
package alerts

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Project-by-Myself/clirouter/internal/config"
	"github.com/Project-by-Myself/clirouter/internal/redact"
)

// Event is one notification. Kind is "budget", "provider_budget",
// "key_expiring", "quota" or "test".
type Event struct {
	Kind      string         `json:"event"`
	Subject   string         `json:"subject"`
	Threshold int            `json:"threshold,omitempty"`
	Text      string         `json:"text"`
	Data      map[string]any `json:"data,omitempty"`
	Time      time.Time      `json:"time"`
}

// Notifier is safe for concurrent use.
type Notifier struct {
	cfg    atomic.Pointer[config.Alerts]
	ch     chan Event
	client *http.Client
	log    *slog.Logger
	mu     sync.Mutex
	sent   map[string]time.Time // dedupe keys
	// TelegramAPI is the Bot API base (overridable in tests).
	TelegramAPI string
}

func New(log *slog.Logger) *Notifier {
	n := &Notifier{ch: make(chan Event, 256), client: &http.Client{Timeout: 10 * time.Second}, log: log, sent: map[string]time.Time{}, TelegramAPI: "https://api.telegram.org"}
	n.cfg.Store(&config.Alerts{})
	return n
}

// SetConfig applies new alert settings.
func (n *Notifier) SetConfig(a config.Alerts) { n.cfg.Store(&a) }

// Config returns the current settings.
func (n *Notifier) Config() config.Alerts { return *n.cfg.Load() }

// Run delivers queued events until ctx ends.
func (n *Notifier) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-n.ch:
			if errs := n.deliver(ctx, ev); len(errs) > 0 && n.log != nil {
				n.log.Warn("alert delivery failed", "event", ev.Kind, "subject", ev.Subject, "err", strings.Join(errs, "; "))
			}
		}
	}
}

// Once enqueues ev unless an event with the same key was already sent.
// Keys embed the period/window so a new period alerts again.
func (n *Notifier) Once(key string, ev Event) {
	if n == nil || !n.cfg.Load().Enabled() {
		return
	}
	n.mu.Lock()
	if _, dup := n.sent[key]; dup {
		n.mu.Unlock()
		return
	}
	n.sent[key] = time.Now()
	if len(n.sent) > 10000 { // forget the oldest half; keys are per period anyway
		cut := time.Now().Add(-24 * time.Hour)
		for k, t := range n.sent {
			if t.Before(cut) {
				delete(n.sent, k)
			}
		}
	}
	n.mu.Unlock()
	ev.Time = time.Now()
	select {
	case n.ch <- ev:
	default: // never block the caller; a full queue means delivery is stuck
	}
}

// Mark records key as already sent without sending (used so a restart does
// not re-announce thresholds crossed earlier in the period).
func (n *Notifier) Mark(key string) {
	n.mu.Lock()
	n.sent[key] = time.Now()
	n.mu.Unlock()
}

// Test delivers a test event synchronously and reports per-channel errors.
func (n *Notifier) Test(ctx context.Context) []string {
	if !n.cfg.Load().Enabled() {
		return []string{"no alert channel is configured"}
	}
	return n.deliver(ctx, Event{Kind: "test", Subject: "clirouter", Text: "✅ clirouter test alert: notifications are working.", Time: time.Now()})
}

func (n *Notifier) deliver(ctx context.Context, ev Event) []string {
	a := n.cfg.Load()
	var errs []string
	if u := config.Secret(a.WebhookURL); u != "" {
		body, _ := json.Marshal(ev)
		if err := n.post(ctx, u, "application/json", body); err != nil {
			errs = append(errs, "webhook: "+err.Error())
		}
	}
	if tok := config.Secret(a.TelegramBotToken); tok != "" && a.TelegramChatID != "" {
		form := url.Values{"chat_id": {a.TelegramChatID}, "text": {ev.Text}, "disable_web_page_preview": {"true"}}
		if err := n.post(ctx, n.TelegramAPI+"/bot"+tok+"/sendMessage", "application/x-www-form-urlencoded", []byte(form.Encode())); err != nil {
			errs = append(errs, "telegram: "+strings.ReplaceAll(err.Error(), tok, "<token>"))
		}
	}
	return errs
}

func (n *Notifier) post(ctx context.Context, u, ctype string, body []byte) error {
	var last error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt) * 2 * time.Second):
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
		if err != nil {
			return redactErr(err)
		}
		req.Header.Set("Content-Type", ctype)
		req.Header.Set("User-Agent", "clirouter-alerts")
		resp, err := n.client.Do(req)
		if err != nil {
			last = redactErr(err)
			continue
		}
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		resp.Body.Close()
		if resp.StatusCode < 300 {
			return nil
		}
		last = fmt.Errorf("HTTP %d: %s", resp.StatusCode, redact.String(strings.TrimSpace(string(msg))))
		if resp.StatusCode < 500 && resp.StatusCode != 429 {
			break
		}
	}
	return last
}

// redactErr hides URLs (webhook paths and bot tokens are secrets) in errors.
func redactErr(err error) error {
	var ue *url.Error
	if e, ok := err.(*url.Error); ok {
		ue = e
		return fmt.Errorf("%s: %v", ue.Op, ue.Err)
	}
	return fmt.Errorf("%s", redact.String(err.Error()))
}

// Pct returns usage as a percentage of a USD and/or token budget (the
// larger of the two); 0 when no budget is set.
func Pct(costUSD float64, tokens int64, budgetUSD float64, budgetTokens int64) float64 {
	p := 0.0
	if budgetUSD > 0 {
		p = 100 * costUSD / budgetUSD
	}
	if budgetTokens > 0 {
		p = max(p, 100*float64(tokens)/float64(budgetTokens))
	}
	return p
}
