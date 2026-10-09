package api

import (
	"net/http"
	"strconv"
	"time"
)

func (s *Server) handleSystem(w http.ResponseWriter, r *http.Request) {
	accounts, _ := s.st.ListAccounts()
	bots, _ := s.st.ListBots()
	snap := s.hub.SnapshotAccounts()
	online := 0
	for _, rt := range snap {
		if rt.Status != "offline" {
			online++
		}
	}
	botsOnline := 0
	for _, b := range bots {
		if s.hub.BotOnlineCount(b.ID) > 0 {
			botsOnline++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"version":          s.version,
		"uptime_seconds":   int64(time.Since(s.startedAt).Seconds()),
		"accounts_total":   len(accounts),
		"accounts_online":  online,
		"bots_total":       len(bots),
		"bots_online":      botsOnline,
		"pending_accounts": s.hub.PendingAccounts(),
		"metrics":          s.hub.Stats().Snapshot(),
	})
}

func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	limit := 200
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	entries, err := s.st.ListAudit(limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"audit": entries})
}

func (s *Server) handleConnections(w http.ResponseWriter, r *http.Request) {
	snap := s.hub.SnapshotAccounts()
	type connRow struct {
		SelfID string `json:"self_id"`
		PeerID string `json:"peer_id"`
		Role   string `json:"role"`
	}
	var rows []connRow
	for selfID, rt := range snap {
		for _, c := range rt.Conns {
			rows = append(rows, connRow{SelfID: selfID, PeerID: c.PeerID, Role: c.Role})
		}
	}
	if rows == nil {
		rows = []connRow{}
	}
	bots, _ := s.st.ListBots()
	type botRow struct {
		ID     int64  `json:"id"`
		Name   string `json:"name"`
		Online int    `json:"online"`
	}
	var botRows []botRow
	for _, b := range bots {
		botRows = append(botRows, botRow{ID: b.ID, Name: b.Name, Online: s.hub.BotOnlineCount(b.ID)})
	}
	if botRows == nil {
		botRows = []botRow{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"upstream": rows, "downstream": botRows})
}
