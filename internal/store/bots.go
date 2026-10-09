package store

import (
	"database/sql"
	"errors"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/model"
)

const botCols = `id, name, token_hash, enabled, rate_limit, action_policy, note, created_at`

func scanBot(row interface{ Scan(...any) error }) (model.Bot, error) {
	var b model.Bot
	err := row.Scan(&b.ID, &b.Name, &b.TokenHash, &b.Enabled, &b.RateLimit, &b.ActionPolicy, &b.Note, &b.CreatedAt)
	return b, err
}

// CreateBot inserts a downstream bot client.
func (s *Store) CreateBot(name, tokenHash string) (model.Bot, error) {
	res, err := s.db.Exec(`INSERT INTO bots (name, token_hash, enabled, rate_limit, action_policy, note, created_at)
		VALUES (?, ?, 1, '{}', '{}', '', ?)`, name, tokenHash, now())
	if err != nil {
		return model.Bot{}, err
	}
	id, _ := res.LastInsertId()
	return s.GetBot(id)
}

// GetBot loads one bot by id.
func (s *Store) GetBot(id int64) (model.Bot, error) {
	row := s.db.QueryRow(`SELECT `+botCols+` FROM bots WHERE id = ?`, id)
	b, err := scanBot(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Bot{}, ErrNotFound
	}
	return b, err
}

// GetBotByName loads one bot by unique name.
func (s *Store) GetBotByName(name string) (model.Bot, error) {
	row := s.db.QueryRow(`SELECT `+botCols+` FROM bots WHERE name = ?`, name)
	b, err := scanBot(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Bot{}, ErrNotFound
	}
	return b, err
}

// ListBots returns all bots ordered by id.
func (s *Store) ListBots() ([]model.Bot, error) {
	rows, err := s.db.Query(`SELECT ` + botCols + ` FROM bots ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Bot
	for rows.Next() {
		b, err := scanBot(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// UpdateBot updates mutable fields.
func (s *Store) UpdateBot(id int64, name string, enabled bool, note, rateLimit, actionPolicy string) error {
	_, err := s.db.Exec(`UPDATE bots SET name = ?, enabled = ?, note = ?, rate_limit = ?, action_policy = ? WHERE id = ?`,
		name, enabled, note, rateLimit, actionPolicy, id)
	return err
}

// SetBotTokenHash rotates a bot token hash.
func (s *Store) SetBotTokenHash(id int64, hash string) error {
	_, err := s.db.Exec(`UPDATE bots SET token_hash = ? WHERE id = ?`, hash, id)
	return err
}

// GetBotByTokenHash resolves a bot from a data-plane token hash.
func (s *Store) GetBotByTokenHash(hash string) (model.Bot, error) {
	row := s.db.QueryRow(`SELECT `+botCols+` FROM bots WHERE token_hash = ?`, hash)
	b, err := scanBot(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Bot{}, ErrNotFound
	}
	return b, err
}

// DeleteBot removes a bot and its bindings.
func (s *Store) DeleteBot(id int64) error {
	if _, err := s.db.Exec(`DELETE FROM bindings WHERE bot_id = ?`, id); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM bots WHERE id = ?`, id)
	return err
}
