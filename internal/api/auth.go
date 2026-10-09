package api

import (
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/auth"
)

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "version": s.version})
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	stats := s.hub.Stats().Snapshot()
	keys := make([]string, 0, len(stats))
	for k := range stats {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	for _, k := range keys {
		fmt.Fprintf(w, "onebotnoa_%s %d\n", k, stats[k])
	}
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	user, err := s.st.GetUserByUsername(req.Username)
	if err != nil || !auth.VerifyPassword(user.PasswordHash, req.Password) {
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	token, err := auth.NewSessionToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "session error")
		return
	}
	expires := time.Now().Add(7 * 24 * time.Hour)
	if err := s.st.CreateSession(token, user.ID, expires.Unix()); err != nil {
		writeError(w, http.StatusInternalServerError, "session error")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteLaxMode, Expires: expires,
	})
	s.audit(r, "login", user.Username, "")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "username": user.Username, "role": user.Role})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if tok := sessionToken(r); tok != "" {
		_ = s.st.DeleteSession(tok)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"username": currentUser(r)})
}
