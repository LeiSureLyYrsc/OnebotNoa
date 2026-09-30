// Package api implements the management REST API (and the SSE stream) that the
// WebUI talks to. The data plane (OneBot) lives in internal/transport.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/auth"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/config"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/hub"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/model"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/store"
)

const (
	sessionCookie = "hub_session"
	maxJSONBody   = 1 << 20 // 1 MiB
)

// Options wires the API server to the rest of the process.
type Options struct {
	Store     *store.Store
	Auth      *auth.Manager
	Logger    *slog.Logger
	Config    *config.Config
	Hub       *hub.Hub
	Events    *hub.EventLog
	Version   string
	StartedAt time.Time
}

// Server serves /api/v1 and the SSE stream.
type Server struct {
	opt     Options
	logger  *slog.Logger
	limiter *loginLimiter
}

// New builds the API server.
func New(opt Options) *Server {
	logger := opt.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{
		opt:     opt,
		logger:  logger,
		limiter: newLoginLimiter(8, 5*time.Minute),
	}
}

// Register mounts every management route on mux.
func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/auth/login", s.handleLogin)
	mux.HandleFunc("POST /api/v1/auth/logout", s.requireAuth(s.handleLogout))
	mux.HandleFunc("GET /api/v1/auth/me", s.requireAuth(s.handleMe))

	mux.HandleFunc("GET /api/v1/system/status", s.requireAuth(s.handleSystemStatus))

	// QQ instances (accounts)
	mux.HandleFunc("GET /api/v1/accounts", s.requireAuth(s.handleListAccounts))
	mux.HandleFunc("POST /api/v1/accounts", s.requireAuth(s.handleCreateAccount))
	mux.HandleFunc("GET /api/v1/accounts/pending", s.requireAuth(s.handleListPending))
	mux.HandleFunc("POST /api/v1/accounts/pending/approve", s.requireAuth(s.handleApprovePending))
	mux.HandleFunc("POST /api/v1/accounts/pending/reject", s.requireAuth(s.handleRejectPending))
	mux.HandleFunc("GET /api/v1/accounts/{id}", s.requireAuth(s.handleGetAccount))
	mux.HandleFunc("PATCH /api/v1/accounts/{id}", s.requireAuth(s.handleUpdateAccount))
	mux.HandleFunc("DELETE /api/v1/accounts/{id}", s.requireAuth(s.handleDeleteAccount))
	mux.HandleFunc("POST /api/v1/accounts/{id}/token", s.requireAuth(s.handleRotateAccountToken))
	mux.HandleFunc("DELETE /api/v1/accounts/{id}/token", s.requireAuth(s.handleClearAccountToken))

	// Bot applications
	mux.HandleFunc("GET /api/v1/bots", s.requireAuth(s.handleListBots))
	mux.HandleFunc("POST /api/v1/bots", s.requireAuth(s.handleCreateBot))
	mux.HandleFunc("GET /api/v1/bots/{id}", s.requireAuth(s.handleGetBot))
	mux.HandleFunc("PATCH /api/v1/bots/{id}", s.requireAuth(s.handleUpdateBot))
	mux.HandleFunc("DELETE /api/v1/bots/{id}", s.requireAuth(s.handleDeleteBot))
	mux.HandleFunc("POST /api/v1/bots/{id}/token/rotate", s.requireAuth(s.handleRotateBotToken))

	// Bindings (which Bot may use which account)
	mux.HandleFunc("GET /api/v1/bindings", s.requireAuth(s.handleListBindings))
	mux.HandleFunc("POST /api/v1/bindings", s.requireAuth(s.handleCreateBinding))
	mux.HandleFunc("PATCH /api/v1/bindings/{id}", s.requireAuth(s.handleUpdateBinding))
	mux.HandleFunc("DELETE /api/v1/bindings/{id}", s.requireAuth(s.handleDeleteBinding))

	// Dedicated listeners (runtime hot-reload arrives in I9)
	mux.HandleFunc("GET /api/v1/listeners", s.requireAuth(s.handleListListeners))
	mux.HandleFunc("POST /api/v1/listeners", s.requireAuth(s.handleCreateListener))
	mux.HandleFunc("PATCH /api/v1/listeners/{id}", s.requireAuth(s.handleUpdateListener))
	mux.HandleFunc("DELETE /api/v1/listeners/{id}", s.requireAuth(s.handleDeleteListener))

	// Live activity
	mux.HandleFunc("GET /api/v1/events/recent", s.requireAuth(s.handleRecentEvents))
	mux.HandleFunc("GET /api/v1/events/stream", s.requireAuth(s.handleEventStream))
}

