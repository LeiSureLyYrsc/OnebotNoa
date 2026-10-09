package store

import (
	"database/sql"
	"errors"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/model"
)

const bindingCols = `id, bot_id, account_id, priority, is_default, enabled, scope`

func scanBinding(row interface{ Scan(...any) error }) (model.Binding, error) {
	var b model.Binding
	err := row.Scan(&b.ID, &b.BotID, &b.AccountID, &b.Priority, &b.IsDefault, &b.Enabled, &b.Scope)
	return b, err
}

// CreateBinding inserts a bot<->account assignment.
func (s *Store) CreateBinding(botID, accountID int64, priority int, isDefault bool, scope string) (model.Binding, error) {
	if scope == "" {
		scope = "{}"
	}
	res, err := s.db.Exec(`INSERT INTO bindings (bot_id, account_id, priority, is_default, enabled, scope)
		VALUES (?, ?, ?, ?, 1, ?)`, botID, accountID, priority, isDefault, scope)
	if err != nil {
		return model.Binding{}, err
	}
	id, _ := res.LastInsertId()
	return s.GetBinding(id)
}

// GetBinding loads one binding by id.
func (s *Store) GetBinding(id int64) (model.Binding, error) {
	row := s.db.QueryRow(`SELECT `+bindingCols+` FROM bindings WHERE id = ?`, id)
	b, err := scanBinding(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Binding{}, ErrNotFound
	}
	return b, err
}

// ListBindings returns every binding.
func (s *Store) ListBindings() ([]model.Binding, error) {
	return s.queryBindings(`SELECT ` + bindingCols + ` FROM bindings ORDER BY bot_id, priority`)
}

// ListBindingsByAccount returns bindings for one account.
func (s *Store) ListBindingsByAccount(accountID int64) ([]model.Binding, error) {
	return s.queryBindings(`SELECT `+bindingCols+` FROM bindings WHERE account_id = ? ORDER BY priority`, accountID)
}

// ListBindingsByBot returns bindings for one bot.
func (s *Store) ListBindingsByBot(botID int64) ([]model.Binding, error) {
	return s.queryBindings(`SELECT `+bindingCols+` FROM bindings WHERE bot_id = ? ORDER BY priority`, botID)
}

func (s *Store) queryBindings(query string, args ...any) ([]model.Binding, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Binding
	for rows.Next() {
		b, err := scanBinding(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// UpdateBinding updates mutable fields.
func (s *Store) UpdateBinding(id int64, priority int, isDefault, enabled bool, scope string) error {
	if scope == "" {
		scope = "{}"
	}
	_, err := s.db.Exec(`UPDATE bindings SET priority = ?, is_default = ?, enabled = ?, scope = ? WHERE id = ?`,
		priority, isDefault, enabled, scope, id)
	return err
}

// DeleteBinding removes one binding.
func (s *Store) DeleteBinding(id int64) error {
	_, err := s.db.Exec(`DELETE FROM bindings WHERE id = ?`, id)
	return err
}
