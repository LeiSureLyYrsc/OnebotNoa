// Package api serves the management REST API, the live SSE event stream, and the
// embedded WebUI.
package api

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/auth"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/config"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/connect"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/hub"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/store"
)

const sessionCookie = "onebotnoa_session"

// Server serves the management plane.
type Server struct {
	cfg           config.Config
	hub           *hub.Hub
	st            *store.Store
	vault         *connect.Vault
	log           *slog.Logger
	version       string
	syncListeners func() error
	startedAt     time.Time
}

// New constructs the management API server.
func New(cfg config.Config, h *hub.Hub, st *store.Store, vault *connect.Vault, log *slog.Logger, version string, syncListeners func() error) *Server {
	return &Server{cfg: cfg, hub: h, st: st, vault: vault, log: log, version: version, syncListeners: syncListeners, startedAt: time.Now()}
}

// Register attaches every management route to mux.
func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /metrics", s.handleMetrics)

	mux.HandleFunc("POST /api/v1/login", s.handleLogin)
	mux.HandleFunc("POST /api/v1/logout", s.handleLogout)
	mux.HandleFunc("GET /api/v1/me", s.requireAuth(s.handleMe))

	mux.HandleFunc("GET /api/v1/accounts", s.requireAuth(s.handleAccountsList))
	mux.HandleFunc("POST /api/v1/accounts", s.requireAuth(s.handleAccountCreate))
	mux.HandleFunc("POST /api/v1/accounts/approve", s.requireAuth(s.handleAccountApprove))
	mux.HandleFunc("GET /api/v1/accounts/{id}", s.requireAuth(s.handleAccountGet))
	mux.HandleFunc("PATCH /api/v1/accounts/{id}", s.requireAuth(s.handleAccountUpdate))
	mux.HandleFunc("DELETE /api/v1/accounts/{id}", s.requireAuth(s.handleAccountDelete))
	mux.HandleFunc("POST /api/v1/accounts/{id}/token/rotate", s.requireAuth(s.handleAccountRotate))
	mux.HandleFunc("GET /api/v1/accounts/{id}/connect", s.requireAuth(s.handleAccountConnect))

	mux.HandleFunc("GET /api/v1/bots", s.requireAuth(s.handleBotsList))
	mux.HandleFunc("POST /api/v1/bots", s.requireAuth(s.handleBotCreate))
	mux.HandleFunc("GET /api/v1/bots/{id}", s.requireAuth(s.handleBotGet))
	mux.HandleFunc("PATCH /api/v1/bots/{id}", s.requireAuth(s.handleBotUpdate))
	mux.HandleFunc("DELETE /api/v1/bots/{id}", s.requireAuth(s.handleBotDelete))
	mux.HandleFunc("POST /api/v1/bots/{id}/token/rotate", s.requireAuth(s.handleBotRotate))
	mux.HandleFunc("GET /api/v1/bots/{id}/connect", s.requireAuth(s.handleBotConnect))

	mux.HandleFunc("GET /api/v1/bindings", s.requireAuth(s.handleBindingsList))
	mux.HandleFunc("POST /api/v1/bindings", s.requireAuth(s.handleBindingCreate))
	mux.HandleFunc("PATCH /api/v1/bindings/{id}", s.requireAuth(s.handleBindingUpdate))
	mux.HandleFunc("DELETE /api/v1/bindings/{id}", s.requireAuth(s.handleBindingDelete))

	mux.HandleFunc("GET /api/v1/listeners", s.requireAuth(s.handleListenersList))
	mux.HandleFunc("POST /api/v1/listeners", s.requireAuth(s.handleListenerCreate))
	mux.HandleFunc("PATCH /api/v1/listeners/{id}", s.requireAuth(s.handleListenerUpdate))
	mux.HandleFunc("DELETE /api/v1/listeners/{id}", s.requireAuth(s.handleListenerDelete))

	mux.HandleFunc("GET /api/v1/connections", s.requireAuth(s.handleConnections))
	mux.HandleFunc("GET /api/v1/events/stream", s.requireAuth(s.handleEventStream))
	mux.HandleFunc("GET /api/v1/events/recent", s.requireAuth(s.handleEventsRecent))
	mux.HandleFunc("POST /api/v1/console/invoke", s.requireAuth(s.handleConsoleInvoke))

	mux.HandleFunc("GET /api/v1/system", s.requireAuth(s.handleSystem))
	mux.HandleFunc("GET /api/v1/audit", s.requireAuth(s.handleAudit))
	mux.HandleFunc("GET /api/v1/connect", s.requireAuth(s.handleConnectExport))
	mux.HandleFunc("PUT /api/v1/connect", s.requireAuth(s.handleConnectImport))
}

// Bootstrap ensures an administrator exists and returns the password when one
// had to be generated.
func (s *Server) Bootstrap() (string, error) {
	n, err := s.st.CountUsers()
	if err != nil {
		return "", err
	}
	if n > 0 {
		return "", nil
	}
	password := s.cfg.Server.AdminPassword
	generated := false
	if password == "" {
		password = randomPassword()
		generated = true
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return "", err
	}
	if _, err := s.st.CreateUser("admin", hash, "admin"); err != nil {
		return "", err
	}
	if generated {
		return password, nil
	}
	return "", nil
}

func randomPassword() string {
	buf := make([]byte, 12)
	_, _ = rand.Read(buf)
	return base64.RawURLEncoding.EncodeToString(buf)
}

type ctxKey string

const userCtx ctxKey = "user"

func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok := sessionToken(r)
		if tok == "" {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		sess, err := s.st.GetSession(tok)
		if err != nil || sess.ExpiresAt < time.Now().Unix() {
			writeError(w, http.StatusUnauthorized, "session expired")
			return
		}
		user, err := s.st.GetUser(sess.UserID)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unknown user")
			return
		}
		ctx := context.WithValue(r.Context(), userCtx, user)
		next(w, r.WithContext(ctx))
	}
}

func sessionToken(r *http.Request) string {
	if c, err := r.Cookie(sessionCookie); err == nil {
		return c.Value
	}
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
	}
	return ""
}

func currentUser(r *http.Request) string {
	if u, ok := r.Context().Value(userCtx).(string); ok {
		return u
	}
	return "admin"
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func readJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(v)
}

func (s *Server) audit(r *http.Request, action, target, detail string) {
	ip := clientIP(r, s.cfg.Server.TrustProxy)
	if err := s.st.AddAudit(currentUser(r), action, target, detail, ip); err != nil {
		s.log.Warn("audit write failed", "err", err)
	}
}

func clientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			return strings.TrimSpace(strings.Split(xff, ",")[0])
		}
	}
	host := r.RemoteAddr
	if i := strings.LastIndex(host, ":"); i > 0 {
		host = host[:i]
	}
	return host
}

func baseURL(r *http.Request, trustProxy bool) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if trustProxy {
		if xf := r.Header.Get("X-Forwarded-Proto"); xf != "" {
			scheme = strings.TrimSpace(strings.Split(xf, ",")[0])
		}
	}
	return fmt.Sprintf("%s://%s", scheme, r.Host)
}

func mustQuote(s string) json.RawMessage {
	b, _ := json.Marshal(s)
	return b
}
