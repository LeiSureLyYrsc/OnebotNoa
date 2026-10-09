package api

import (
	"encoding/json"
	"net/http"
)

func (s *Server) handleConsoleInvoke(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SelfID string          `json:"self_id"`
		Action string          `json:"action"`
		Params json.RawMessage `json:"params"`
	}
	if err := readJSON(r, &req); err != nil || req.SelfID == "" || req.Action == "" {
		writeError(w, http.StatusBadRequest, "self_id and action required")
		return
	}
	resp, err := s.hub.Invoke(req.SelfID, req.Action, req.Params)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"status": "failed", "retcode": 1500, "message": err.Error(), "data": nil})
		return
	}
	s.audit(r, "console.invoke", req.SelfID, req.Action)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(resp)
}
