package store

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
)

// LegacyConnectionGraph is the accounts/Bots/bindings/listeners/endpoints set as
// it existed inside SQLite before the split into connect.json.
//
// It exists only so an upgrade does not lose an operator's setup: the first time
// a build with connect.json starts against an older database, this is read, the
// file is written, and the tables are dropped by migration 0003. Afterwards
// these helpers are dead weight and can be removed.
type LegacyConnectionGraph struct {
	Accounts  []LegacyAccount
	Bots      []LegacyBot
	Bindings  []LegacyBinding
	Listeners []LegacyListener
	Endpoints []LegacyEndpoint
}

// LegacyAccount is one row of the old accounts table.
type LegacyAccount struct {
	ID        int64
	SelfID    string
	Name      string
	Nickname  string
	Avatar    string
	Enabled   bool
	Tags      string
	TokenHash string
	Source    string
}

// LegacyBot is one row of the old bots table.
type LegacyBot struct {
	ID           int64
	Name         string
	Enabled      bool
	Note         string
	RateLimit    string
	ActionPolicy string
	TokenHash    string
}

// LegacyBinding is one row of the old bindings table.
type LegacyBinding struct {
	BotID     int64
	AccountID int64
	Priority  int
	IsDefault bool
	Enabled   bool
	Scope     string
}

// LegacyListener is one row of the old listeners table.
type LegacyListener struct {
	Name          string
	Kind          string
	BindAddr      string
	Path          string
	AccountSelfID string
	BotName       string
	FixedSelfID   string
	TLSCert       string
	TLSKey        string
	Enabled       bool
}

// LegacyEndpoint is one row of the old endpoints table.
type LegacyEndpoint struct {
	Name        string
	Kind        string
	URL         string
	Mode        string
	AccountHint string
	BotName     string
	Token       string
	Reconnect   string
	Enabled     bool
}

// HasLegacyConnections reports whether the old tables still exist and hold data.
func (s *Store) HasLegacyConnections(ctx context.Context) bool {
	if !s.tableExists(ctx, "accounts") {
		return false
	}
	var n int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM accounts").Scan(&n); err != nil {
		return false
	}
	return n > 0
}

func (s *Store) tableExists(ctx context.Context, name string) bool {
	var found string
	err := s.db.QueryRowContext(ctx,
		"SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?", name).Scan(&found)
	return err == nil && found == name
}

// LegacyGraph reads the old connection tables. A missing table yields an empty
// slice rather than an error, so a half-migrated database still loads.
func (s *Store) LegacyGraph(ctx context.Context) (LegacyConnectionGraph, error) {
	var graph LegacyConnectionGraph
	if !s.tableExists(ctx, "accounts") {
		return graph, nil
	}

	accounts, err := s.db.QueryContext(ctx,
		"SELECT id, self_id, name, nickname, avatar, enabled, tags, COALESCE(token_hash, ''), source FROM accounts ORDER BY id")
	if err != nil {
		return graph, err
	}
	for accounts.Next() {
		var row LegacyAccount
		var enabled int
		if err := accounts.Scan(&row.ID, &row.SelfID, &row.Name, &row.Nickname, &row.Avatar,
			&enabled, &row.Tags, &row.TokenHash, &row.Source); err != nil {
			_ = accounts.Close()
			return graph, err
		}
		row.Enabled = enabled == 1
		graph.Accounts = append(graph.Accounts, row)
	}
	_ = accounts.Close()

	bots, err := s.db.QueryContext(ctx,
		"SELECT id, name, enabled, note, rate_limit, action_policy, token_hash FROM bots ORDER BY id")
	if err != nil {
		return graph, err
	}
	for bots.Next() {
		var row LegacyBot
		var enabled int
		if err := bots.Scan(&row.ID, &row.Name, &enabled, &row.Note, &row.RateLimit, &row.ActionPolicy,
			&row.TokenHash); err != nil {
			_ = bots.Close()
			return graph, err
		}
		row.Enabled = enabled == 1
		graph.Bots = append(graph.Bots, row)
	}
	_ = bots.Close()

	bindings, err := s.db.QueryContext(ctx,
		"SELECT bot_id, account_id, priority, is_default, enabled, scope FROM bindings ORDER BY id")
	if err != nil {
		return graph, err
	}
	for bindings.Next() {
		var row LegacyBinding
		var isDefault, enabled int
		if err := bindings.Scan(&row.BotID, &row.AccountID, &row.Priority, &isDefault, &enabled, &row.Scope); err != nil {
			_ = bindings.Close()
			return graph, err
		}
		row.IsDefault = isDefault == 1
		row.Enabled = enabled == 1
		graph.Bindings = append(graph.Bindings, row)
	}
	_ = bindings.Close()

	if s.tableExists(ctx, "listeners") {
		rows, err := s.db.QueryContext(ctx, `
			SELECT l.name, l.kind, l.bind_addr, l.path, COALESCE(a.self_id, ''), COALESCE(b.name, ''),
			       l.fixed_self_id, l.tls_cert, l.tls_key, l.enabled
			FROM listeners l
			LEFT JOIN accounts a ON a.id = l.account_id
			LEFT JOIN bots b ON b.id = l.bot_id ORDER BY l.id`)
		if err != nil {
			return graph, err
		}
		for rows.Next() {
			var row LegacyListener
			var enabled int
			if err := rows.Scan(&row.Name, &row.Kind, &row.BindAddr, &row.Path, &row.AccountSelfID,
				&row.BotName, &row.FixedSelfID, &row.TLSCert, &row.TLSKey, &enabled); err != nil {
				_ = rows.Close()
				return graph, err
			}
			row.Enabled = enabled == 1
			graph.Listeners = append(graph.Listeners, row)
		}
		_ = rows.Close()
	}

	if s.tableExists(ctx, "endpoints") {
		rows, err := s.db.QueryContext(ctx, `
			SELECT e.name, e.kind, e.url, e.mode, e.account_hint, COALESCE(b.name, ''),
			       COALESCE(e.token, ''), e.reconnect, e.enabled
			FROM endpoints e LEFT JOIN bots b ON b.id = e.bot_id ORDER BY e.id`)
		if err != nil {
			return graph, err
		}
		for rows.Next() {
			var row LegacyEndpoint
			var enabled int
			if err := rows.Scan(&row.Name, &row.Kind, &row.URL, &row.Mode, &row.AccountHint,
				&row.BotName, &row.Token, &row.Reconnect, &enabled); err != nil {
				_ = rows.Close()
				return graph, err
			}
			row.Enabled = enabled == 1
			graph.Endpoints = append(graph.Endpoints, row)
		}
		_ = rows.Close()
	}

	return graph, nil
}

// DropLegacyConnections removes the old tables. Migration 0003 normally does
// this; the helper exists so a caller can finish the job explicitly.
func (s *Store) DropLegacyConnections(ctx context.Context) error {
	for _, table := range []string{"bindings", "listeners", "endpoints", "bots", "accounts"} {
		if _, err := s.db.ExecContext(ctx, "DROP TABLE IF EXISTS "+table); err != nil {
			return err
		}
	}
	return nil
}

var _ = errors.Is
var _ = sql.ErrNoRows
var _ = slog.Info
