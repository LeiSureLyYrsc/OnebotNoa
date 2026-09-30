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
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/store"
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

	store    *store.Store
	logger   *slog.Logger
	observer Observer
}

func newRegistry(st *store.Store, logger *slog.Logger) *Registry {
	return &Registry{
		sessions: map[string]*AccountSession{},
		pending:  map[string]*PendingConn{},
		policy:   "pending",
		store:    st,
		logger:   logger,
		observer: NopObserver{},
	}
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
		account, err := r.store.AccountBySelfID(ctx, selfID)
		switch {
		case err == nil:
			// known account
		case errors.Is(err, store.ErrNotFound):
			switch r.policy {
			case "auto":
				account, err = r.store.EnsureAccount(ctx, selfID, "", info.Source)
				if err != nil {
					return nil, fmt.Errorf("hub: auto-create account %s: %w", selfID, err)
				}
				r.logger.Info("auto-created account", "self_id", selfID, "source", info.Source)
			case "reject":
				r.recordRejected(ctx, info, "unknown account")
				return nil, ErrUnknownAccount
			default: // pending
				r.recordPending(info)
				return nil, ErrPendingApproval
			}
		default:
			return nil, fmt.Errorf("hub: load account %s: %w", selfID, err)
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
	if err := r.store.UpdateAccountStatus(ctx, session.Account().ID, state, time.Now()); err != nil {
		r.logger.Warn("could not persist account status", "self_id", selfID, "error", err)
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
	if err := r.store.UpdateAccountStatus(ctx, session.Account().ID, state, time.Now()); err != nil {
		r.logger.Warn("could not persist account status", "self_id", selfID, "error", err)
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
func (r *Registry) ApprovePending(ctx context.Context, id string) (model.Account, error) {
	r.mu.Lock()
	entry, ok := r.pending[id]
	if ok {
		delete(r.pending, id)
	}
	r.mu.Unlock()
	if !ok {
		return model.Account{}, store.ErrNotFound
	}

	account, err := r.store.EnsureAccount(ctx, entry.SelfID, "", entry.Source)
	if err != nil {
		return model.Account{}, err
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
	if err := r.store.AppendAudit(ctx, entry); err != nil {
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
