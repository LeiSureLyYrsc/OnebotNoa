package transport

import (
	"encoding/json"
)

// ConnStore is the connection-side persistence the data plane needs while it
// authenticates and routes: which token belongs to which account or Bot, which
// grants exist, and which connections are configured.
//
// It mirrors internal/connstore and is satisfied by it; keeping it an interface
// here means the transport tests can drive the data plane without a real file.
type ConnStore interface {
	// AccountBySelfID and AccountByToken resolve QQ instances.
	AccountBySelfID(selfID string) (AccountRef, bool)
	AccountByToken(token string) (AccountRef, bool)

	// BotByToken authenticates a downstream application; BotByID/BotByName are
	// used to authorise dedicated listeners and dial targets.
	BotByToken(token string) (BotRef, bool)
	BotByID(id int64) (BotRef, bool)
	BotByName(name string) (BotRef, bool)

	// BindingsByBot lists one Bot's grants; BindingByPair answers the
	// "may this Bot use this account" question.
	BindingsByBot(botName string) []BindingRef
	BindingByPair(botName, selfID string) (BindingRef, bool)

	// Connections returns the configured links; ConnectionToken reads one
	// credential for the outbound dial path.
	Connections() []ConnectionRef
	ConnectionToken(id int64) (string, error)
}

// AccountRef is one QQ instance as the data plane sees it.
type AccountRef struct {
	ID      int64
	SelfID  string
	Enabled bool
}

// BotRef is one downstream application as the data plane sees it. The policy
// fields travel with it because the relay has to decide about an action without
// a second lookup.
type BotRef struct {
	ID           int64
	Name         string
	Enabled      bool
	RateLimit    json.RawMessage
	ActionPolicy json.RawMessage
}

// BindingRef is one grant: enough to authorise a call without joining tables.
type BindingRef struct {
	BotName       string
	AccountSelfID string
	Enabled       bool
	IsDefault     bool
	Scope         json.RawMessage
}

// ConnectionRef is one configured link (shared or dedicated, dial or listen).
type ConnectionRef struct {
	ID            int64
	Name          string
	Kind          string
	Enabled       bool
	Addr          string
	URL           string
	Path          string
	Mode          string
	AccountSelfID string
	BotName       string
	FixedSelfID   string
	TLSCert       string
	TLSKey        string
	Min           string
	Max           string
	Jitter        float64
	HasToken      bool
}
