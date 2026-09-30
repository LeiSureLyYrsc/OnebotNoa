package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/model"
)

const botColumns = `id, name, token_hash, enabled, rate_limit, action_policy, note, last_seen_at, created_at, updated_at`

func scanBot(row interface{ Scan(...any) error }) (model.Bot, error) {
	var (
		b                  model.Bot
		enabled            int
		rateLimit, policy  string
		lastSeen           sql.NullInt64
		created, updatedAt int64
	)
	if err := row.Scan(&b.ID, &b.Name, &b.TokenHash, &enabled, &rateLimit, &policy, &b.Note,
		&lastSeen, &created, &updatedAt); err != nil {
		return model.Bot{}, wrapNotFound(err)
	}
	b.Enabled = enabled == 1
	b.RateLimit = json.RawMessage(jsonOrEmpty([]byte(rateLimit), "{}"))
	b.ActionPolicy = json.RawMessage(jsonOrEmpty([]byte(policy), "{}"))
	if lastSeen.Valid {
		ts := unixToTime(lastSeen.Int64)
		b.LastSeenAt = &ts
	}
	b.CreatedAt = unixToTime(created)
	b.UpdatedAt = unixToTime(updatedAt)
	return b, nil
}

// BotByID loads one downstream application.
func (s *Store) BotByID(ctx context.Context, id int64) (model.Bot, error) {
	row := s.db.QueryRowContext(ctx, "SELECT "+botColumns+" FROM bots WHERE id = ?", id)
	return scanBot(row)
}

// BotByName loads one downstream application by name.
func (s *Store) BotByName(ctx context.Context, name string) (model.Bot, error) {
	row := s.db.QueryRowContext(ctx, "SELECT "+botColumns+" FROM bots WHERE name = ?", name)
	return scanBot(row)
}

// BotByTokenHash resolves the token a Bot presents.
func (s *Store) BotByTokenHash(ctx context.Context, tokenHash string) (model.Bot, error) {
	row := s.db.QueryRowContext(ctx, "SELECT "+botColumns+" FROM bots WHERE token_hash = ?", tokenHash)
	return scanBot(row)
}

// ListBots returns every downstream application.
func (s *Store) ListBots(ctx context.Context) ([]model.Bot, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+botColumns+" FROM bots ORDER BY name")
	if err != nil {
		return nil, fmt.Errorf("store: list bots: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := []model.Bot{}
	for rows.Next() {
		b, err := scanBot(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan bot: %w", err)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// CreateBot inserts a downstream application. tokenHash must be the hash of a
// generated token; the plaintext is never stored.
func (s *Store) CreateBot(ctx context.Context, name, tokenHash, note string) (model.Bot, error) {
	ts := now()
	res, err := s.db.ExecContext(ctx, `
INSERT INTO bots (name, token_hash, enabled, rate_limit, action_policy, note, created_at, updated_at)
VALUES (?, ?, 1, '{}', '{}', ?, ?, ?)`, name, tokenHash, note, ts, ts)
	if err != nil {
		return model.Bot{}, fmt.Errorf("store: create bot %q: %w", name, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return model.Bot{}, fmt.Errorf("store: create bot id: %w", err)
	}
	return s.BotByID(ctx, id)
}

// UpdateBot refreshes the mutable fields of a Bot.
func (s *Store) UpdateBot(ctx context.Context, b model.Bot) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE bots SET name = ?, enabled = ?, rate_limit = ?, action_policy = ?, note = ?, updated_at = ? WHERE id = ?`,
		b.Name, boolToInt(b.Enabled), jsonOrEmpty(b.RateLimit, "{}"), jsonOrEmpty(b.ActionPolicy, "{}"), b.Note, now(), b.ID)
	if err != nil {
		return fmt.Errorf("store: update bot: %w", err)
	}
	return nil
}

// SetBotToken stores a freshly generated token hash.
func (s *Store) SetBotToken(ctx context.Context, id int64, tokenHash string) error {
	if _, err := s.db.ExecContext(ctx,
		"UPDATE bots SET token_hash = ?, updated_at = ? WHERE id = ?", tokenHash, now(), id); err != nil {
		return fmt.Errorf("store: set bot token: %w", err)
	}
	return nil
}

// TouchBotSeen records the last time a Bot connected.
func (s *Store) TouchBotSeen(ctx context.Context, id int64, at time.Time) error {
	if _, err := s.db.ExecContext(ctx, "UPDATE bots SET last_seen_at = ? WHERE id = ?", timeToUnix(at), id); err != nil {
		return fmt.Errorf("store: touch bot: %w", err)
	}
	return nil
}

// DeleteBot removes a Bot (bindings cascade).
func (s *Store) DeleteBot(ctx context.Context, id int64) error {
	if _, err := s.db.ExecContext(ctx, "DELETE FROM bots WHERE id = ?", id); err != nil {
		return fmt.Errorf("store: delete bot: %w", err)
	}
	return nil
}
