package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/model"
)

// CountUsers reports how many management users exist (bootstrap check).
func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM users").Scan(&n); err != nil {
		return 0, fmt.Errorf("store: count users: %w", err)
	}
	return n, nil
}

// CreateUser inserts a management user.
func (s *Store) CreateUser(ctx context.Context, username, passwordHash, role string) (model.User, error) {
	ts := now()
	res, err := s.db.ExecContext(ctx,
		"INSERT INTO users (username, password_hash, role, created_at, updated_at) VALUES (?, ?, ?, ?, ?)",
		username, passwordHash, role, ts, ts)
	if err != nil {
		return model.User{}, fmt.Errorf("store: create user %q: %w", username, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return model.User{}, fmt.Errorf("store: create user id: %w", err)
	}
	return s.UserByID(ctx, id)
}

const userColumns = "id, username, password_hash, role, created_at, updated_at"

func scanUser(row interface{ Scan(...any) error }) (model.User, error) {
	var (
		u              model.User
		created, updat int64
	)
	if err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &created, &updat); err != nil {
		return model.User{}, wrapNotFound(err)
	}
	u.CreatedAt = unixToTime(created)
	u.UpdatedAt = unixToTime(updat)
	return u, nil
}

// UserByID loads one user.
func (s *Store) UserByID(ctx context.Context, id int64) (model.User, error) {
	row := s.db.QueryRowContext(ctx, "SELECT "+userColumns+" FROM users WHERE id = ?", id)
	return scanUser(row)
}

// UserByUsername loads one user by login name.
func (s *Store) UserByUsername(ctx context.Context, username string) (model.User, error) {
	row := s.db.QueryRowContext(ctx, "SELECT "+userColumns+" FROM users WHERE username = ?", username)
	return scanUser(row)
}

// ListUsers returns every management user.
func (s *Store) ListUsers(ctx context.Context) ([]model.User, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+userColumns+" FROM users ORDER BY id")
	if err != nil {
		return nil, fmt.Errorf("store: list users: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := []model.User{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan user: %w", err)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// UpdateUserPassword replaces the stored password hash.
func (s *Store) UpdateUserPassword(ctx context.Context, id int64, passwordHash string) error {
	res, err := s.db.ExecContext(ctx, "UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?",
		passwordHash, now(), id)
	if err != nil {
		return fmt.Errorf("store: update password: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: update password rows: %w", err)
	}
	if n == 0 {
		return wrapNotFound(sql.ErrNoRows)
	}
	return nil
}
