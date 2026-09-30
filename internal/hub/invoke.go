package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/model"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/onebot"
)

// Errors surfaced by the API debugger.
var (
	ErrNoTarget = errors.New("hub: no target account")
	ErrNotSent  = errors.New("hub: action was not sent")
	ErrNoReply  = errors.New("hub: no reply within the timeout")
)

// consolePeer is a virtual downstream connection that captures the reply of a
// single console-invoked action. It reuses the normal pending table, so the
// debugger exercises exactly the same echo rewriting and correlation the real
// Bot traffic does.
type consolePeer struct {
	id    string
	self  string
	mu    sync.Mutex
	reply chan []byte
	done  chan struct{}
	close sync.Once
}

func newConsolePeer(id, selfID string, buffer int) *consolePeer {
	if buffer <= 0 {
		buffer = 1
	}
	return &consolePeer{id: id, self: selfID, reply: make(chan []byte, buffer), done: make(chan struct{})}
}

func (c *consolePeer) ID() string               { return c.id }
func (c *consolePeer) Kind() string             { return KindDownstream }
func (c *consolePeer) Role() onebot.Role        { return onebot.RoleUniversal }
func (c *consolePeer) SelfID() string           { return c.self }
func (c *consolePeer) RemoteAddr() string       { return "console" }
func (c *consolePeer) TokenFingerprint() string { return "" }
func (c *consolePeer) UserAgent() string        { return "OnebotNoa-console" }
func (c *consolePeer) Label() string            { return "console/" + c.id }

// ReadMessage never yields a frame: the console connection is write-only from
// the relay's point of view.
func (c *consolePeer) ReadMessage() ([]byte, error) {
	<-c.done
	return nil, errors.New("console peer closed")
}

func (c *consolePeer) SetReadDeadlineOverride(time.Time) {}
func (c *consolePeer) WriteLoop()                        {}
func (c *consolePeer) Send(raw []byte) bool {
	copied := append([]byte(nil), raw...)
	select {
	case c.reply <- copied:
	default:
	}
	return true
}
func (c *consolePeer) Close(int, string) {
	c.close.Do(func() { close(c.done) })
}
func (c *consolePeer) Done() <-chan struct{} { return c.done }
func (c *consolePeer) Queued() int64         { return int64(len(c.reply)) }
func (c *consolePeer) Dropped() int64        { return 0 }

// consoleConnection builds a synthetic downstream connection that mirrors the
// bindings of the target account, so relay-served actions answer with the same
// account list a Bot would see.
func (h *Hub) consoleConnection(peer Peer, selfID string) *DownstreamConn {
	conn := &DownstreamConn{
		id:       peer.ID(),
		peer:     peer,
		hub:      h,
		logger:   h.logger.With("console", peer.ID()),
		bindings: map[int64]model.Binding{},
	}

	account, err := h.store.AccountBySelfID(context.Background(), selfID)
	if err != nil {
		return conn
	}
	// Every Bot that is allowed to use this account is "bound" for the console.
	bindings, err := h.store.BindingsByAccount(context.Background(), account.ID)
	if err != nil {
		return conn
	}
	for _, binding := range bindings {
		conn.bindings[binding.AccountID] = binding
		if binding.IsDefault {
			conn.fixedAccountID = binding.AccountID
		}
	}
	if len(conn.bindings) == 0 {
		conn.bindings[account.ID] = model.Binding{
			BotID: 0, AccountID: account.ID, Enabled: true, IsDefault: true, Scope: []byte("{}"),
		}
	}
	return conn
}

// Invoke runs one action against an account through the real routing path and
// returns the upstream reply (unless async).
//
// It deliberately does NOT bypass policy: rate limits, allow/deny lists and the
// offline queue all apply, because "the second Bot gets refused here" is exactly
// what the operator wants to find out.
func (h *Hub) Invoke(ctx context.Context, selfID string, frame []byte, echo json.RawMessage, async bool) (json.RawMessage, error) {
	session, ok := h.registry.Session(selfID)
	if !ok || !session.CanSendActions() {
		return nil, fmt.Errorf("%w: %s", ErrNoTarget, selfID)
	}

	var parsed onebot.ActionFrame
	if err := json.Unmarshal(frame, &parsed); err != nil {
		return nil, fmt.Errorf("invalid action frame: %w", err)
	}

	peer := newConsolePeer("console-"+strconv.FormatUint(h.actions.seq.Add(1), 10), selfID, 1)
	defer peer.Close(0, "console done")

	// The debugger must reproduce what a Bot would experience, so the actions the
	// relay serves itself (get_status, can_*, hub_*) are answered locally first,
	// exactly as they are on the real downstream path — including the bindings,
	// because hub_list_accounts and the per-account status are built from them.
	if h.actions.local != nil {
		consoleEcho := echo
		if len(consoleEcho) == 0 {
			consoleEcho = json.RawMessage(strconv.Quote("console"))
		}
		consoleConn := h.consoleConnection(peer, selfID)
		if h.actions.local.HandleAction(ctx, consoleConn, parsed, consoleEcho) {
			select {
			case raw := <-peer.reply:
				return json.RawMessage(raw), nil
			case <-time.After(2 * time.Second):
				return nil, ErrNoReply
			}
		}
	}

	key := "hub@" + peer.ID() + ":1"
	upstreamFrame, err := buildUpstreamFrame(frame, key)
	if err != nil {
		return nil, fmt.Errorf("invalid action frame: %w", err)
	}

	pa := &pendingAction{
		conn:      peer,
		botName:   "console",
		selfID:    selfID,
		origEcho:  echo,
		hasEcho:   len(echo) > 0,
		createdAt: time.Now(),
	}
	timeout := h.cfg.Policy.ActionTimeout.Std()
	if err := h.actions.pending.Add(key, pa, timeout); err != nil {
		return nil, err
	}

	if !session.SendAction(upstreamFrame) {
		h.actions.pending.Take(key)
		return nil, fmt.Errorf("%w: upstream is busy", ErrNotSent)
	}
	if h.actions.traffic != nil {
		h.actions.traffic.BotAction("console", selfID, upstreamFrame)
	}
	if async {
		return nil, nil
	}

	select {
	case raw := <-peer.reply:
		// The pending entry is removed by the reply path; make sure a late
		// timeout cannot double-release it.
		h.actions.pending.Take(key)
		return json.RawMessage(raw), nil
	case <-time.After(timeout + 500*time.Millisecond):
		h.actions.pending.Take(key)
		return nil, ErrNoReply
	case <-ctx.Done():
		h.actions.pending.Take(key)
		return nil, ctx.Err()
	}
}
