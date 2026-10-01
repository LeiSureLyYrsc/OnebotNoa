package api

import (
	"net/http"
	"time"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/hub"
)

// policyLimiter returns the policy engine's limiter when one is configured.
func (s *Server) policyLimiter() *hub.Limiter {
	if s.opt.Policy == nil {
		return nil
	}
	return s.opt.Policy.Limiter()
}

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

	payload := map[string]any{
		"version":       s.opt.Version,
		"uptime_sec":    int(time.Since(s.opt.StartedAt).Seconds()),
		"started_at":    s.opt.StartedAt,
		"listen":        listen,
		"database":      s.opt.Store.Path(),
		"users":         userCount,
		"audit_entries": auditCount,
		"session_ttl":   sessionTTL.String(),
	}

	if s.opt.Hub != nil {
		st := s.opt.Hub.Status()
		payload["relay"] = map[string]any{
			"accounts":         st.Accounts,
			"online":           st.Online,
			"degraded":         st.Degraded,
			"offline":          st.Offline,
			"upstream_conns":   st.UpstreamConns,
			"downstream_conns": st.DownstreamConns,
			"pending_accounts": st.PendingAccounts,
			"pending_actions":  st.PendingActions,
		}
	}
	if s.opt.Events != nil {
		payload["events"] = map[string]any{
			"ring_size":   s.opt.Events.Len(),
			"subscribers": s.opt.Events.Subscribers(),
			"dropped":     s.opt.Events.Dropped(),
		}
	}
	if s.opt.Metrics != nil {
		payload["traffic"] = s.opt.Metrics.Snapshot(s.policyLimiter(), s.opt.Policy)
	}
	writeJSON(w, http.StatusOK, payload)
}
