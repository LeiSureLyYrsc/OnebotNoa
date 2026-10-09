package api

import (
	"net/http"
	"strconv"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/model"
)

func (s *Server) handleListenersList(w http.ResponseWriter, r *http.Request) {
	listeners, err := s.st.ListListeners()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if listeners == nil {
		listeners = []model.Listener{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"listeners": listeners})
}

func (s *Server) handleListenerCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name      string `json:"name"`
		Role      string `json:"role"`
		BindAddr  string `json:"bind_addr"`
		Path      string `json:"path"`
		AccountID *int64 `json:"account_id"`
		BotID     *int64 `json:"bot_id"`
		Enabled   *bool  `json:"enabled"`
	}
	if err := readJSON(r, &req); err != nil || req.BindAddr == "" {
		writeError(w, http.StatusBadRequest, "bind_addr required")
		return
	}
	if req.Role == "" {
		req.Role = model.RoleUpstream
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	l := model.Listener{Name: req.Name, Role: req.Role, BindAddr: req.BindAddr, Path: req.Path,
		AccountID: req.AccountID, BotID: req.BotID, Enabled: enabled}
	created, err := s.st.CreateListener(l)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if s.syncListeners != nil {
		if err := s.syncListeners(); err != nil {
			s.log.Warn("sync listeners", "err", err)
		}
	}
	s.audit(r, "listener.create", req.Name, req.BindAddr)
	writeJSON(w, http.StatusOK, created)
}

func (s *Server) handleListenerUpdate(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	l, err := s.st.GetListener(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "listener not found")
		return
	}
	var req struct {
		Name      *string `json:"name"`
		Role      *string `json:"role"`
		BindAddr  *string `json:"bind_addr"`
		Path      *string `json:"path"`
		AccountID *int64  `json:"account_id"`
		BotID     *int64  `json:"bot_id"`
		Enabled   *bool   `json:"enabled"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.Name != nil {
		l.Name = *req.Name
	}
	if req.Role != nil {
		l.Role = *req.Role
	}
	if req.BindAddr != nil {
		l.BindAddr = *req.BindAddr
	}
	if req.Path != nil {
		l.Path = *req.Path
	}
	if req.AccountID != nil {
		l.AccountID = req.AccountID
	}
	if req.BotID != nil {
		l.BotID = req.BotID
	}
	if req.Enabled != nil {
		l.Enabled = *req.Enabled
	}
	if err := s.st.UpdateListener(l); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if s.syncListeners != nil {
		if err := s.syncListeners(); err != nil {
			s.log.Warn("sync listeners", "err", err)
		}
	}
	s.audit(r, "listener.update", strconv.FormatInt(id, 10), "")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleListenerDelete(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err := s.st.DeleteListener(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if s.syncListeners != nil {
		if err := s.syncListeners(); err != nil {
			s.log.Warn("sync listeners", "err", err)
		}
	}
	s.audit(r, "listener.delete", strconv.FormatInt(id, 10), "")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
