package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/hub"
)

// handleListAudit returns the newest audit entries, newest first.
func (s *Server) handleListAudit(w http.ResponseWriter, r *http.Request) {
	limit := 200
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if value, err := strconv.Atoi(raw); err == nil {
			limit = value
		}
	}
	offset := 0
	if raw := r.URL.Query().Get("offset"); raw != "" {
		if value, err := strconv.Atoi(raw); err == nil && value >= 0 {
			offset = value
		}
	}

	entries, err := s.opt.Store.ListAudit(r.Context(), limit, offset)
	if err != nil {
		s.fail(w, "读取审计日志失败", err)
		return
	}
	total, err := s.opt.Store.CountAudit(r.Context())
	if err != nil {
		s.fail(w, "统计审计日志失败", err)
		return
	}

	// Optional filtering in memory: the table stays small and this keeps the
	// store API simple.
	action := strings.TrimSpace(r.URL.Query().Get("action"))
	query := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	if action != "" || query != "" {
		filtered := entries[:0]
		for _, entry := range entries {
			if action != "" && !strings.Contains(entry.Action, action) {
				continue
			}
			if query != "" {
				haystack := strings.ToLower(entry.Actor + " " + entry.Action + " " + entry.Target + " " + entry.Detail + " " + entry.IP)
				if !strings.Contains(haystack, query) {
					continue
				}
			}
			filtered = append(filtered, entry)
		}
		entries = filtered
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"entries": entries,
		"total":   total,
	})
}

// handleLogs tails the in-memory live view as a plain log for the WebUI.
//
// It is intentionally the same ring the live stream uses: operators debug the
// relay from the traffic it carried, not from a second logging pipeline.
func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	if s.opt.Events == nil {
		writeJSON(w, http.StatusOK, map[string]any{"logs": []any{}})
		return
	}
	limit := 300
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if value, err := strconv.Atoi(raw); err == nil {
			limit = value
		}
	}
	filter := hub.EventFilter{
		SelfID:   r.URL.Query().Get("self_id"),
		Bot:      r.URL.Query().Get("bot"),
		Kind:     r.URL.Query().Get("kind"),
		PostType: r.URL.Query().Get("post_type"),
		GroupID:  r.URL.Query().Get("group_id"),
		Query:    r.URL.Query().Get("q"),
	}
	records := s.opt.Events.Recent(limit, filter)

	// Newest first reads better in a log pane.
	for i, j := 0, len(records)-1; i < j; i, j = i+1, j-1 {
		records[i], records[j] = records[j], records[i]
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"logs":        records,
		"ring_size":   s.opt.Events.Len(),
		"subscribers": s.opt.Events.Subscribers(),
		"dropped":     s.opt.Events.Dropped(),
	})
}
