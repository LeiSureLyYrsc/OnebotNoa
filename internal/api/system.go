package api

import (
	"net/http"
	"time"
)

func (s *Server) handleSystemStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	userCount, err := s.opt.Store.CountUsers(ctx)
	if err != nil {
		s.logger.Error("count users", "error", err)
		writeError(w, http.StatusInternalServerError, "读取系统状态失败")
		return
	}
	auditCount, err := s.opt.Store.CountAudit(ctx)
	if err != nil {
		s.logger.Error("count audit", "error", err)
		writeError(w, http.StatusInternalServerError, "读取系统状态失败")
		return
	}

	listen := ""
	sessionTTL := time.Duration(0)
	if s.opt.Config != nil {
		listen = s.opt.Config.Server.Listen
		sessionTTL = s.opt.Config.Storage.SessionTTL.Std()
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"version":       s.opt.Version,
		"uptime_sec":    int(time.Since(s.opt.StartedAt).Seconds()),
		"started_at":    s.opt.StartedAt,
		"listen":        listen,
		"database":      s.opt.Store.Path(),
		"users":         userCount,
		"audit_entries": auditCount,
		"session_ttl":   sessionTTL.String(),
	})
}
