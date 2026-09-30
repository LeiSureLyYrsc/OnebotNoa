package hub

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/config"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/model"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/onebot"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/store"
)

// Account events pushed to observers (WebUI SSE, metrics).
const (
	EventPeerConnected    = "peer_connected"
	EventPeerDisconnected = "peer_disconnected"
	EventAccountChanged   = "account_changed"
)

// AccountEvent is emitted when a connection or account state changes.
type AccountEvent struct {
	Type       string
	SelfID     string
	AccountID  int64
	Role       onebot.Role
	PeerID     string
	RemoteAddr string
	State      string
	At         time.Time
}

// Observer receives hub-level notifications.
type Observer interface {
	AccountChanged(ev AccountEvent)
	UpstreamFrame(selfID string, role onebot.Role, raw []byte)
}

// FanOutObserver forwards to several observers (live view + metrics).
type FanOutObserver []Observer

// AccountChanged implements Observer.
func (f FanOutObserver) AccountChanged(ev AccountEvent) {
	for _, o := range f {
		o.AccountChanged(ev)
	}
}

// UpstreamFrame implements Observer.
func (f FanOutObserver) UpstreamFrame(selfID string, role onebot.Role, raw []byte) {
	for _, o := range f {
		o.UpstreamFrame(selfID, role, raw)
	}
}

// FanOutTraffic forwards to several traffic observers.
type FanOutTraffic []TrafficObserver

// BotAction implements TrafficObserver.
func (f FanOutTraffic) BotAction(bot, selfID string, raw []byte) {
	for _, o := range f {
		o.BotAction(bot, selfID, raw)
	}
}

// BotResponse implements TrafficObserver.
func (f FanOutTraffic) BotResponse(bot string, raw []byte) {
	for _, o := range f {
		o.BotResponse(bot, raw)
	}
}

// PolicyRejected implements TrafficObserver.
func (f FanOutTraffic) PolicyRejected(bot, action string, retcode int, reason string) {
	for _, o := range f {
		o.PolicyRejected(bot, action, retcode, reason)
	}
}

// ActionTimedOut implements TrafficObserver.
func (f FanOutTraffic) ActionTimedOut(bot, selfID string) {
	for _, o := range f {
		o.ActionTimedOut(bot, selfID)
	}
}

// NopObserver discards everything.
type NopObserver struct{}

// AccountChanged implements Observer.
func (NopObserver) AccountChanged(AccountEvent) {}

// UpstreamFrame implements Observer.
func (NopObserver) UpstreamFrame(string, onebot.Role, []byte) {}

// SetConnectHook registers a callback invoked whenever an account becomes able
// to send actions (used to flush the offline action queue).
func (h *Hub) SetConnectHook(fn func(selfID string)) {
	h.registry.mu.Lock()
	h.registry.onConnect = fn
	h.registry.mu.Unlock()
}

// Dispatcher receives routed frames; implemented by the router in I3/I4.
type Dispatcher interface {
	RouteEvent(session *AccountSession, meta onebot.EventMeta, raw []byte)
	RouteActionResult(session *AccountSession, peer Peer, raw []byte)
}

// Hub is the live relay: registries plus the routing entry points used by the
// transports.
type Hub struct {
	cfg        *config.Config
	store      *store.Store
	logger     *slog.Logger
	registry   *Registry
	dispatcher Dispatcher
	actions    *ActionRouter
	router     *Router

	// downstream connections, keyed by bot id then connection id
	mu          sync.Mutex
	downstreams map[int64]map[string]*DownstreamConn

	// binding cache (account -> bindings) for the per-event path
	bindingMu     sync.RWMutex
	bindingCache  map[int64][]model.Binding
	bindingLoaded time.Time

	onDownstreamConnect    func(context.Context, *DownstreamConn)
	onDownstreamDisconnect func(context.Context, *DownstreamConn)
}

