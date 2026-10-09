package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/auth"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/model"
)

func (s *Server) accountTokenKey(selfID string) string { return "account:" + selfID }

func (s *Server) ensureAccountToken(acc model.Account) (string, error) {
	if tok, ok := s.vault.Get(s.accountTokenKey(acc.SelfID)); ok {
		if acc.TokenHash == auth.HashToken(tok) {
			return tok, nil
		}
	}
	tok, hash, err := auth.NewToken()
	if err != nil {
		return "", err
	}
	if err := s.st.SetAccountTokenHash(acc.ID, hash); err != nil {
		return "", err
	}
	if err := s.vault.Set(s.accountTokenKey(acc.SelfID), tok); err != nil {
		return "", err
	}
	return tok, nil
}

func (s *Server) handleAccountsList(w http.ResponseWriter, r *http.Request) {
	accounts, err := s.st.ListAccounts()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	snap := s.hub.SnapshotAccounts()
	out := make([]map[string]any, 0, len(accounts))
	for _, a := range accounts {
		view := map[string]any{
			"id": a.ID, "self_id": a.SelfID, "name": a.Name, "nickname": a.Nickname,
			"enabled": a.Enabled, "status": a.Status, "created_at": a.CreatedAt, "last_seen_at": a.LastSeenAt,
			"online": 0,
		}
		if rt, ok := snap[a.SelfID]; ok {
			view["status"] = rt.Status
			view["online"] = len(rt.Conns)
			view["connections"] = rt.Conns
		}
		out = append(out, view)
	}
	writeJSON(w, http.StatusOK, map[string]any{"accounts": out, "pending": s.hub.PendingAccounts()})
}

func (s *Server) handleAccountGet(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	acc, err := s.st.GetAccount(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "account not found")
		return
	}
	view := map[string]any{"id": acc.ID, "self_id": acc.SelfID, "name": acc.Name, "nickname": acc.Nickname,
		"enabled": acc.Enabled, "status": acc.Status, "created_at": acc.CreatedAt, "last_seen_at": acc.LastSeenAt}
	if rt, ok := s.hub.SnapshotAccounts()[acc.SelfID]; ok {
		view["status"] = rt.Status
		view["connections"] = rt.Conns
	}
	bindings, _ := s.st.ListBindingsByAccount(acc.ID)
	view["bindings"] = bindings
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) handleAccountCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SelfID string `json:"self_id"`
		Name   string `json:"name"`
	}
	if err := readJSON(r, &req); err != nil || strings.TrimSpace(req.SelfID) == "" {
		writeError(w, http.StatusBadRequest, "self_id required")
		return
	}
	acc, err := s.st.CreateAccount(strings.TrimSpace(req.SelfID), req.Name)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	token, err := s.ensureAccountToken(acc)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = s.hub.Reload()
	s.audit(r, "account.create", acc.SelfID, acc.Name)
	writeJSON(w, http.StatusOK, map[string]any{"account": acc, "token": token})
}

func (s *Server) handleAccountApprove(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SelfID string `json:"self_id"`
	}
	if err := readJSON(r, &req); err != nil || req.SelfID == "" {
		writeError(w, http.StatusBadRequest, "self_id required")
		return
	}
	if err := s.hub.ApproveAccount(req.SelfID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	acc, err := s.st.GetAccountBySelfID(req.SelfID)
	if err == nil {
		_, _ = s.ensureAccountToken(acc)
	}
	s.audit(r, "account.approve", req.SelfID, "")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleAccountUpdate(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	acc, err := s.st.GetAccount(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "account not found")
		return
	}
	var req struct {
		Name    *string `json:"name"`
		Enabled *bool   `json:"enabled"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	name := acc.Name
	enabled := acc.Enabled
	if req.Name != nil {
		name = *req.Name
	}
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	if err := s.st.UpdateAccount(id, name, enabled); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = s.hub.Reload()
	s.audit(r, "account.update", acc.SelfID, "")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleAccountDelete(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	acc, err := s.st.GetAccount(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "account not found")
		return
	}
	if err := s.st.DeleteAccount(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = s.vault.Delete(s.accountTokenKey(acc.SelfID))
	_ = s.hub.Reload()
	s.audit(r, "account.delete", acc.SelfID, "")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleAccountRotate(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	acc, err := s.st.GetAccount(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "account not found")
		return
	}
	tok, hash, err := auth.NewToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.st.SetAccountTokenHash(id, hash); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.vault.Set(s.accountTokenKey(acc.SelfID), tok); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "account.rotate", acc.SelfID, "")
	writeJSON(w, http.StatusOK, map[string]any{"token": tok})
}

func (s *Server) handleAccountConnect(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	acc, err := s.st.GetAccount(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "account not found")
		return
	}
	tok, err := s.ensureAccountToken(acc)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	base := baseURL(r, s.cfg.Server.TrustProxy)
	up := s.cfg.OneBot.Upstream.Path
	httpPrefix := s.cfg.OneBot.HTTP.Prefix
	writeJSON(w, http.StatusOK, map[string]any{
		"self_id":     acc.SelfID,
		"token":       tok,
		"ws_shared":   base + up,
		"ws_token":    base + up + "/" + tok,
		"http_report": base + httpPrefix + "/" + tok,
	})
}
