package hub

import (
	"encoding/json"
	"fmt"
)

// ConnStore is the connection-side persistence the hub reads while it runs:
// QQ instances, Bots, connections and bindings. It is connect.json, not the
// SQLite file, so the data plane keeps working with a damaged management
// database and the connection set stays a reviewable document.
//
// It is an interface for one reason: the hub tests substitute an in-memory
// double, and a stricter contract ("what the data plane actually needs") is
// easier to keep honest than a concrete file handle.
type ConnStore interface {
	AccountBySelfID(selfID string) (Account, bool)
	Accounts() []Account

	BotByID(id int64) (Bot, bool)
	BotByName(name string) (Bot, bool)
	Bots() []Bot
	TouchBotSeen(id int64) error
	SetBotToken(id int64, plain, tokenHash string) error

	Bindings() []JBinding
	BindingsByBot(botName string) []JBinding
	BindingsByAccount(selfID string) []JBinding
	BindingByPair(botName, selfID string) (JBinding, bool)
	ApplyBinding(BindingRef) error

	PersistAccountState(selfID string, status string) error
	EnsureAccount(selfID, source string) (Account, bool, error)
	SetAccountToken(id int64, plain, tokenHash, hint string) error
	AccountToken(id int64) (string, error)
}

// Account is the hub's view of one QQ instance.
type Account struct {
	ID       int64
	SelfID   string
	Name     string
	Nickname string
	Avatar   string
	Enabled  bool
	Tags     []string
	Status   string
}

// Bot is the hub's view of one downstream application.
type Bot struct {
	ID           int64
	Name         string
	Enabled      bool
	Note         string
	RateLimit    json.RawMessage
	ActionPolicy json.RawMessage
}

// JBinding exists so the hub never builds SQL-ish ids for what is really a
// name-keyed document. It carries the names the routing decisions use plus the
// numeric ids the existing API views still expose.
type JBinding struct {
	ID            int64
	BotID         int64
	BotName       string
	AccountID     int64
	AccountSelfID string
	Priority      int
	IsDefault     bool
	Enabled       bool
	Scope         json.RawMessage
}

// BindingRef locates a binding for a mutation.
type BindingRef struct {
	BotName       string
	AccountSelfID string
}

func (b BindingRef) String() string {
	return b.BotName + " -> " + b.AccountSelfID
}

// ErrNotFound mirrors connect.ErrNotFound without importing it, so the hub
// package does not depend on the storage implementation.
var ErrNotFound = fmt.Errorf("hub: not found")