// New builds a hub for the given configuration and store.
func New(cfg *config.Config, st *store.Store, logger *slog.Logger) *Hub {
	if logger == nil {
		logger = slog.Default()
	}
	reg := newRegistry(st, logger)
	reg.policy = cfg.OneBot.UpstreamWS.UnknownAccountPolicy

	h := &Hub{
		cfg:         cfg,
		store:       st,
		logger:      logger,
		registry:    reg,
		downstreams: map[int64]map[string]*DownstreamConn{},
	}
	h.actions = NewActionRouter(cfg, st, h, logger)
	h.router = NewRouter(h, h.actions, logger)
	h.dispatcher = h.router
	return h
}

// Actions exposes the action router (policy hooks, pending metrics).
func (h *Hub) Actions() *ActionRouter { return h.actions }

// SetDownstreamHooks installs connection lifecycle callbacks (meta event
// synthesis, event ring notifications).
func (h *Hub) SetDownstreamHooks(onConnect, onDisconnect func(context.Context, *DownstreamConn)) {
	h.onDownstreamConnect = onConnect
	h.onDownstreamDisconnect = onDisconnect
}

// audit records a hub-level event in the audit log.
func (h *Hub) audit(ctx context.Context, actor, action, target, detail string) {
	entry := model.AuditEntry{
		At:     time.Now(),
		Actor:  actor,
		Action: action,
		Target: target,
		Detail: detail,
	}
	if err := h.store.AppendAudit(ctx, entry); err != nil {
		h.logger.Warn("could not write audit entry", "action", action, "error", err)
	}
}

// Registry exposes the account registry.
func (h *Hub) Registry() *Registry { return h.registry }

// SetObserver installs the observer (event ring, SSE, metrics).
func (h *Hub) SetObserver(o Observer) { h.registry.setObserver(o) }

// SetDispatcher installs the frame router.
func (h *Hub) SetDispatcher(d Dispatcher) { h.dispatcher = d }

// AccountSessions returns every live account session.
func (h *Hub) AccountSessions() []*AccountSession { return h.registry.Sessions() }

// HandleUpstream runs the lifetime of an upstream connection: identity,
// read/write loops and detach. It returns after the connection is closed.
func (h *Hub) HandleUpstream(ctx context.Context, info UpstreamInfo, peer Peer) error {
	if p, ok := peer.(*wsPeer); ok {
		defer p.SetReadDeadlineOverride(time.Time{})
	}

	session, err := h.registry.AttachUpstream(ctx, info, peer)
	switch {
	case err == nil:
		// identified from headers or a pre-bound token
	case errors.Is(err, ErrNeedIdentity):
		session = nil
	default:
		return err
	}

	if session == nil {
		// First-frame fallback: wait up to identity_timeout for a frame that
		// carries self_id (lifecycle/heartbeat arrive immediately).
		deadline := time.Now().Add(h.cfg.OneBot.UpstreamWS.IdentityTimeout.Std())
		peer.SetReadDeadlineOverride(deadline)
		h.logger.Debug("waiting for first-frame identity", "conn", peer.ID(), "deadline", deadline)
	}

	writeDone := make(chan struct{})
	go func() {
		peer.WriteLoop()
		close(writeDone)
	}()

	readErr := h.upstreamReadLoop(ctx, &session, peer, info)

	peer.Close(1000, "closing")
	<-writeDone
	if session != nil {
		h.registry.Detach(ctx, session.SelfID(), peer.ID())
	}
	return readErr
}

func (h *Hub) upstreamReadLoop(ctx context.Context, session **AccountSession, peer Peer, info UpstreamInfo) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		raw, err := peer.ReadMessage()
		if err != nil {
			if *session == nil {
				h.logger.Warn("connection closed before it identified itself",
					"conn", peer.ID(), "addr", peer.RemoteAddr(), "error", err)
				return ErrNeedIdentity
			}
			return err
		}

		if *session == nil {
			selfID := onebot.ExtractSelfID(raw)
			if selfID == "" {
				continue // still waiting for identity; the frame is discarded
			}
			info.SelfID = selfID
			if wp, ok := peer.(*wsPeer); ok {
				wp.SetSelfID(selfID)
				wp.SetReadDeadlineOverride(time.Time{})
			}
			attached, err := h.registry.AttachUpstream(ctx, info, peer)
			if err != nil {
				return err
			}
			*session = attached
		}

		h.dispatchUpstreamFrame(*session, peer, raw)
	}
}

