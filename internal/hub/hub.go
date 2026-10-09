// Package hub is the relay core: it aggregates QQ-side connections into logical
// accounts, multicasts events to bound bots, and unicasts actions with echo
// rewriting and timeout tracking.
package hub

import (
	"log/slog"
	"sync"
	"time"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/model"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/store"
)

// Peer is one physical WebSocket connection on either side.
type Peer interface {
	ID() string
	Role() string // api | event | universal (upstream); "" for bots
	Send(raw []byte) bool
	Close()
	Done() <-chan struct{}
}

// Options configures hub policy.
type Options struct {
	ActionTimeout  time.Duration
	OfflineAction  string // fail_fast | queue
	MetaEvents     string // synthetic | passthrough | drop
	AccountRate    float64
	AccountBurst   int
	UnknownAccount string // pending | reject | auto
}

// Hub is the relay core.
type Hub struct {
	store *store.Store
	log   *slog.Logger
	opts  Options

	bus     *EventBus
	stats   *Metrics
	buckets *bucketSet

	mu        sync.RWMutex
	accounts  map[string]*accountRuntime // selfID -> runtime
	accountID map[string]int64           // selfID -> account DB id
	bots      map[int64]*botRuntime      // bot DB id -> runtime
	bindings  map[int64][]model.Binding  // account DB id -> bindings
	bindByBot map[int64][]model.Binding  // bot DB id -> bindings

	pending map[string][]Peer // unknown self_ids awaiting approval

	pendingMu sync.Mutex
	inflight  map[string]*inflight
	seq       uint64

	quit chan struct{}
}

type accountRuntime struct {
	selfID  string
	name    string
	enabled bool
	conns   map[string]connEntry
}

type connEntry struct {
	peer Peer
	role string
}

type botRuntime struct {
	id      int64
	name    string
	enabled bool
	token   string
	peers   map[string]botConn
}

type botConn struct {
	peer        Peer
	fixedSelfID string // empty => aggregate view
}

// New constructs a hub bound to the store.
func New(st *store.Store, log *slog.Logger, opts Options) *Hub {
	if opts.ActionTimeout <= 0 {
		opts.ActionTimeout = 15 * time.Second
	}
	if opts.MetaEvents == "" {
		opts.MetaEvents = "synthetic"
	}
	if opts.AccountRate <= 0 {
		opts.AccountRate = 1
	}
	if opts.AccountBurst <= 0 {
		opts.AccountBurst = 5
	}
	if opts.UnknownAccount == "" {
		opts.UnknownAccount = "auto"
	}
	h := &Hub{
		store:     st,
		log:       log,
		opts:      opts,
		bus:       newBus(2000),
		stats:     newMetrics(),
		buckets:   newBucketSet(opts.AccountRate, opts.AccountBurst),
		accounts:  map[string]*accountRuntime{},
		accountID: map[string]int64{},
		bots:      map[int64]*botRuntime{},
		bindings:  map[int64][]model.Binding{},
		bindByBot: map[int64][]model.Binding{},
		pending:   map[string][]Peer{},
		inflight:  map[string]*inflight{},
		quit:      make(chan struct{}),
	}
	return h
}

// Bus exposes the event bus for the SSE stream.
func (h *Hub) Bus() *EventBus { return h.bus }

// Stats exposes counters.
func (h *Hub) Stats() *Metrics { return h.stats }

// Close stops background timers.
func (h *Hub) Close() { close(h.quit) }

