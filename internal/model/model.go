// Package model holds the persisted entities shared by the store, the hub and
// the management API.
//
// Conventions:
//   - Every SQLite id is int64 (QQ ids in JSON payloads are never parsed into
//     floats; they stay json.RawMessage on the protocol path).
//   - Timestamps are stored as unix seconds.
//   - JSON columns (tags, scope, rate_limit, ...) keep raw JSON text so the API
//     can pass them through without a lossy map round-trip.
package model

import (
	"encoding/json"
	"time"
)

// Account status values.
const (
	StatusOffline    = "offline"
	StatusConnecting = "connecting"
	StatusOnline     = "online"
	StatusDegraded   = "degraded"
)

// User is a management-plane account (WebUI login).
type User struct {
	ID           int64     `json:"id"`
	Username     string    `json:"username"`
	PasswordHash string    `json:"-"`
	Role         string    `json:"role"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// Session is a logged-in management session. Only the token hash is stored.
type Session struct {
	TokenHash  string    `json:"-"`
	UserID     int64     `json:"user_id"`
	CSRFToken  string    `json:"csrf_token"`
	CreatedAt  time.Time `json:"created_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	LastSeenAt time.Time `json:"last_seen_at"`
	UserAgent  string    `json:"user_agent"`
	IP         string    `json:"ip"`
}

// Account is one QQ instance (an upstream OneBot implementation), aggregated
// across however many physical connections it opens.
type Account struct {
	ID         int64           `json:"id"`
	SelfID     string          `json:"self_id"`
	Name       string          `json:"name"`
	Nickname   string          `json:"nickname"`
	Avatar     string          `json:"avatar"`
	Enabled    bool            `json:"enabled"`
	Status     string          `json:"status"`
	Tags       json.RawMessage `json:"tags"`
	HasToken   bool            `json:"has_token"`
	Source     string          `json:"source"`
	LastSeenAt *time.Time      `json:"last_seen_at,omitempty"`
	CreatedAt  time.Time       `json:"created_at"`
	UpdatedAt  time.Time       `json:"updated_at"`
}

// Bot is a downstream OneBot application.
type Bot struct {
	ID           int64           `json:"id"`
	Name         string          `json:"name"`
	TokenHash    string          `json:"-"`
	Enabled      bool            `json:"enabled"`
	RateLimit    json.RawMessage `json:"rate_limit"`
	ActionPolicy json.RawMessage `json:"action_policy"`
	Note         string          `json:"note"`
	LastSeenAt   *time.Time      `json:"last_seen_at,omitempty"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
}

// Binding grants one Bot access to one Account, with an optional filter scope.
type Binding struct {
	ID        int64           `json:"id"`
	BotID     int64           `json:"bot_id"`
	AccountID int64           `json:"account_id"`
	Priority  int             `json:"priority"`
	IsDefault bool            `json:"is_default"`
	Enabled   bool            `json:"enabled"`
	Scope     json.RawMessage `json:"scope"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

// Listener is a dedicated (own address/path) data-plane listener.
type Listener struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	Kind        string    `json:"kind"`
	BindAddr    string    `json:"bind_addr"`
	Path        string    `json:"path"`
	AccountID   *int64    `json:"account_id,omitempty"`
	BotID       *int64    `json:"bot_id,omitempty"`
	FixedSelfID string    `json:"fixed_self_id"`
	TLSCert     string    `json:"tls_cert"`
	TLSKey      string    `json:"tls_key"`
	Enabled     bool      `json:"enabled"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Endpoint is a relay-dialed connection (the hub is the WebSocket client).
// Secrets such as outbound tokens stay in config.yaml, never here.
type Endpoint struct {
	ID          int64           `json:"id"`
	Name        string          `json:"name"`
	Kind        string          `json:"kind"`
	URL         string          `json:"url"`
	Mode        string          `json:"mode"`
	AccountHint string          `json:"account_hint"`
	BotID       *int64          `json:"bot_id,omitempty"`
	Reconnect   json.RawMessage `json:"reconnect"`
	Enabled     bool            `json:"enabled"`
	// Token is the credential the hub presents when it dials. It must be usable
	// in the clear, so it is stored as-is and never serialised to the API.
	Token    string `json:"-"`
	HasToken bool   `json:"has_token"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

// AuditEntry records a management action or a rejected data-plane call.
type AuditEntry struct {
	ID     int64     `json:"id"`
	At     time.Time `json:"at"`
	Actor  string    `json:"actor"`
	Action string    `json:"action"`
	Target string    `json:"target"`
	Detail string    `json:"detail"`
	IP     string    `json:"ip"`
}
