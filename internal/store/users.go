package store

import (
	"database/sql"
	"errors"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/model"
)

// CountUsers returns the number of management users.
func (s *Store) CountUsers() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

// CreateUser inserts a management user.
func (s *Store) CreateUser(username, passwordHash, role string) (model.User, error) {
	if role == "" {
		role = "admin"
	}
	res, err := s.db.Exec(`INSERT INTO users (username, password_hash, role, created_at) VALUES (?, ?, ?, ?)`,
		username, passwordHash, role, now())
	if err != nil {
		return model.User{}, err
	}
	id, _ := res.LastInsertId()
	return s.GetUser(id)
}

// GetUser loads one user by id.
func (s *Store) GetUser(id int64) (model.User, error) {
	row := s.db.QueryRow(`SELECT id, username, password_hash, role, created_at FROM users WHERE id = ?`, id)
	return scanUser(row)
}

// GetUserByUsername loads one user by username.
func (s *Store) GetUserByUsername(username string) (model.User, error) {
	row := s.db.QueryRow(`SELECT id, username, password_hash, role, created_at FROM users WHERE username = ?`, username)
	return scanUser(row)
}

func scanUser(row interface{ Scan(...any) error }) (model.User, error) {
	var u model.User
	err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return model.User{}, ErrNotFound
	}
	return u, err
}

// UpdatePassword changes a user's password hash.
func (s *Store) UpdatePassword(userID int64, passwordHash string) error {
	_, err := s.db.Exec(`UPDATE users SET password_hash = ? WHERE id = ?`, passwordHash, userID)
	return err
}

// CreateSession stores a login session.
func (s *Store) CreateSession(token string, userID, expiresAt int64) error {
	_, err := s.db.Exec(`INSERT INTO sessions (token, user_id, expires_at) VALUES (?, ?, ?)`, token, userID, expiresAt)
	return err
}

// GetSession loads a session by token.
func (s *Store) GetSession(token string) (model.Session, error) {
	var sess model.Session
	sess.Token = token
	err := s.db.QueryRow(`SELECT user_id, expires_at FROM sessions WHERE token = ?`, token).Scan(&sess.UserID, &sess.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Session{}, ErrNotFound
	}
	return sess, err
}

// DeleteSession removes one session.
func (s *Store) DeleteSession(token string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE token = ?`, token)
	return err
}

// DeleteSessionsByUser removes all sessions for one user.
func (s *Store) DeleteSessionsByUser(userID int64) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE user_id = ?`, userID)
	return err
}

// PurgeExpiredSessions removes sessions whose expiry has passed.
func (s *Store) PurgeExpiredSessions(nowUnix int64) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE expires_at < ?`, nowUnix)
	return err
}

// AddAudit appends an audit entry.
func (s *Store) AddAudit(actor, action, target, detail, ip string) error {
	_, err := s.db.Exec(`INSERT INTO audit_log (at, actor, action, target, detail, ip) VALUES (?, ?, ?, ?, ?, ?)`,
		now(), actor, action, target, detail, ip)
	return err
}

// ListAudit returns the most recent audit entries, newest first.
func (s *Store) ListAudit(limit int) ([]model.AuditEntry, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := s.db.Query(`SELECT id, at, actor, action, target, detail, ip FROM audit_log ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.AuditEntry
	for rows.Next() {
		var e model.AuditEntry
		if err := rows.Scan(&e.ID, &e.At, &e.Actor, &e.Action, &e.Target, &e.Detail, &e.IP); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
