package store

import (
	"database/sql"
	"errors"
	"strings"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/model"
)

// ErrNotFound is returned when a requested entity does not exist.
var ErrNotFound = errors.New("not found")

const accountCols = `id, self_id, name, nickname, token_hash, enabled, status, created_at, last_seen_at`

func scanAccount(row interface{ Scan(...any) error }) (model.Account, error) {
	var a model.Account
	err := row.Scan(&a.ID, &a.SelfID, &a.Name, &a.Nickname, &a.TokenHash, &a.Enabled, &a.Status, &a.CreatedAt, &a.LastSeenAt)
	return a, err
}

// SetAccountTokenHash stores a rotated account token hash.
func (s *Store) SetAccountTokenHash(id int64, hash string) error {
	_, err := s.db.Exec(`UPDATE accounts SET token_hash = ? WHERE id = ?`, hash, id)
	return err
}

// GetAccountByTokenHash resolves an account from a data-plane token hash.
func (s *Store) GetAccountByTokenHash(hash string) (model.Account, error) {
	row := s.db.QueryRow(`SELECT `+accountCols+` FROM accounts WHERE token_hash = ?`, hash)
	a, err := scanAccount(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Account{}, ErrNotFound
	}
	return a, err
}

// CreateAccount inserts a new QQ account.
func (s *Store) CreateAccount(selfID, name string) (model.Account, error) {
	selfID = strings.TrimSpace(selfID)
	res, err := s.db.Exec(`INSERT INTO accounts (self_id, name, enabled, status, created_at, last_seen_at)
		VALUES (?, ?, 1, 'offline', ?, 0)`, selfID, name, now())
	if err != nil {
		return model.Account{}, err
	}
	id, _ := res.LastInsertId()
	return s.GetAccount(id)
}

// UpsertAccountBySelfID returns the account for selfID, creating it when absent.
func (s *Store) UpsertAccountBySelfID(selfID string) (model.Account, error) {
	a, err := s.GetAccountBySelfID(selfID)
	if err == nil {
		return a, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return model.Account{}, err
	}
	return s.CreateAccount(selfID, "")
}

// GetAccount loads one account by id.
func (s *Store) GetAccount(id int64) (model.Account, error) {
	row := s.db.QueryRow(`SELECT `+accountCols+` FROM accounts WHERE id = ?`, id)
	a, err := scanAccount(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Account{}, ErrNotFound
	}
	return a, err
}

// GetAccountBySelfID loads one account by its QQ number.
func (s *Store) GetAccountBySelfID(selfID string) (model.Account, error) {
	row := s.db.QueryRow(`SELECT `+accountCols+` FROM accounts WHERE self_id = ?`, selfID)
	a, err := scanAccount(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Account{}, ErrNotFound
	}
	return a, err
}

// ListAccounts returns all accounts ordered by id.
func (s *Store) ListAccounts() ([]model.Account, error) {
	rows, err := s.db.Query(`SELECT ` + accountCols + ` FROM accounts ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Account
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// UpdateAccount updates mutable fields.
func (s *Store) UpdateAccount(id int64, name string, enabled bool) error {
	_, err := s.db.Exec(`UPDATE accounts SET name = ?, enabled = ? WHERE id = ?`, name, enabled, id)
	return err
}

// SetAccountStatus records liveness metadata reported by the data plane.
func (s *Store) SetAccountStatus(selfID, status, nickname string, lastSeen int64) error {
	_, err := s.db.Exec(`UPDATE accounts SET status = ?, nickname = CASE WHEN ? <> '' THEN ? ELSE nickname END, last_seen_at = ? WHERE self_id = ?`,
		status, nickname, nickname, lastSeen, selfID)
	return err
}

// DeleteAccount removes an account and its bindings.
func (s *Store) DeleteAccount(id int64) error {
	if _, err := s.db.Exec(`DELETE FROM bindings WHERE account_id = ?`, id); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM accounts WHERE id = ?`, id)
	return err
}
