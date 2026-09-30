package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/model"
)

const accountColumns = `id, self_id, name, nickname, avatar, enabled, status, tags, token_hash, source, last_seen_at, created_at, updated_at`

func scanAccount(row interface{ Scan(...any) error }) (model.Account, error) {
	var (
		acc                 model.Account
		enabled             int
		tags                string
		tokenHash           sql.NullString
		lastSeen            sql.NullInt64
		created, updatedAt  int64
	)
	if err := row.Scan(&acc.ID, &acc.SelfID, &acc.Name, &acc.Nickname, &acc.Avatar, &enabled, &acc.Status,
		&tags, &tokenHash, &acc.Source, &lastSeen, &created, &updatedAt); err != nil {
		return model.Account{}, wrapNotFound(err)
	}
	acc.Enabled = enabled == 1
	acc.HasToken = tokenHash.Valid && tokenHash.String != ""
	acc.Tags = json.RawMessage(jsonOrEmpty([]byte(tags), "[]"))
	if lastSeen.Valid {
		ts := unixToTime(lastSeen.Int64)
		acc.LastSeenAt = &ts
	}
	acc.CreatedAt = unixToTime(created)
	acc.UpdatedAt = unixToTime(updatedAt)
	return acc, nil
}

// AccountBySelfID looks up one QQ instance by its self_id.
func (s *Store) AccountBySelfID(ctx context.Context, selfID string) (model.Account, error) {
	row := s.db.QueryRowContext(ctx, "SELECT "+accountColumns+" FROM accounts WHERE self_id = ?", selfID)
	return scanAccount(row)
}

// AccountByID looks up one QQ instance by primary key.
func (s *Store) AccountByID(ctx context.Context, id int64) (model.Account, error) {
	row := s.db.QueryRowContext(ctx, "SELECT "+accountColumns+" FROM accounts WHERE id = ?", id)
	return scanAccount(row)
}

// AccountByTokenHash resolves a pre-bound per-instance token.
func (s *Store) AccountByTokenHash(ctx context.Context, tokenHash string) (model.Account, error) {
	row := s.db.QueryRowContext(ctx,
		"SELECT "+accountColumns+" FROM accounts WHERE token_hash IS NOT NULL AND token_hash = ?", tokenHash)
	return scanAccount(row)
}

// ListAccounts returns every QQ instance.
func (s *Store) ListAccounts(ctx context.Context) ([]model.Account, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+accountColumns+" FROM accounts ORDER BY self_id")
	if err != nil {
		return nil, fmt.Errorf("store: list accounts: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := []model.Account{}
	for rows.Next() {
		acc, err := scanAccount(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan account: %w", err)
		}
		out = append(out, acc)
	}
	return out, rows.Err()
}

// CreateAccount inserts a QQ instance (self_id is unique).
func (s *Store) CreateAccount(ctx context.Context, selfID, name, source string) (model.Account, error) {
	ts := now()
	res, err := s.db.ExecContext(ctx, `
INSERT INTO accounts (self_id, name, source, status, tags, created_at, updated_at)
VALUES (?, ?, ?, 'offline', '[]', ?, ?)`, selfID, name, source, ts, ts)
	if err != nil {
		return model.Account{}, fmt.Errorf("store: create account %s: %w", selfID, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return model.Account{}, fmt.Errorf("store: create account id: %w", err)
	}
	return s.AccountByID(ctx, id)
}

// EnsureAccount returns the account for selfID, creating it when missing.
func (s *Store) EnsureAccount(ctx context.Context, selfID, name, source string) (model.Account, error) {
	acc, err := s.AccountBySelfID(ctx, selfID)
	if err == nil {
		return acc, nil
	}
	if err != ErrNotFound && !strings.Contains(err.Error(), "not found") {
		return model.Account{}, err
	}
	return s.CreateAccount(ctx, selfID, name, source)
}

// UpdateAccountStatus stores the live status and last-seen timestamp.
func (s *Store) UpdateAccountStatus(ctx context.Context, id int64, status string, lastSeen time.Time) error {
	_, err := s.db.ExecContext(ctx,
		"UPDATE accounts SET status = ?, last_seen_at = ?, updated_at = ? WHERE id = ?",
		status, timeToUnix(lastSeen), now(), id)
	if err != nil {
		return fmt.Errorf("store: update account status: %w", err)
	}
	return nil
}

// UpdateAccountProfile refreshes cached metadata (nickname/avatar/name/tags).
func (s *Store) UpdateAccountProfile(ctx context.Context, acc model.Account) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE accounts SET name = ?, nickname = ?, avatar = ?, enabled = ?, tags = ?, updated_at = ? WHERE id = ?`,
		acc.Name, acc.Nickname, acc.Avatar, boolToInt(acc.Enabled), jsonOrEmpty(acc.Tags, "[]"), now(), acc.ID)
	if err != nil {
		return fmt.Errorf("store: update account profile: %w", err)
	}
	return nil
}

// SetAccountToken binds (or clears, with a nil hash) the per-instance token.
func (s *Store) SetAccountToken(ctx context.Context, id int64, tokenHash *string) error {
	var value any
	if tokenHash != nil {
		value = *tokenHash
	}
	if _, err := s.db.ExecContext(ctx,
		"UPDATE accounts SET token_hash = ?, updated_at = ? WHERE id = ?", value, now(), id); err != nil {
		return fmt.Errorf("store: set account token: %w", err)
	}
	return nil
}

// DeleteAccount removes a QQ instance (bindings cascade).
func (s *Store) DeleteAccount(ctx context.Context, id int64) error {
	if _, err := s.db.ExecContext(ctx, "DELETE FROM accounts WHERE id = ?", id); err != nil {
		return fmt.Errorf("store: delete account: %w", err)
	}
	return nil
}
