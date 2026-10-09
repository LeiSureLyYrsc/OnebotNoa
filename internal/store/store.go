// Package store persists management-plane state (accounts, bots, bindings,
// listeners, users, sessions, audit) in an embedded SQLite database.
package store

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no cgo)
)

// Store wraps the SQLite database handle.
type Store struct {
	db *sql.DB
}

// Open opens (creating when needed) the SQLite database at path and runs
// migrations.
func Open(path string) (*Store, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// SQLite allows a single writer; serialising the pool avoids lock errors.
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

// DB exposes the raw handle for tests.
func (s *Store) DB() *sql.DB { return s.db }

func (s *Store) migrate() error {
	for _, stmt := range migrations {
		if _, err := s.db.Exec(stmt); err != nil {
			return fmt.Errorf("migration failed: %w\n%s", err, stmt)
		}
	}
	return nil
}

func now() int64 { return time.Now().Unix() }

var migrations = []string{
	`CREATE TABLE IF NOT EXISTS accounts (
		id           INTEGER PRIMARY KEY AUTOINCREMENT,
		self_id      TEXT    NOT NULL UNIQUE,
		name         TEXT    NOT NULL DEFAULT '',
		nickname     TEXT    NOT NULL DEFAULT '',
		token_hash   TEXT    NOT NULL DEFAULT '',
		enabled      INTEGER NOT NULL DEFAULT 1,
		status       TEXT    NOT NULL DEFAULT 'offline',
		created_at   INTEGER NOT NULL DEFAULT 0,
		last_seen_at INTEGER NOT NULL DEFAULT 0
	)`,
	`CREATE TABLE IF NOT EXISTS bots (
		id            INTEGER PRIMARY KEY AUTOINCREMENT,
		name          TEXT    NOT NULL UNIQUE,
		token_hash    TEXT    NOT NULL DEFAULT '',
		enabled       INTEGER NOT NULL DEFAULT 1,
		rate_limit    TEXT    NOT NULL DEFAULT '{}',
		action_policy TEXT    NOT NULL DEFAULT '{}',
		note          TEXT    NOT NULL DEFAULT '',
		created_at    INTEGER NOT NULL DEFAULT 0
	)`,
	`CREATE TABLE IF NOT EXISTS bindings (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		bot_id     INTEGER NOT NULL,
		account_id INTEGER NOT NULL,
		priority   INTEGER NOT NULL DEFAULT 100,
		is_default INTEGER NOT NULL DEFAULT 0,
		enabled    INTEGER NOT NULL DEFAULT 1,
		scope      TEXT    NOT NULL DEFAULT '{}',
		UNIQUE (bot_id, account_id)
	)`,
	`CREATE TABLE IF NOT EXISTS listeners (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		name       TEXT    NOT NULL,
		role       TEXT    NOT NULL,
		bind_addr  TEXT    NOT NULL,
		path       TEXT    NOT NULL,
		account_id INTEGER,
		bot_id     INTEGER,
		enabled    INTEGER NOT NULL DEFAULT 1
	)`,
	`CREATE TABLE IF NOT EXISTS users (
		id            INTEGER PRIMARY KEY AUTOINCREMENT,
		username      TEXT    NOT NULL UNIQUE,
		password_hash TEXT    NOT NULL,
		role          TEXT    NOT NULL DEFAULT 'admin',
		created_at    INTEGER NOT NULL DEFAULT 0
	)`,
	`CREATE TABLE IF NOT EXISTS sessions (
		token      TEXT    PRIMARY KEY,
		user_id    INTEGER NOT NULL,
		expires_at INTEGER NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS audit_log (
		id     INTEGER PRIMARY KEY AUTOINCREMENT,
		at     INTEGER NOT NULL,
		actor  TEXT    NOT NULL DEFAULT '',
		action TEXT    NOT NULL DEFAULT '',
		target TEXT    NOT NULL DEFAULT '',
		detail TEXT    NOT NULL DEFAULT '',
		ip     TEXT    NOT NULL DEFAULT ''
	)`,
}
