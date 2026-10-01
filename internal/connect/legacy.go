package connect

import (
	"encoding/json"
	"strings"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/cryptobox"
)

// SeedFromLegacy fills an empty document from the connection tables an older
// build kept in SQLite.
//
// This runs exactly once per deployment, on the first start after the upgrade,
// and only when connect.json has no content yet. Without it the schema migration
// that drops the old tables would silently discard an operator's accounts, Bots
// and connections.
//
// The input is the raw SQL rows; the caller (store.LegacyGraph) reads them so
// this package never opens the database.
type LegacyRow struct {
	// Accounts, Bots, Bindings, Listeners and Endpoints are JSON-encoded legacy
	// rows; keeping them opaque avoids a dependency on the store package.
	Accounts  []LegacyAccount
	Bots      []LegacyBot
	Bindings  []LegacyBinding
	Listeners []LegacyConnection
	Endpoints []LegacyConnection
}

// LegacyAccount mirrors the columns the old accounts table exposed.
type LegacyAccount struct {
	SelfID    string
	Name      string
	Nickname  string
	Avatar    string
	Enabled   bool
	Tags      []string
	TokenHash string
	Token     string
	Source    string
}

// LegacyBot mirrors the old bots table.
type LegacyBot struct {
	Name         string
	Enabled      bool
	Note         string
	RateLimit    json.RawMessage
	ActionPolicy json.RawMessage
	TokenHash    string
	Token        string
}

// LegacyBinding mirrors the old bindings table after name resolution.
type LegacyBinding struct {
	BotName       string
	AccountSelfID string
	Priority      int
	IsDefault     bool
	Enabled       bool
	Scope         Scope
}

// LegacyConnection is either an old listener row or an old endpoint row, already
// normalised onto the connect.json connection shape.
type LegacyConnection struct {
	Name          string
	Kind          string
	Addr          string
	URL           string
	Path          string
	Mode          string
	AccountSelfID string
	BotName       string
	FixedSelfID   string
	TLSCert       string
	TLSKey        string
	Token         string
	Min           string
	Max           string
	Jitter        float64
	Enabled       bool
}

// SeedFromLegacy writes the legacy rows into the document when it is still empty.
// It reports how many entities were imported.
func (s *Store) SeedFromLegacy(legacy LegacyRow) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.doc.Accounts) > 0 || len(s.doc.Bots) > 0 || len(s.doc.Connections) > 0 || len(s.doc.Bindings) > 0 {
		return 0, nil
	}
	imported := 0

	for _, row := range legacy.Accounts {
		if strings.TrimSpace(row.SelfID) == "" {
			continue
		}
		account := Account{
			ID: s.nextAccountIDLocked(), SelfID: row.SelfID, Name: row.Name,
			Nickname: row.Nickname, Avatar: row.Avatar, Enabled: row.Enabled,
			Tags: append([]string(nil), row.Tags...), Source: row.Source,
		}
		if row.TokenHash != "" || row.Token != "" {
			sealed, err := s.sealLocked(row.Token)
			if err != nil {
				return imported, err
			}
			account.Grant = Grant{
				Token: sealed, TokenHash: row.TokenHash,
				Sealed: row.Token != "", TokenHint: hintOf(row.Token),
			}
		}
		s.doc.Accounts = append(s.doc.Accounts, account)
		imported++
	}

	for _, row := range legacy.Bots {
		if strings.TrimSpace(row.Name) == "" {
			continue
		}
		sealed, err := s.sealLocked(row.Token)
		if err != nil {
			return imported, err
		}
		bot := Bot{
			ID: s.nextBotIDLocked(), Name: row.Name, Enabled: row.Enabled, Note: row.Note,
			RateLimit: row.RateLimit, ActionPolicy: row.ActionPolicy,
			Grant: Grant{Token: sealed, TokenHash: row.TokenHash, Sealed: row.Token != "", TokenHint: hintOf(row.Token)},
		}
		s.doc.Bots = append(s.doc.Bots, bot)
		imported++
	}

	for _, row := range legacy.Bindings {
		binding := Binding{
			ID: s.nextBindingIDLocked(), BotName: row.BotName, AccountSelfID: row.AccountSelfID,
			Priority: row.Priority, IsDefault: row.IsDefault, Enabled: row.Enabled, Scope: row.Scope,
		}
		if binding.Priority == 0 {
			binding.Priority = 100
		}
		s.doc.Bindings = append(s.doc.Bindings, binding)
		imported++
	}

	for _, row := range legacy.Listeners {
		connection := Connection{
			ID: s.nextConnectionIDLocked(), Name: row.Name, Kind: row.Kind,
			Addr: row.Addr, Path: row.Path, AccountSelfID: row.AccountSelfID,
			BotName: row.BotName, FixedSelfID: row.FixedSelfID,
			TLSCert: row.TLSCert, TLSKey: row.TLSKey, Enabled: row.Enabled,
		}
		s.doc.Connections = append(s.doc.Connections, connection)
		imported++
	}

	for _, row := range legacy.Endpoints {
		sealed, err := s.sealLocked(row.Token)
		if err != nil {
			return imported, err
		}
		connection := Connection{
			ID: s.nextConnectionIDLocked(), Name: row.Name, Kind: row.Kind,
			URL: row.URL, Mode: defaultMode(row.Mode), AccountSelfID: row.AccountSelfID,
			BotName: row.BotName, Enabled: row.Enabled,
			Grant: Grant{Token: sealed, Sealed: row.Token != "", TokenHint: hintOf(row.Token)},
		}
		if row.Min != "" || row.Max != "" || row.Jitter != 0 {
			connection.Reconnect = &Reconnect{Min: row.Min, Max: row.Max, Jitter: row.Jitter}
		}
		s.doc.Connections = append(s.doc.Connections, connection)
		imported++
	}

	if imported == 0 {
		return 0, nil
	}
	if err := s.saveLocked(); err != nil {
		return imported, err
	}
	return imported, nil
}

func defaultMode(mode string) string {
	if mode == "" {
		return "universal"
	}
	return mode
}

var _ = cryptobox.IsSealed