// Reload refreshes the account/bot/binding snapshot from the store.
func (h *Hub) Reload() error {
	accounts, err := h.store.ListAccounts()
	if err != nil {
		return err
	}
	bots, err := h.store.ListBots()
	if err != nil {
		return err
	}
	bindings, err := h.store.ListBindings()
	if err != nil {
		return err
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	// Preserve live connections while refreshing metadata.
	byID := map[string]int64{}
	for _, a := range accounts {
		byID[a.SelfID] = a.ID
		if rt, ok := h.accounts[a.SelfID]; ok {
			rt.name = a.Name
			rt.enabled = a.Enabled
			continue
		}
		h.accounts[a.SelfID] = &accountRuntime{
			selfID:  a.SelfID,
			name:    a.Name,
			enabled: a.Enabled,
			conns:   map[string]connEntry{},
		}
	}
	// Drop runtimes whose account was deleted.
	for selfID := range h.accounts {
		if _, ok := byID[selfID]; !ok {
			h.accounts[selfID].enabled = false
		}
	}
	bindAcc := map[int64][]model.Binding{}
	for _, b := range bindings {
		bindAcc[b.AccountID] = append(bindAcc[b.AccountID], b)
	}
	bindBot := map[int64][]model.Binding{}
	for _, b := range bindings {
		bindBot[b.BotID] = append(bindBot[b.BotID], b)
	}
	botByID := map[int64]*botRuntime{}
	for _, b := range bots {
		rt := h.bots[b.ID]
		if rt == nil {
			rt = &botRuntime{id: b.ID, peers: map[string]botConn{}}
		}
		rt.name = b.Name
		rt.enabled = b.Enabled
		rt.token = b.TokenHash
		botByID[b.ID] = rt
	}
	h.bots = botByID
	h.accountID = byID
	h.bindings = bindAcc
	h.bindByBot = bindBot
	return nil
}

// LookupBotByToken resolves a downstream token hash to its bot.
func (h *Hub) LookupBotByToken(tokenHash string) (*model.Bot, error) {
	bots, err := h.store.ListBots()
	if err != nil {
		return nil, err
	}
	for i := range bots {
		if bots[i].TokenHash == tokenHash && bots[i].Enabled {
			return &bots[i], nil
		}
	}
	return nil, store.ErrNotFound
}

// RegisterUpstream registers a QQ-side connection. It returns "ok" when the
// account is known/enabled, "pending" when the account is unknown and policy is
// to await approval, or "" when the connection must be rejected.
func (h *Hub) RegisterUpstream(selfID, role string, peer Peer) string {
	pending, allowed, err := h.ensureAccount(selfID)
	if err != nil {
		h.log.Error("register upstream: ensure account", "self_id", selfID, "err", err)
		return ""
	}
	if !allowed {
		return ""
	}
	if pending {
		h.pendingMu.Lock()
		h.pending[selfID] = append(h.pending[selfID], peer)
		h.pendingMu.Unlock()
		h.stats.pendingConns.Add(1)
		h.bus.publish(Event{Time: time.Now().UnixMilli(), Direction: "system", Account: selfID, Kind: "pending", Summary: "QQ instance awaiting approval", Raw: nil})
		return "pending"
	}

	h.mu.Lock()
	rt := h.accounts[selfID]
	if rt == nil {
		rt = &accountRuntime{selfID: selfID, enabled: true, conns: map[string]connEntry{}}
		h.accounts[selfID] = rt
	}
	if rt.conns == nil {
		rt.conns = map[string]connEntry{}
	}
	rt.conns[peer.ID()] = connEntry{peer: peer, role: role}
	h.mu.Unlock()

	h.stats.upstreamConns.Add(1)
	if err := h.store.SetAccountStatus(selfID, h.accountStatus(selfID), "", time.Now().Unix()); err != nil {
		h.log.Warn("persist account status", "self_id", selfID, "err", err)
	}
	h.bus.publish(Event{Time: time.Now().UnixMilli(), Direction: "system", Account: selfID, Kind: "connect", Summary: role + " connection established", Raw: nil})
	return "ok"
}

func (h *Hub) ensureAccount(selfID string) (pending bool, allowed bool, err error) {
	h.mu.RLock()
	_, known := h.accountID[selfID]
	h.mu.RUnlock()
	if known {
		return false, true, nil
	}
	if _, err := h.store.GetAccountBySelfID(selfID); err == nil {
		if err := h.Reload(); err != nil {
			return false, false, err
		}
		return false, true, nil
	} else if err != store.ErrNotFound {
		return false, false, err
	}
	switch h.unknownPolicy() {
	case "reject":
		return false, false, nil
	case "auto":
		if _, err := h.store.UpsertAccountBySelfID(selfID); err != nil {
			return false, false, err
		}
		if err := h.Reload(); err != nil {
			return false, false, err
		}
		return false, true, nil
	default: // pending
		return true, true, nil
	}
}

func (h *Hub) unknownPolicy() string {
	return h.opts.UnknownAccount
}

// ApproveAccount promotes a pending self_id into a real account.
func (h *Hub) ApproveAccount(selfID string) error {
	if _, err := h.store.UpsertAccountBySelfID(selfID); err != nil {
		return err
	}
	if err := h.Reload(); err != nil {
		return err
	}
	h.pendingMu.Lock()
	peers := h.pending[selfID]
	delete(h.pending, selfID)
	h.pendingMu.Unlock()
	for _, p := range peers {
		h.RegisterUpstream(selfID, p.Role(), p)
	}
	return nil
}

// PendingAccounts lists self_ids awaiting approval.
func (h *Hub) PendingAccounts() []string {
	h.pendingMu.Lock()
	defer h.pendingMu.Unlock()
	out := make([]string, 0, len(h.pending))
	for k := range h.pending {
		out = append(out, k)
	}
	return out
}

// UnregisterUpstream removes a QQ-side connection.
func (h *Hub) UnregisterUpstream(selfID string, peerID string) {
	h.mu.Lock()
	if rt := h.accounts[selfID]; rt != nil {
		delete(rt.conns, peerID)
	}
	h.mu.Unlock()
	h.stats.upstreamConns.Add(-1)
	h.failPendingOf(selfID)
	if err := h.store.SetAccountStatus(selfID, h.accountStatus(selfID), "", time.Now().Unix()); err != nil {
		h.log.Warn("persist account status", "self_id", selfID, "err", err)
	}
}

// RegisterBot registers a downstream bot connection.
func (h *Hub) RegisterBot(botID int64, peer Peer, fixedSelfID string) {
	h.mu.Lock()
	rt := h.bots[botID]
	if rt == nil {
		rt = &botRuntime{id: botID, name: "", enabled: true, peers: map[string]botConn{}}
		h.bots[botID] = rt
	}
	rt.peers[peer.ID()] = botConn{peer: peer, fixedSelfID: fixedSelfID}
	h.mu.Unlock()

	h.stats.botConns.Add(1)
	h.bus.publish(Event{Time: time.Now().UnixMilli(), Direction: "system", Bot: botName(h, botID), Kind: "connect", Summary: "bot connection established", Raw: nil})
	go h.synthesizeFor(botID, fixedSelfID, peer)
}

// UnregisterBot removes a downstream bot connection.
func (h *Hub) UnregisterBot(botID int64, peerID string) {
	h.mu.Lock()
	if rt := h.bots[botID]; rt != nil {
		delete(rt.peers, peerID)
	}
	h.mu.Unlock()
	h.stats.botConns.Add(-1)
}

func botName(h *Hub, botID int64) string {
	if b, err := h.store.GetBot(botID); err == nil {
		return b.Name
	}
	return ""
}

func (h *Hub) accountStatus(selfID string) string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	rt := h.accounts[selfID]
	if rt == nil {
		return model.StatusOffline
	}
	hasEvent, hasAPI := false, false
	for _, c := range rt.conns {
		if c.role == "event" || c.role == "universal" {
			hasEvent = true
		}
		if c.role == "api" || c.role == "universal" {
			hasAPI = true
		}
	}
	switch {
	case hasEvent && hasAPI:
		return model.StatusOnline
	case hasEvent || hasAPI:
		return model.StatusDegraded
	default:
		return model.StatusOffline
	}
}

