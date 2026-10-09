package api

import (
	"net/http"
	"strconv"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/model"
)

func (s *Server) handleBindingsList(w http.ResponseWriter, r *http.Request) {
	bindings, err := s.st.ListBindings()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if bindings == nil {
		bindings = []model.Binding{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"bindings": bindings})
}

func (s *Server) handleBindingCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		BotID     int64  `json:"bot_id"`
		AccountID int64  `json:"account_id"`
		Priority  int    `json:"priority"`
		IsDefault bool   `json:"is_default"`
		Scope     string `json:"scope"`
	}
	if err := readJSON(r, &req); err != nil || req.BotID == 0 || req.AccountID == 0 {
		writeError(w, http.StatusBadRequest, "bot_id and account_id required")
		return
	}
	if _, err := s.st.GetBot(req.BotID); err != nil {
		writeError(w, http.StatusNotFound, "bot not found")
		return
	}
	if _, err := s.st.GetAccount(req.AccountID); err != nil {
		writeError(w, http.StatusNotFound, "account not found")
		return
	}
	if req.Priority == 0 {
		req.Priority = 100
	}
	b, err := s.st.CreateBinding(req.BotID, req.AccountID, req.Priority, req.IsDefault, req.Scope)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	_ = s.hub.Reload()
	s.audit(r, "binding.create", strconv.FormatInt(b.ID, 10), "")
	writeJSON(w, http.StatusOK, b)
}

func (s *Server) handleBindingUpdate(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	b, err := s.st.GetBinding(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "binding not found")
		return
	}
	var req struct {
		Priority  *int    `json:"priority"`
		IsDefault *bool   `json:"is_default"`
		Enabled   *bool   `json:"enabled"`
		Scope     *string `json:"scope"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	priority, isDefault, enabled, scope := b.Priority, b.IsDefault, b.Enabled, b.Scope
	if req.Priority != nil {
		priority = *req.Priority
	}
	if req.IsDefault != nil {
		isDefault = *req.IsDefault
	}
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	if req.Scope != nil {
		scope = *req.Scope
	}
	if err := s.st.UpdateBinding(id, priority, isDefault, enabled, scope); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = s.hub.Reload()
	s.audit(r, "binding.update", strconv.FormatInt(id, 10), "")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleBindingDelete(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err := s.st.DeleteBinding(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = s.hub.Reload()
	s.audit(r, "binding.delete", strconv.FormatInt(id, 10), "")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
