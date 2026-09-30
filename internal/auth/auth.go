// Package auth implements the management-plane authentication: password
// hashing, session tokens and CSRF tokens. The data plane (Bot and account
// tokens) uses the same token primitives but a separate, hash-only lookup.
package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/model"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/store"
)

// Errors returned by the manager.
var (
	ErrInvalidCredentials = errors.New("auth: invalid credentials")
	ErrNoSession          = errors.New("auth: no session")
	ErrSessionExpired     = errors.New("auth: session expired")
)

// Session bundles a validated session with its user and the plaintext token
// (only present right after Login).
type Session struct {
	Token   string
	Session model.Session
	User    model.User
}

// Manager creates and validates management sessions.
type Manager struct {
	st     *store.Store
	ttl    time.Duration
	logger *slog.Logger

	dummyOnce sync.Once
	dummyHash string
}

// NewManager builds a session manager.
func NewManager(st *store.Store, ttl time.Duration, logger *slog.Logger) *Manager {
	if ttl <= 0 {
		ttl = 7 * 24 * time.Hour
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Manager{st: st, ttl: ttl, logger: logger}
}

// TTL returns the configured session lifetime.
func (m *Manager) TTL() time.Duration { return m.ttl }

// dummyPasswordHash returns a throwaway hash so a login for a non-existent user
// costs the same as one for an existing user (no user-enumeration timing leak).
func (m *Manager) dummyPasswordHash() string {
	m.dummyOnce.Do(func() {
		h, err := HashPassword("not-a-real-password")
		if err != nil {
			h = ""
		}
		m.dummyHash = h
	})
	return m.dummyHash
}

// Login verifies credentials and creates a session.
func (m *Manager) Login(ctx context.Context, username, password, ip, userAgent string) (Session, error) {
	user, err := m.st.UserByUsername(ctx, username)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			_ = VerifyPassword(m.dummyPasswordHash(), password)
			return Session{}, ErrInvalidCredentials
		}
		return Session{}, fmt.Errorf("auth: load user: %w", err)
	}
	if !VerifyPassword(user.PasswordHash, password) {
		return Session{}, ErrInvalidCredentials
	}

	plain, hash, err := NewToken()
	if err != nil {
		return Session{}, fmt.Errorf("auth: generate session token: %w", err)
	}
	nowT := time.Now()
	sess := model.Session{
		TokenHash:  hash,
		UserID:     user.ID,
		CSRFToken:  NewCSRFToken(),
		CreatedAt:  nowT,
		ExpiresAt:  nowT.Add(m.ttl),
		LastSeenAt: nowT,
		UserAgent:  truncate(userAgent, 256),
		IP:         ip,
	}
	if err := m.st.CreateSession(ctx, sess); err != nil {
		return Session{}, err
	}

	if NeedsRehash(user.PasswordHash) {
		if rehashed, err := HashPassword(password); err == nil {
			if err := m.st.UpdateUserPassword(ctx, user.ID, rehashed); err != nil {
				m.logger.Warn("could not upgrade password hash", "user", user.Username, "error", err)
			} else {
				m.logger.Info("upgraded password hash parameters", "user", user.Username)
			}
		}
	}
	return Session{Token: plain, Session: sess, User: user}, nil
}

// Authenticate validates a session token.
func (m *Manager) Authenticate(ctx context.Context, token string) (Session, error) {
	if token == "" {
		return Session{}, ErrNoSession
	}
	hash := HashToken(token)
	sess, err := m.st.SessionByTokenHash(ctx, hash)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return Session{}, ErrNoSession
		}
		return Session{}, fmt.Errorf("auth: load session: %w", err)
	}
	nowT := time.Now()
	if nowT.After(sess.ExpiresAt) {
		_ = m.st.DeleteSession(ctx, hash)
		return Session{}, ErrSessionExpired
	}
	user, err := m.st.UserByID(ctx, sess.UserID)
	if err != nil {
		_ = m.st.DeleteSession(ctx, hash)
		return Session{}, ErrNoSession
	}
	if nowT.Sub(sess.LastSeenAt) > time.Minute {
		if err := m.st.TouchSession(ctx, hash, nowT); err != nil {
			m.logger.Warn("could not touch session", "error", err)
		}
		sess.LastSeenAt = nowT
	}
	return Session{Session: sess, User: user}, nil
}

// Logout deletes a session.
func (m *Manager) Logout(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	return m.st.DeleteSession(ctx, HashToken(token))
}

// Cleanup removes expired sessions and returns the number deleted.
func (m *Manager) Cleanup(ctx context.Context) (int64, error) {
	return m.st.DeleteExpiredSessions(ctx, time.Now())
}

// EnsureAdmin creates the initial administrator when no user exists yet and
// returns the generated password (empty when one was configured).
func EnsureAdmin(ctx context.Context, st *store.Store, configuredPassword string, logger *slog.Logger) (string, error) {
	n, err := st.CountUsers(ctx)
	if err != nil {
		return "", err
	}
	if n > 0 {
		return "", nil
	}

	generated := ""
	password := configuredPassword
	if password == "" {
		generated = NewSecret()
		password = generated
	}
	hash, err := HashPassword(password)
	if err != nil {
		return "", err
	}
	if _, err := st.CreateUser(ctx, "admin", hash, "admin"); err != nil {
		return "", err
	}
	if generated == "" {
		logger.Info("created initial administrator with the configured password", "username", "admin")
	}
	return generated, nil
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}