// AccountRuntime returns a snapshot of connection info for one self_id.
type AccountRuntime struct {
	SelfID   string     `json:"self_id"`
	Name     string     `json:"name"`
	Enabled  bool       `json:"enabled"`
	Status   string     `json:"status"`
	CanEvent bool       `json:"can_event"`
	CanAPI   bool       `json:"can_api"`
	Conns    []ConnInfo `json:"connections"`
}

// ConnInfo describes one physical upstream connection.
type ConnInfo struct {
	PeerID string `json:"peer_id"`
	Role   string `json:"role"`
}

// SnapshotAccounts returns runtime info for all known accounts.
func (h *Hub) SnapshotAccounts() map[string]AccountRuntime {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make(map[string]AccountRuntime, len(h.accounts))
	for selfID, rt := range h.accounts {
		ar := AccountRuntime{SelfID: selfID, Name: rt.name, Enabled: rt.enabled}
		for id, c := range rt.conns {
			ar.Conns = append(ar.Conns, ConnInfo{PeerID: id, Role: c.role})
			if c.role == "event" || c.role == "universal" {
				ar.CanEvent = true
			}
			if c.role == "api" || c.role == "universal" {
				ar.CanAPI = true
			}
		}
		switch {
		case ar.CanEvent && ar.CanAPI:
			ar.Status = model.StatusOnline
		case ar.CanEvent || ar.CanAPI:
			ar.Status = model.StatusDegraded
		default:
			ar.Status = model.StatusOffline
		}
		out[selfID] = ar
	}
	return out
}

// BotOnlineCount returns the number of live connections for a bot.
func (h *Hub) BotOnlineCount(botID int64) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if rt := h.bots[botID]; rt != nil {
		return len(rt.peers)
	}
	return 0
}

// BoundAccountIDs returns the account DB ids bound to a bot.
func (h *Hub) BoundAccountIDs(botID int64) []int64 {
	h.mu.RLock()
	defer h.mu.RUnlock()
	var out []int64
	for _, b := range h.bindByBot[botID] {
		if b.Enabled {
			out = append(out, b.AccountID)
		}
	}
	return out
}

// AccountSelfID resolves an account DB id to its self_id.
func (h *Hub) AccountSelfID(accountID int64) string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for selfID, id := range h.accountID {
		if id == accountID {
			return selfID
		}
	}
	return ""
}
