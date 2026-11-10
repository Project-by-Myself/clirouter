package proxy

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"

	"github.com/Project-by-Myself/clirouter/internal/ir"
	"github.com/Project-by-Myself/clirouter/internal/translate"
	"github.com/Project-by-Myself/clirouter/internal/upstream"
)

func ErrorBody(f ir.Format, status int, typ, msg string) []byte {
	return translate.For(f).ErrorBody(status, typ, msg)
}

// CountTokens implements POST /v1/messages/count_tokens.
func (h *Handler) CountTokens(w http.ResponseWriter, r *http.Request) {
	rt := h.rt.Load()
	body, err := readBody(r, int64(rt.Cfg.Routing.MaxBodyMB)<<20)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write(ErrorBody(ir.FormatAnthropic, 400, "invalid_request_error", err.Error()))
		return
	}
	model := gjson.GetBytes(body, "model").String()
	if targets, err := rt.Router.Resolve(model); err == nil {
		for _, t := range targets {
			if typ := t.Provider.Cfg.Type; typ != "anthropic" && typ != "claude-oauth" {
				continue
			}
			a, _ := rt.Router.Pick(t, "", nil)
			if a == nil {
				continue
			}
			client, err := rt.Clients.Get(t.Provider.Cfg.Proxy)
			if err != nil {
				break
			}
			b := body
			if t.Model != model {
				b = []byte(strings.Replace(string(body), `"`+model+`"`, `"`+t.Model+`"`, 1))
			}
			req, err := upstream.Build(r.Context(), a, ir.FormatAnthropic, t.Model, b, false, r.Header)
			if err != nil {
				break
			}
			req.URL.Path += "/count_tokens"
			resp, err := client.Do(req)
			if err != nil {
				break
			}
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				w.Header().Set("Content-Type", "application/json")
				io.Copy(w, resp.Body)
				return
			}
			break
		}
	}
	// Rough estimate: ~4 bytes per token of JSON payload.
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]int{"input_tokens": max(len(body)/4, 1)})
}