// dispatchUpstreamFrame routes one frame from a QQ-side implementation.
func (h *Hub) dispatchUpstreamFrame(session *AccountSession, peer Peer, raw []byte) {
	role := peer.Role()
	if role.CanReceiveEvents() {
		if meta, ok := onebot.ParseEventMeta(raw); ok {
			h.registry.notifyFrame(session.SelfID(), role, raw)
			h.onUpstreamEvent(session, peer, meta, raw)
			return
		}
	}
	if role.CanSendActions() || onebot.IsActionResult(raw) {
		h.registry.notifyFrame(session.SelfID(), role, raw)
		h.onUpstreamActionResult(session, peer, raw)
	}
}

// onUpstreamEvent handles an event produced by a QQ instance.
func (h *Hub) onUpstreamEvent(session *AccountSession, peer Peer, meta onebot.EventMeta, raw []byte) {
	if meta.PostType == "meta_event" && meta.DetailType == "lifecycle" {
		h.logger.Info("upstream lifecycle", "self_id", session.SelfID(), "sub_type", meta.SubType)
	}
	if h.dispatcher != nil {
		h.dispatcher.RouteEvent(session, meta, raw)
	}
}

// onUpstreamActionResult handles API replies; the pending table arrives in I3.
func (h *Hub) onUpstreamActionResult(session *AccountSession, peer Peer, raw []byte) {
	if h.dispatcher != nil {
		h.dispatcher.RouteActionResult(session, peer, raw)
	}
}

// IngestEvent routes an event that arrived over HTTP instead of a WebSocket.
//
// The relay treats it exactly like a frame read from the upstream socket: it is
// metered, recorded in the live view and multicast to every bound Bot with the
// original bytes. The account must already exist (HTTP clients identify
// themselves with X-Self-ID, which the transport resolved before calling here).
func (h *Hub) IngestEvent(ctx context.Context, selfID string, raw []byte) error {
	session, ok := h.registry.Session(selfID)
	if !ok {
		// No WebSocket session: make sure the account exists so the event has an
		// owner, then route it directly.
		if _, err := h.store.EnsureAccount(ctx, selfID, "", "http"); err != nil {
			return fmt.Errorf("hub: %w", err)
		}
		h.registry.notifyFrame(selfID, onebot.RoleUniversal, raw)
		if meta, ok := onebot.ParseEventMeta(raw); ok {
			h.router.RouteEventBySelfID(selfID, meta, raw)
		}
		return nil
	}
	h.registry.notifyFrame(session.SelfID(), onebot.RoleUniversal, raw)
	if meta, ok := onebot.ParseEventMeta(raw); ok {
		h.router.RouteEvent(session, meta, raw)
	}
	return nil
}

// SendActionTo forwards an already-encoded action frame to an account.
func (h *Hub) SendActionTo(selfID string, raw []byte) bool {
	session, ok := h.registry.Session(selfID)
	if !ok {
		return false
	}
	return session.SendAction(raw)
}

// Status summarizes the live relay state.
type Status struct {
	Accounts        int
	Online          int
	Degraded        int
	Offline         int
	UpstreamConns   int
	PendingAccounts int
	DownstreamConns int
	PendingActions  int
}

// Status reports live counters for the dashboard.
func (h *Hub) Status() Status {
	var st Status
	for _, s := range h.registry.Sessions() {
		st.Accounts++
		st.UpstreamConns += len(s.Peers())
		switch s.State() {
		case model.StatusOnline:
			st.Online++
		case model.StatusDegraded:
			st.Degraded++
		default:
			st.Offline++
		}
	}
	st.PendingAccounts = len(h.registry.Pending())
	st.DownstreamConns = h.DownstreamCount()
	st.PendingActions = h.actions.PendingCount()
	return st
}
