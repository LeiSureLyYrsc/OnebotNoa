package store

import (
	"database/sql"
	"errors"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/model"
)

const listenerCols = `id, name, role, bind_addr, path, account_id, bot_id, enabled`

func scanListener(row interface{ Scan(...any) error }) (model.Listener, error) {
	var l model.Listener
	var accountID, botID sql.NullInt64
	if err := row.Scan(&l.ID, &l.Name, &l.Role, &l.BindAddr, &l.Path, &accountID, &botID, &l.Enabled); err != nil {
		return l, err
	}
	if accountID.Valid {
		l.AccountID = &accountID.Int64
	}
	if botID.Valid {
		l.BotID = &botID.Int64
	}
	return l, nil
}

// CreateListener inserts a dedicated listen endpoint.
func (s *Store) CreateListener(l model.Listener) (model.Listener, error) {
	accountID := nullableInt(l.AccountID)
	botID := nullableInt(l.BotID)
	res, err := s.db.Exec(`INSERT INTO listeners (name, role, bind_addr, path, account_id, bot_id, enabled)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, l.Name, l.Role, l.BindAddr, l.Path, accountID, botID, l.Enabled)
	if err != nil {
		return model.Listener{}, err
	}
	id, _ := res.LastInsertId()
	return s.GetListener(id)
}

// GetListener loads one listener by id.
func (s *Store) GetListener(id int64) (model.Listener, error) {
	row := s.db.QueryRow(`SELECT `+listenerCols+` FROM listeners WHERE id = ?`, id)
	l, err := scanListener(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Listener{}, ErrNotFound
	}
	return l, err
}

// ListListeners returns all listeners ordered by id.
func (s *Store) ListListeners() ([]model.Listener, error) {
	rows, err := s.db.Query(`SELECT ` + listenerCols + ` FROM listeners ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Listener
	for rows.Next() {
		l, err := scanListener(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// UpdateListener updates a listener's mutable fields.
func (s *Store) UpdateListener(l model.Listener) error {
	_, err := s.db.Exec(`UPDATE listeners SET name = ?, role = ?, bind_addr = ?, path = ?, account_id = ?, bot_id = ?, enabled = ? WHERE id = ?`,
		l.Name, l.Role, l.BindAddr, l.Path, nullableInt(l.AccountID), nullableInt(l.BotID), l.Enabled, l.ID)
	return err
}

// DeleteListener removes a listener.
func (s *Store) DeleteListener(id int64) error {
	_, err := s.db.Exec(`DELETE FROM listeners WHERE id = ?`, id)
	return err
}

func nullableInt(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}
