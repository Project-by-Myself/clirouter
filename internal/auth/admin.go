package auth

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

// AdminPrefix is where the server mounts AdminHandler. The handler strips it
// itself, so it works whether or not the caller uses http.StripPrefix.
const AdminPrefix = "/admin/api/oauth/"

// AdminHandler serves the login/account JSON API. It performs no
// authentication: mount it behind the server's admin auth.
//
//	POST   start          StartRequest -> StartResponse
//	POST   complete       {"flow_id","code"} -> AccountInfo
//	GET    status?flow_id -> FlowStatus
//	GET    accounts       -> []AccountInfo
//	DELETE accounts/{id}
func (m *Manager) AdminHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if i := strings.Index(p, AdminPrefix); i >= 0 {
			p = p[i+len(AdminPrefix):]
		}
		p = strings.Trim(p, "/")
		w.Header().Set("Cache-Control", "no-store")

		switch {
		case p == "start":
			if !allow(w, r, http.MethodPost) {
				return
			}
			var req StartRequest
			if !decode(w, r, &req) {
				return
			}
			resp, err := m.StartFlow(req)
			if err != nil {
				writeErr(w, http.StatusBadRequest, err)
				return
			}
			writeJSON(w, http.StatusOK, resp)
		case p == "complete":
			if !allow(w, r, http.MethodPost) {
				return
			}
			var req struct {
				FlowID string `json:"flow_id"`
				Code   string `json:"code"`
			}
			if !decode(w, r, &req) {
				return
			}
			info, err := m.CompleteFlow(r.Context(), req.FlowID, req.Code)
			if err != nil {
				code := http.StatusBadRequest
				var he *HTTPError
				if errors.As(err, &he) {
					code = http.StatusBadGateway
				}
				writeErr(w, code, err)
				return
			}
			writeJSON(w, http.StatusOK, info)
		case p == "status":
			if !allow(w, r, http.MethodGet) {
				return
			}
			st, ok := m.FlowStatus(r.URL.Query().Get("flow_id"))
			if !ok {
				writeErr(w, http.StatusNotFound, errors.New("unknown or expired flow"))
				return
			}
			writeJSON(w, http.StatusOK, st)
		case p == "accounts":
			if !allow(w, r, http.MethodGet) {
				return
			}
			list, err := m.List()
			if err != nil {
				writeErr(w, http.StatusInternalServerError, err)
				return
			}
			writeJSON(w, http.StatusOK, list)
		case strings.HasPrefix(p, "accounts/"):
			if !allow(w, r, http.MethodDelete) {
				return
			}
			id := strings.TrimPrefix(p, "accounts/")
			if !ValidAccountID(id) {
				writeErr(w, http.StatusBadRequest, errors.New("invalid account id"))
				return
			}
			if err := m.Delete(id); err != nil {
				code := http.StatusInternalServerError
				if errors.Is(err, ErrNotFound) {
					code = http.StatusNotFound
				}
				writeErr(w, code, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		default:
			writeErr(w, http.StatusNotFound, errors.New("not found"))
		}
	})
}

func allow(w http.ResponseWriter, r *http.Request, method string) bool {
	if r.Method == method {
		return true
	}
	w.Header().Set("Allow", method)
	writeErr(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
	return false
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("invalid JSON body"))
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}
