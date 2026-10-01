package hub

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/auth"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/model"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/onebot"
)

// Errors surfaced by the registry.
var (
	ErrNeedIdentity    = errors.New("hub: identity not established yet")
	ErrUnknownAccount  = errors.New("hub: unknown account")
	ErrPendingApproval = errors.New("hub: account awaiting approval")
	ErrAccountDisabled = errors.New("hub: account disabled")
)

// maxPending bounds the approval queue so a reconnect loop cannot grow it
// without limit.
const maxPending = 64

// UpstreamInfo describes a connecting QQ-side implementation connection.
type UpstreamInfo struct {
	SelfID           string
	Role             onebot.Role
	RemoteAddr       string
	UserAgent        string
	TokenFingerprint string
	// TokenAccountSelfID is set when the presented token is pre-bound to an
	// account, which resolves the identity without any header.
	TokenAccountSelfID string
	Source             string
}

// PendingConn is a connection refused because its account is not approved yet.
// Reverse-WS clients reconnect on their own, so approval only has to create the
// account (or bind the token) for the next attempt to succeed.
type PendingConn struct {
	ID               string
	SelfID           string
	Role             onebot.Role
	RemoteAddr       string
	UserAgent        string
	TokenFingerprint string
	Source           string
	FirstSeen        time.Time
	LastSeen         time.Time
	Attempts         int
}

// Registry tracks live account sessions and the pending-approval queue.
type Registry struct {
	mu       sync.Mutex
	sessions map[string]*AccountSession
	pending  map[string]*PendingConn
	policy   string

	conns     ConnStore
	logger    *slog.Logger
	observer  Observer
	onConnect func(selfID string)
	// audit is the management-database writer, injected by the hub so the hub
	// package does not depend on the SQLite store.
	audit func(model.AuditEntry) error
}

func newRegistry(conns ConnStore, logger *slog.Logger) *Registry {
	return &Registry{
		sessions: map[string]*AccountSession{},
		pending:  map[string]*PendingConn{},
		policy:   "pending",
		conns:    conns,
		logger:   logger,
		observer: NopObserver{},
	}
}

// SetAuditWriter installs the audit sink used for refused connections.
func (r *Registry) SetAuditWriter(write func(model.AuditEntry) error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.audit = write
}

func (r *Registry) setObserver(o Observer) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if o == nil {
		o = NopObserver{}
	}
	r.observer = o
}

func (r *Registry) notify(ev AccountEvent) {
	r.mu.Lock()
	observer := r.observer
	r.mu.Unlock()
	observer.AccountChanged(ev)
}

func (r *Registry) notifyFrame(selfID string, role onebot.Role, raw []byte) {
	r.mu.Lock()
	observer := r.observer
	r.mu.Unlock()
	observer.UpstreamFrame(selfID, role, raw)
}

// AttachUpstream binds a physical connection to an account session.
func (r *Registry) AttachUpstream(ctx context.Context, info UpstreamInfo, peer Peer) (*AccountSession, error) {
	selfID := info.SelfID
	if selfID == "" {
		selfID = info.TokenAccountSelfID
	}
	if selfID == "" {
		return nil, ErrNeedIdentity
	}

	r.mu.Lock()
	session, live := r.sessions[selfID]
	r.mu.Unlock()

	if !live {
		var account Account
		existing, found := r.conns.AccountBySelfID(selfID)
		switch {
		case found:
			account = existing
		default:
			switch r.policy {
			case "auto":
				created, _, err := r.conns.EnsureAccount(selfID, info.Source)
				if err != nil {
					return nil, fmt.Errorf("hub: auto-create account %s: %w", selfID, err)
				}
				account = created
				r.logger.Info("auto-created account", "self_id", selfID, "source", info.Source)
			case "reject":
				r.recordRejected(ctx, info, "unknown account")
				return nil, ErrUnknownAccount
			default: // pending
				r.recordPending(info)
				return nil, ErrPendingApproval
			}
		}

		if !account.Enabled {
			r.recordRejected(ctx, info, "account disabled")
			return nil, ErrAccountDisabled
		}

		r.mu.Lock()
		if existing, ok := r.sessions[selfID]; ok {
			session = existing
		} else {
			session = newAccountSession(account, r.logger)
			r.sessions[selfID] = session
		}
		r.mu.Unlock()
	}

	replaced, err := session.AddPeer(peer)
	if err != nil {
		r.recordRejected(ctx, info, "token mismatch for an existing connection")
		return nil, err
	}
	if replaced != nil {
		r.logger.Warn("replaced a stale connection with the same role",
			"self_id", selfID, "role", string(replaced.Role()), "old_conn", replaced.ID(), "new_conn", peer.ID())
		replaced.Close(1000, "replaced by a newer connection")
	}

	state := session.State()
	if err := r.conns.PersistAccountState(selfID, state); err != nil {
		r.logger.Warn("could not record account status", "self_id", selfID, "error", err)
	}
	r.notify(AccountEvent{
		Type:       EventPeerConnected,
		SelfID:     selfID,
		AccountID:  session.Account().ID,
		Role:       peer.Role(),
		PeerID:     peer.ID(),
		RemoteAddr: peer.RemoteAddr(),
		State:      state,
		At:         time.Now(),
	})
	r.logger.Info("upstream connection attached",
		"self_id", selfID, "role", string(peer.Role()), "state", state, "addr", peer.RemoteAddr())

	// The account can now accept API calls: let the caller flush anything that
	// was queued while it was offline.
	if session.CanSendActions() {
		r.mu.Lock()
		hook := r.onConnect
		r.mu.Unlock()
		if hook != nil {
			hook(selfID)
		}
	}
	return session, nil
}

