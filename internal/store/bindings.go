package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/model"
)

const bindingColumns = `id, bot_id, account_id, priority, is_default, enabled, scope, created_at, updated_at`

func scanBinding(row interface{ Scan(...any) error }) (model.Binding, error) {
	var (
		b                  model.Binding
		isDefault, enabled int
		scope              string
		created, updatedAt int64
	)
	if err := row.Scan(&b.ID, &b.BotID, &b.AccountID, &b.Priority, &isDefault, &enabled, &scope,
		&created, &updatedAt); err != nil {
		return model.Binding{}, wrapNotFound(err)
	}
	b.IsDefault = isDefault == 1
	b.Enabled = enabled == 1
	b.Scope = json.RawMessage(jsonOrEmpty([]byte(scope), "{}"))
	b.CreatedAt = unixToTime(created)
	b.UpdatedAt = unixToTime(updatedAt)
	return b, nil
}

func (s *Store) queryBindings(ctx context.Context, query string, args ...any) ([]model.Binding, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list bindings: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := []model.Binding{}
	for rows.Next() {
		b, err := scanBinding(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan binding: %w", err)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// ListBindings returns every grant.
func (s *Store) ListBindings(ctx context.Context) ([]model.Binding, error) {
	return s.queryBindings(ctx, "SELECT "+bindingColumns+" FROM bindings ORDER BY account_id, priority DESC")
}

// BindingsByBot returns the accounts granted to one Bot (highest priority first).
func (s *Store) BindingsByBot(ctx context.Context, botID int64) ([]model.Binding, error) {
	return s.queryBindings(ctx,
		"SELECT "+bindingColumns+" FROM bindings WHERE bot_id = ? ORDER BY priority DESC, account_id", botID)
}

// BindingsByAccount returns the Bots allowed to use one account.
func (s *Store) BindingsByAccount(ctx context.Context, accountID int64) ([]model.Binding, error) {
	return s.queryBindings(ctx,
		"SELECT "+bindingColumns+" FROM bindings WHERE account_id = ? ORDER BY priority DESC, bot_id", accountID)
}

// BindingByPair returns one grant.
func (s *Store) BindingByPair(ctx context.Context, botID, accountID int64) (model.Binding, error) {
	row := s.db.QueryRowContext(ctx,
		"SELECT "+bindingColumns+" FROM bindings WHERE bot_id = ? AND account_id = ?", botID, accountID)
	return scanBinding(row)
}

// CreateBinding grants an account to a Bot.
func (s *Store) CreateBinding(ctx context.Context, b model.Binding) (model.Binding, error) {
	ts := now()
	if b.Priority == 0 {
		b.Priority = 100
	}
	res, err := s.db.ExecContext(ctx, `
INSERT INTO bindings (bot_id, account_id, priority, is_default, enabled, scope, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		b.BotID, b.AccountID, b.Priority, boolToInt(b.IsDefault), boolToInt(b.Enabled),
		jsonOrEmpty(b.Scope, "{}"), ts, ts)
	if err != nil {
		return model.Binding{}, fmt.Errorf("store: create binding: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return model.Binding{}, fmt.Errorf("store: create binding id: %w", err)
	}
	return s.BindingByID(ctx, id)
}

// BindingByID loads one grant.
func (s *Store) BindingByID(ctx context.Context, id int64) (model.Binding, error) {
	row := s.db.QueryRowContext(ctx, "SELECT "+bindingColumns+" FROM bindings WHERE id = ?", id)
	return scanBinding(row)
}

// UpdateBinding refreshes a grant and, when it becomes the default for its Bot,
// clears the flag on the Bot's other grants so only one default can exist.
func (s *Store) UpdateBinding(ctx context.Context, b model.Binding) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin binding update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if b.IsDefault {
		if _, err := tx.ExecContext(ctx,
			"UPDATE bindings SET is_default = 0 WHERE bot_id = ? AND id <> ?", b.BotID, b.ID); err != nil {
			return fmt.Errorf("store: clear default bindings: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE bindings SET priority = ?, is_default = ?, enabled = ?, scope = ?, updated_at = ? WHERE id = ?`,
		b.Priority, boolToInt(b.IsDefault), boolToInt(b.Enabled), jsonOrEmpty(b.Scope, "{}"), now(), b.ID); err != nil {
		return fmt.Errorf("store: update binding: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit binding update: %w", err)
	}
	return nil
}

// DeleteBinding removes a grant.
func (s *Store) DeleteBinding(ctx context.Context, id int64) error {
	if _, err := s.db.ExecContext(ctx, "DELETE FROM bindings WHERE id = ?", id); err != nil {
		return fmt.Errorf("store: delete binding: %w", err)
	}
	return nil
}

var _ = sql.ErrNoRows
