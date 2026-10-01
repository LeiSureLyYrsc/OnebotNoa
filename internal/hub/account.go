package hub

import (
	"errors"
	"log/slog"
	"sync"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/onebot"
)

// ErrTokenMismatch is returned when a live connection with the same
// (self_id, role) presents a different token - a likely self_id spoof.
var ErrTokenMismatch = errors.New("hub: token mismatch for the same self_id and role")

// AccountSession aggregates every physical connection of one QQ instance. An
// instance may hold up to three connections (API, Event, Universal), so the
// physical key is (kind, self_id, role, connID) and never self_id alone.
type AccountSession struct {
	mu      sync.RWMutex
	account Account
	peers   map[string]Peer
	logger  *slog.Logger
}

func newAccountSession(account Account, logger *slog.Logger) *AccountSession {
	return &AccountSession{
		account: account,
		peers:   map[string]Peer{},
		logger:  logger.With("self_id", account.SelfID),
	}
}

// SelfID returns the account's QQ id.
func (a *AccountSession) SelfID() string { return a.account.SelfID }

// Account returns a copy of the persisted row.
func (a *AccountSession) Account() Account {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.account
}

// SetAccount refreshes the cached row after a change in connect.json.
func (a *AccountSession) SetAccount(account Account) {
	a.mu.Lock()
	a.account = account
	a.mu.Unlock()
}

// AddPeer registers a connection. A connection that replaces a stale one with
// the same role is returned to the caller so it can be closed.
func (a *AccountSession) AddPeer(p Peer) (replaced Peer, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	for id, existing := range a.peers {
		if existing.Role() != p.Role() {
			continue
		}
		oldToken, newToken := existing.TokenFingerprint(), p.TokenFingerprint()
		if oldToken != "" && newToken != "" && oldToken != newToken {
			return nil, ErrTokenMismatch
		}
		if oldToken == "" && newToken == "" && existing.RemoteAddr() != p.RemoteAddr() {
			a.logger.Warn("replacing a live connection with the same role from a different address",
				"role", string(p.Role()), "old_addr", existing.RemoteAddr(), "new_addr", p.RemoteAddr())
		}
		delete(a.peers, id)
		replaced = existing
		break
	}
	a.peers[p.ID()] = p
	return replaced, nil
}

// RemovePeer detaches a connection and reports whether it was attached.
func (a *AccountSession) RemovePeer(id string) (Peer, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	p, ok := a.peers[id]
	if ok {
		delete(a.peers, id)
	}
	return p, ok
}

// Peers returns a snapshot of the attached connections.
func (a *AccountSession) Peers() []Peer {
	a.mu.RLock()
	defer a.mu.RUnlock()
	out := make([]Peer, 0, len(a.peers))
	for _, p := range a.peers {
		out = append(out, p)
	}
	return out
}

// EventPeer returns a connection that can deliver events, if any.
func (a *AccountSession) EventPeer() Peer {
	a.mu.RLock()
	defer a.mu.RUnlock()
	for _, p := range a.peers {
		if p.Role().CanReceiveEvents() {
			return p
		}
	}
	return nil
}

// APIPeer returns a connection that can carry API calls, if any.
func (a *AccountSession) APIPeer() Peer {
	a.mu.RLock()
	defer a.mu.RUnlock()
	for _, p := range a.peers {
		if p.Role().CanSendActions() {
			return p
		}
	}
	return nil
}

// State derives the account status from its live connections:
// offline (none), online (both directions), degraded (only one direction).
func (a *AccountSession) State() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if len(a.peers) == 0 {
		return StatusOffline
	}
	var events, actions bool
	for _, p := range a.peers {
		if p.Role().CanReceiveEvents() {
			events = true
		}
		if p.Role().CanSendActions() {
			actions = true
		}
	}
	if events && actions {
		return StatusOnline
	}
	return StatusDegraded
}

// CanReceiveEvents reports whether any connection can deliver events.
func (a *AccountSession) CanReceiveEvents() bool { return a.EventPeer() != nil }

// CanSendActions reports whether any connection can carry API calls.
func (a *AccountSession) CanSendActions() bool { return a.APIPeer() != nil }

// SendEvent delivers an event frame to the event connection.
func (a *AccountSession) SendEvent(raw []byte) bool {
	if p := a.EventPeer(); p != nil {
		return p.Send(raw)
	}
	return false
}

// SendAction delivers an API frame to the API connection.
func (a *AccountSession) SendAction(raw []byte) bool {
	if p := a.APIPeer(); p != nil {
		return p.Send(raw)
	}
	return false
}

// Role returns the role of the given connection, if attached.
func (a *AccountSession) RoleOf(peerID string) (onebot.Role, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if p, ok := a.peers[peerID]; ok {
		return p.Role(), true
	}
	return "", false
}
