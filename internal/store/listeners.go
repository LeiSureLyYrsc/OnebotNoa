package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/model"
)

const listenerColumns = `id, name, kind, bind_addr, path, account_id, bot_id, fixed_self_id, tls_cert, tls_key, enabled, created_at, updated_at`

func scanListener(row interface{ Scan(...any) error }) (model.Listener, error) {
	var (
		l                  model.Listener
		accountID, botID   sql.NullInt64
		enabled            int
		created, updatedAt int64
	)
	if err := row.Scan(&l.ID, &l.Name, &l.Kind, &l.BindAddr, &l.Path, &accountID, &botID, &l.FixedSelfID,
		&l.TLSCert, &l.TLSKey, &enabled, &created, &updatedAt); err != nil {
		return model.Listener{}, wrapNotFound(err)
	}
	if accountID.Valid {
		v := accountID.Int64
		l.AccountID = &v
	}
	if botID.Valid {
		v := botID.Int64
		l.BotID = &v
	}
	l.Enabled = enabled == 1
	l.CreatedAt = unixToTime(created)
	l.UpdatedAt = unixToTime(updatedAt)
	return l, nil
}

// ListListeners returns every dedicated listener.
func (s *Store) ListListeners(ctx context.Context) ([]model.Listener, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+listenerColumns+" FROM listeners ORDER BY bind_addr, path")
	if err != nil {
		return nil, fmt.Errorf("store: list listeners: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := []model.Listener{}
	for rows.Next() {
		l, err := scanListener(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan listener: %w", err)
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// ListenerByID loads one dedicated listener.
func (s *Store) ListenerByID(ctx context.Context, id int64) (model.Listener, error) {
	row := s.db.QueryRowContext(ctx, "SELECT "+listenerColumns+" FROM listeners WHERE id = ?", id)
	return scanListener(row)
}

// CreateListener inserts a dedicated listener.
func (s *Store) CreateListener(ctx context.Context, l model.Listener) (model.Listener, error) {
	ts := now()
	res, err := s.db.ExecContext(ctx, `
INSERT INTO listeners (name, kind, bind_addr, path, account_id, bot_id, fixed_self_id, tls_cert, tls_key, enabled, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		l.Name, l.Kind, l.BindAddr, l.Path, l.AccountID, l.BotID, l.FixedSelfID, l.TLSCert, l.TLSKey,
		boolToInt(l.Enabled), ts, ts)
	if err != nil {
		return model.Listener{}, fmt.Errorf("store: create listener: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return model.Listener{}, fmt.Errorf("store: create listener id: %w", err)
	}
	return s.ListenerByID(ctx, id)
}

// UpdateListener refreshes a dedicated listener.
func (s *Store) UpdateListener(ctx context.Context, l model.Listener) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE listeners SET name = ?, kind = ?, bind_addr = ?, path = ?, account_id = ?, bot_id = ?,
       fixed_self_id = ?, tls_cert = ?, tls_key = ?, enabled = ?, updated_at = ?
WHERE id = ?`,
		l.Name, l.Kind, l.BindAddr, l.Path, l.AccountID, l.BotID, l.FixedSelfID, l.TLSCert, l.TLSKey,
		boolToInt(l.Enabled), now(), l.ID)
	if err != nil {
		return fmt.Errorf("store: update listener: %w", err)
	}
	return nil
}

// DeleteListener removes a dedicated listener.
func (s *Store) DeleteListener(ctx context.Context, id int64) error {
	if _, err := s.db.ExecContext(ctx, "DELETE FROM listeners WHERE id = ?", id); err != nil {
		return fmt.Errorf("store: delete listener: %w", err)
	}
	return nil
}

var _ = json.RawMessage(nil)
