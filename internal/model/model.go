// Package model defines the persisted domain entities shared by the store,
// the management API, and the connect document.
package model

// Account is a logical QQ instance (a OneBot implementation) aggregated from
// one or more physical connections sharing the same self_id.
type Account struct {
	ID         int64  `json:"id"`
	SelfID     string `json:"self_id"`
	Name       string `json:"name"`
	Nickname   string `json:"nickname"`
	TokenHash  string `json:"-"`
	Enabled    bool   `json:"enabled"`
	Status     string `json:"status"`
	CreatedAt  int64  `json:"created_at"`
	LastSeenAt int64  `json:"last_seen_at"`
}

// Bot is a downstream OneBot application client.
type Bot struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	TokenHash    string `json:"-"`
	Enabled      bool   `json:"enabled"`
	RateLimit    string `json:"rate_limit"`    // JSON
	ActionPolicy string `json:"action_policy"` // JSON
	Note         string `json:"note"`
	CreatedAt    int64  `json:"created_at"`
}

// Binding assigns one account to one bot with an optional event filter scope.
type Binding struct {
	ID        int64  `json:"id"`
	BotID     int64  `json:"bot_id"`
	AccountID int64  `json:"account_id"`
	Priority  int    `json:"priority"`
	IsDefault bool   `json:"is_default"`
	Enabled   bool   `json:"enabled"`
	Scope     string `json:"scope"` // JSON
}

// Listener is a dynamically managed dedicated listen endpoint.
type Listener struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Role      string `json:"role"` // upstream | downstream
	BindAddr  string `json:"bind_addr"`
	Path      string `json:"path"`
	AccountID *int64 `json:"account_id,omitempty"`
	BotID     *int64 `json:"bot_id,omitempty"`
	Enabled   bool   `json:"enabled"`
}

// User is a management-plane operator.
type User struct {
	ID           int64  `json:"id"`
	Username     string `json:"username"`
	PasswordHash string `json:"-"`
	Role         string `json:"role"`
	CreatedAt    int64  `json:"created_at"`
}

// Session is a management-plane login session.
type Session struct {
	Token     string `json:"-"`
	UserID    int64  `json:"user_id"`
	ExpiresAt int64  `json:"expires_at"`
}

// AuditEntry records a management or data-plane action.
type AuditEntry struct {
	ID     int64  `json:"id"`
	At     int64  `json:"at"`
	Actor  string `json:"actor"`
	Action string `json:"action"`
	Target string `json:"target"`
	Detail string `json:"detail"`
	IP     string `json:"ip"`
}

// Account status values.
const (
	StatusOffline  = "offline"
	StatusOnline   = "online"
	StatusDegraded = "degraded"
)

// Listener roles.
const (
	RoleUpstream   = "upstream"
	RoleDownstream = "downstream"
)
