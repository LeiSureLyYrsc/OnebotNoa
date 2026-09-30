package store

import (
	"context"
	"fmt"
	"time"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/model"
)

// CreateSession stores a new management session (keyed by token hash).
func (s *Store) CreateSession(ctx context.Context, sess model.Session) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO sessions (token_hash, user_id, csrf_token, created_at, expires_at, last_seen_at, user_agent, ip)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		sess.TokenHash, sess.UserID, sess.CSRFToken,
		timeToUnix(sess.CreatedAt), timeToUnix(sess.ExpiresAt), timeToUnix(sess.LastSeenAt),
		sess.UserAgent, sess.IP)
	if err != nil {
		return fmt.Errorf("store: create session: %w", err)
	}
	return nil
}

// SessionByTokenHash loads a session by the hash of its bearer token.
func (s *Store) SessionByTokenHash(ctx context.Context, tokenHash string) (model.Session, error) {
	var (
		sess                           model.Session
		created, expires, lastSeenUnix int64
	)
	row := s.db.QueryRowContext(ctx, `
SELECT token_hash, user_id, csrf_token, created_at, expires_at, last_seen_at, user_agent, ip
FROM sessions WHERE token_hash = ?`, tokenHash)
	if err := row.Scan(&sess.TokenHash, &sess.UserID, &sess.CSRFToken, &created, &expires, &lastSeenUnix,
		&sess.UserAgent, &sess.IP); err != nil {
		return model.Session{}, wrapNotFound(err)
	}
	sess.CreatedAt = unixToTime(created)
	sess.ExpiresAt = unixToTime(expires)
	sess.LastSeenAt = unixToTime(lastSeenUnix)
	return sess, nil
}

// TouchSession extends the idle bookkeeping for a session.
func (s *Store) TouchSession(ctx context.Context, tokenHash string, at time.Time) error {
	if _, err := s.db.ExecContext(ctx, "UPDATE sessions SET last_seen_at = ? WHERE token_hash = ?",
		timeToUnix(at), tokenHash); err != nil {
		return fmt.Errorf("store: touch session: %w", err)
	}
	return nil
}

// DeleteSession removes one session (logout).
func (s *Store) DeleteSession(ctx context.Context, tokenHash string) error {
	if _, err := s.db.ExecContext(ctx, "DELETE FROM sessions WHERE token_hash = ?", tokenHash); err != nil {
		return fmt.Errorf("store: delete session: %w", err)
	}
	return nil
}

// DeleteSessionsForUser removes every session of a user.
func (s *Store) DeleteSessionsForUser(ctx context.Context, userID int64) (int64, error) {
	res, err := s.db.ExecContext(ctx, "DELETE FROM sessions WHERE user_id = ?", userID)
	if err != nil {
		return 0, fmt.Errorf("store: delete user sessions: %w", err)
	}
	return res.RowsAffected()
}

// DeleteExpiredSessions drops sessions whose expiry is in the past.
func (s *Store) DeleteExpiredSessions(ctx context.Context, nowT time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, "DELETE FROM sessions WHERE expires_at <= ?", timeToUnix(nowT))
	if err != nil {
		return 0, fmt.Errorf("store: delete expired sessions: %w", err)
	}
	return res.RowsAffected()
}