// ---------------------------------------------------------------- middleware

type ctxKey int

const sessionCtxKey ctxKey = iota

// sessionFrom returns the authenticated session stored by requireAuth.
func sessionFrom(ctx context.Context) (auth.Session, bool) {
	sess, ok := ctx.Value(sessionCtxKey).(auth.Session)
	return sess, ok
}

// requireAuth enforces a valid session cookie and, for unsafe methods, a
// matching CSRF token.
func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookie)
		if err != nil || cookie.Value == "" {
			writeError(w, http.StatusUnauthorized, "未登录")
			return
		}
		sess, err := s.opt.Auth.Authenticate(r.Context(), cookie.Value)
		if err != nil {
			if !errors.Is(err, auth.ErrNoSession) && !errors.Is(err, auth.ErrSessionExpired) {
				s.logger.Error("session lookup failed", "error", err)
			}
			s.clearSessionCookie(w, r)
			writeError(w, http.StatusUnauthorized, "登录已过期，请重新登录")
			return
		}
		if isUnsafeMethod(r.Method) && !csrfMatches(r, sess.Session.CSRFToken) {
			writeError(w, http.StatusForbidden, "CSRF 校验失败，请刷新页面")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), sessionCtxKey, sess)))
	}
}

func isUnsafeMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	default:
		return true
	}
}

func csrfMatches(r *http.Request, want string) bool {
	if want == "" {
		return false
	}
	got := r.Header.Get("X-CSRF-Token")
	if got == "" {
		return false
	}
	return auth.TokenMatches(got, auth.HashToken(want))
}

// -------------------------------------------------------------- http helpers

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func decodeJSON(r *http.Request, dst any) error {
	defer func() { _ = r.Body.Close() }()
	dec := json.NewDecoder(io.LimitReader(r.Body, maxJSONBody))
	if err := dec.Decode(dst); err != nil {
		return err
	}
	return nil
}

// jsonKindProblem validates an optional JSON column. kind is "object" or
// "array"; an empty raw value means "leave unchanged".
func jsonKindProblem(raw json.RawMessage, kind string) string {
	if len(raw) == 0 {
		return ""
	}
	if !json.Valid(raw) {
		return "不是合法的 JSON"
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return ""
	}
	switch kind {
	case "object":
		if trimmed[0] != '{' {
			return "必须是 JSON 对象"
		}
	case "array":
		if trimmed[0] != '[' {
			return "必须是 JSON 数组"
		}
	}
	return ""
}

// ------------------------------------------------------------- cookies / ips

func (s *Server) secureCookie(r *http.Request) bool {
	if s.opt.Config != nil && s.opt.Config.Server.TLSCert != "" {
		return true
	}
	if s.opt.Config != nil && s.opt.Config.Server.TrustProxy {
		if proto := r.Header.Get("X-Forwarded-Proto"); strings.EqualFold(proto, "https") {
			return true
		}
	}
	return false
}

func (s *Server) setSessionCookie(w http.ResponseWriter, r *http.Request, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.secureCookie(r),
		SameSite: http.SameSiteLaxMode,
		Expires:  expires,
	})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   s.secureCookie(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

func (s *Server) clientIP(r *http.Request) string {
	if s.opt.Config != nil && s.opt.Config.Server.TrustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			if idx := strings.IndexByte(xff, ','); idx > 0 {
				return strings.TrimSpace(xff[:idx])
			}
			return strings.TrimSpace(xff)
		}
		if xri := r.Header.Get("X-Real-IP"); xri != "" {
			return strings.TrimSpace(xri)
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// audit records a management action; failures are logged, never fatal.
func (s *Server) audit(r *http.Request, actor, action, target, detail string) {
	entry := model.AuditEntry{
		At:     time.Now(),
		Actor:  actor,
		Action: action,
		Target: target,
		Detail: detail,
		IP:     s.clientIP(r),
	}
	if err := s.opt.Store.AppendAudit(r.Context(), entry); err != nil {
		s.logger.Warn("could not write audit entry", "action", action, "error", err)
	}
}
