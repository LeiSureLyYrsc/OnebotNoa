package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/model"
)

const endpointColumns = `id, name, kind, url, mode, account_hint, bot_id, reconnect, enabled, token, created_at, updated_at`

func scanEndpoint(row interface{ Scan(...any) error }) (model.Endpoint, error) {
	var (
		e                  model.Endpoint
		botID              sql.NullInt64
		reconnect          string
		enabled            int
		created, updatedAt int64
	)
	if err := row.Scan(&e.ID, &e.Name, &e.Kind, &e.URL, &e.Mode, &e.AccountHint, &botID, &reconnect,
		&enabled, &e.Token, &created, &updatedAt); err != nil {
		return model.Endpoint{}, wrapNotFound(err)
	}
	if botID.Valid {
		v := botID.Int64
		e.BotID = &v
	}
	e.Reconnect = json.RawMessage(jsonOrEmpty([]byte(reconnect), "{}"))
	e.Enabled = enabled == 1
	e.HasToken = e.Token != ""
	e.CreatedAt = unixToTime(created)
	e.UpdatedAt = unixToTime(updatedAt)
	return e, nil
}

// ListEndpoints returns every relay-dialed connection definition.
func (s *Store) ListEndpoints(ctx context.Context) ([]model.Endpoint, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+endpointColumns+" FROM endpoints ORDER BY name")
	if err != nil {
		return nil, fmt.Errorf("store: list endpoints: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := []model.Endpoint{}
	for rows.Next() {
		e, err := scanEndpoint(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan endpoint: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// EndpointByID loads one endpoint.
func (s *Store) EndpointByID(ctx context.Context, id int64) (model.Endpoint, error) {
	row := s.db.QueryRowContext(ctx, "SELECT "+endpointColumns+" FROM endpoints WHERE id = ?", id)
	return scanEndpoint(row)
}

// EndpointByName loads one endpoint by its unique display name.
func (s *Store) EndpointByName(ctx context.Context, name string) (model.Endpoint, error) {
	row := s.db.QueryRowContext(ctx, "SELECT "+endpointColumns+" FROM endpoints WHERE name = ?", name)
	return scanEndpoint(row)
}

// CreateEndpoint inserts a dial target.
func (s *Store) CreateEndpoint(ctx context.Context, e model.Endpoint) (model.Endpoint, error) {
	ts := now()
	res, err := s.db.ExecContext(ctx, `
INSERT INTO endpoints (name, kind, url, mode, account_hint, bot_id, reconnect, enabled, token, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.Name, e.Kind, e.URL, e.Mode, e.AccountHint, e.BotID,
		jsonOrEmpty(e.Reconnect, "{}"), boolToInt(e.Enabled), e.Token, ts, ts)
	if err != nil {
		return model.Endpoint{}, fmt.Errorf("store: create endpoint: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return model.Endpoint{}, fmt.Errorf("store: create endpoint id: %w", err)
	}
	return s.EndpointByID(ctx, id)
}

// UpdateEndpoint refreshes a dial target.
func (s *Store) UpdateEndpoint(ctx context.Context, e model.Endpoint) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE endpoints SET name = ?, kind = ?, url = ?, mode = ?, account_hint = ?, bot_id = ?,
       reconnect = ?, enabled = ?, token = ?, updated_at = ?
WHERE id = ?`,
		e.Name, e.Kind, e.URL, e.Mode, e.AccountHint, e.BotID,
		jsonOrEmpty(e.Reconnect, "{}"), boolToInt(e.Enabled), e.Token, now(), e.ID)
	if err != nil {
		return fmt.Errorf("store: update endpoint: %w", err)
	}
	return nil
}

// DeleteEndpoint removes a dial target.
func (s *Store) DeleteEndpoint(ctx context.Context, id int64) error {
	if _, err := s.db.ExecContext(ctx, "DELETE FROM endpoints WHERE id = ?", id); err != nil {
		return fmt.Errorf("store: delete endpoint: %w", err)
	}
	return nil
}