// Detach removes a connection and returns the account session it belonged to.
func (r *Registry) Detach(ctx context.Context, selfID, peerID string) *AccountSession {
	r.mu.Lock()
	session, ok := r.sessions[selfID]
	r.mu.Unlock()
	if !ok {
		return nil
	}

	peer, removed := session.RemovePeer(peerID)
	if !removed {
		return session
	}

	state := session.State()
	if err := r.conns.PersistAccountState(selfID, state); err != nil {
		r.logger.Warn("could not record account status", "self_id", selfID, "error", err)
	}
	r.notify(AccountEvent{
		Type:      EventPeerDisconnected,
		SelfID:    selfID,
		AccountID: session.Account().ID,
		Role:      peer.Role(),
		PeerID:    peerID,
		State:     state,
		At:        time.Now(),
	})
	r.logger.Info("upstream connection detached", "self_id", selfID, "role", string(peer.Role()), "state", state)

	if len(session.Peers()) == 0 {
		r.mu.Lock()
		if current, ok := r.sessions[selfID]; ok && current == session {
			delete(r.sessions, selfID)
		}
		r.mu.Unlock()
	}
	return session
}

// Session returns the live session of an account.
func (r *Registry) Session(selfID string) (*AccountSession, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sessions[selfID]
	return s, ok
}

// Sessions returns every live session.
func (r *Registry) Sessions() []*AccountSession {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*AccountSession, 0, len(r.sessions))
	for _, s := range r.sessions {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SelfID() < out[j].SelfID() })
	return out
}

// Pending returns the pending-approval queue, most recent first.
func (r *Registry) Pending() []PendingConn {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]PendingConn, 0, len(r.pending))
	for _, p := range r.pending {
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastSeen.After(out[j].LastSeen) })
	return out
}

// ApprovePending creates the account behind a pending entry so that the client's
// next reconnect succeeds.
func (r *Registry) ApprovePending(ctx context.Context, id string) (Account, error) {
	r.mu.Lock()
	entry, ok := r.pending[id]
	if ok {
		delete(r.pending, id)
	}
	r.mu.Unlock()
	if !ok {
		return Account{}, ErrNotFound
	}

	account, _, err := r.conns.EnsureAccount(entry.SelfID, entry.Source)
	if err != nil {
		return Account{}, err
	}
	r.logger.Info("pending account approved", "self_id", entry.SelfID, "account_id", account.ID)
	return account, nil
}

// RejectPending forgets a pending entry.
func (r *Registry) RejectPending(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.pending[id]
	delete(r.pending, id)
	return ok
}

func (r *Registry) recordPending(info UpstreamInfo) {
	key := info.SelfID + "|" + string(info.Role)
	nowT := time.Now()

	r.mu.Lock()
	defer r.mu.Unlock()

	if entry, ok := r.pending[key]; ok {
		entry.LastSeen = nowT
		entry.Attempts++
		entry.RemoteAddr = info.RemoteAddr
		return
	}
	if len(r.pending) >= maxPending {
		var oldestKey string
		var oldest time.Time
		for k, v := range r.pending {
			if oldestKey == "" || v.LastSeen.Before(oldest) {
				oldestKey, oldest = k, v.LastSeen
			}
		}
		delete(r.pending, oldestKey)
		r.logger.Warn("pending account queue is full, dropped the oldest entry")
	}
	r.pending[key] = &PendingConn{
		ID:               key,
		SelfID:           info.SelfID,
		Role:             info.Role,
		RemoteAddr:       info.RemoteAddr,
		UserAgent:        info.UserAgent,
		TokenFingerprint: info.TokenFingerprint,
		Source:           info.Source,
		FirstSeen:        nowT,
		LastSeen:         nowT,
		Attempts:         1,
	}
	r.logger.Warn("account awaiting approval",
		"self_id", info.SelfID, "role", string(info.Role), "addr", info.RemoteAddr, "policy", r.policy)
}

// recordRejected writes an audit entry and a log line for a refused connection.
func (r *Registry) recordRejected(ctx context.Context, info UpstreamInfo, reason string) {
	r.logger.Warn("upstream connection rejected",
		"self_id", info.SelfID, "role", string(info.Role), "addr", info.RemoteAddr, "reason", reason)
	entry := model.AuditEntry{
		At:     time.Now(),
		Actor:  "hub",
		Action: "upstream.rejected",
		Target: info.SelfID,
		Detail: reason + " addr=" + info.RemoteAddr + " token_fp=" + info.TokenFingerprint,
		IP:     info.RemoteAddr,
	}
	if r.audit == nil {
		return
	}
	if err := r.audit(entry); err != nil {
		r.logger.Warn("could not write audit entry", "error", err)
	}
}

// ShortFingerprint turns a token hash into a non-secret identity hint used to
// detect two different tokens claiming the same self_id.
func ShortFingerprint(tokenHash string) string {
	if len(tokenHash) > 12 {
		return tokenHash[:12]
	}
	return tokenHash
}

// FingerprintToken hashes a presented token and shortens it for comparison.
func FingerprintToken(token string) string {
	if token == "" {
		return ""
	}
	return ShortFingerprint(auth.HashToken(token))
}
