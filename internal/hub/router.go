package hub

import (
	"context"
	"log/slog"
	"time"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/onebot"
)

// Router implements Dispatcher: it multicasts upstream events to every Bot that
// is bound to the account and passes API replies to the action router.
type Router struct {
	hub     *Hub
	actions *ActionRouter
	logger  *slog.Logger
}

// NewRouter builds the event/response router.
func NewRouter(h *Hub, actions *ActionRouter, logger *slog.Logger) *Router {
	if logger == nil {
		logger = slog.Default()
	}
	return &Router{hub: h, actions: actions, logger: logger}
}

// RouteEvent delivers one event to every matching Bot connection.
//
// The frame bytes are forwarded untouched: they already carry self_id, so a Bot
// with several bound accounts can tell them apart without any injected field.
func (r *Router) RouteEvent(session *AccountSession, meta onebot.EventMeta, raw []byte) {
	r.deliver(session.Account().SelfID, meta, raw)
}

// RouteEventBySelfID multicasts an event whose sender has no live session (for
// example an event delivered over HTTP).
func (r *Router) RouteEventBySelfID(selfID string, meta onebot.EventMeta, raw []byte) {
	if _, ok := r.hub.conns.AccountBySelfID(selfID); !ok {
		r.logger.Warn("event from an unknown account was dropped", "self_id", selfID)
		return
	}
	r.deliver(selfID, meta, raw)
}

// deliver multicasts one event to every Bot bound to selfID.
func (r *Router) deliver(selfID string, meta onebot.EventMeta, raw []byte) {
	bindings := r.hub.bindingsForAccount(selfID)
	if len(bindings) == 0 {
		return
	}

	delivered := 0
	for _, b := range bindings {
		if !b.Enabled {
			continue
		}
		scope, err := ParseScope(b.Scope)
		if err != nil {
			r.logger.Warn("invalid binding scope", "binding", b.ID, "error", err)
			continue
		}
		if !scope.Match(meta, r.hub.cfg.Policy.MetaEvents) {
			continue
		}
		for _, conn := range r.hub.DownstreamsForBot(b.BotID) {
			if !conn.wantsAccount(selfID) {
				continue
			}
			if conn.Send(raw) {
				delivered++
			}
		}
	}
	if delivered == 0 {
		r.logger.Debug("event had no subscriber", "self_id", selfID, "post_type", meta.PostType)
	}
}

// RouteActionResult hands an upstream reply to the action router.
func (r *Router) RouteActionResult(_ *AccountSession, _ Peer, raw []byte) {
	r.actions.HandleResponse(raw)
}

// wantsAccount reports whether this connection subscribes to an account. The
// transparent single-account view is deliberately blind to every other account
// bound to the same Bot.
func (c *DownstreamConn) wantsAccount(selfID string) bool {
	if c.fixedSelfID != "" {
		return selfID == c.fixedSelfID
	}
	_, ok := c.bindingFor(selfID)
	return ok
}

// ------------------------------------------------------------ binding cache

// bindingsForAccount serves the account -> bindings lookup from a short-lived
// cache. The per-event path is the hottest path in the process, so it must not
// re-scan connect.json for every frame; cache entries are invalidated whenever
// the management API changes a grant.
func (h *Hub) bindingsForAccount(selfID string) []JBinding {
	h.bindingMu.RLock()
	if h.bindingCache != nil && time.Since(h.bindingLoaded) < bindingCacheTTL {
		out := h.bindingCache[selfID]
		h.bindingMu.RUnlock()
		return out
	}
	h.bindingMu.RUnlock()

	rows := h.conns.Bindings()
	cache := map[string][]JBinding{}
	for _, b := range rows {
		cache[b.AccountSelfID] = append(cache[b.AccountSelfID], b)
	}
	h.bindingMu.Lock()
	h.bindingCache = cache
	h.bindingLoaded = time.Now()
	h.bindingMu.Unlock()
	return cache[selfID]
}

// InvalidateBindings forces the next lookup to reload from the store.
func (h *Hub) InvalidateBindings() {
	h.bindingMu.Lock()
	h.bindingCache = nil
	h.bindingLoaded = time.Time{}
	h.bindingMu.Unlock()
}

// RefreshBindings invalidates the cache and reloads the grants of every live Bot
// connection, so a management change takes effect immediately.
func (h *Hub) RefreshBindings(ctx context.Context) {
	h.InvalidateBindings()
	for _, conn := range h.AllDownstreams() {
		if err := conn.ReloadBindings(ctx); err != nil {
			h.logger.Warn("could not reload bot bindings", "bot", conn.Bot().Name, "error", err)
		}
	}
}

// bindingCacheTTL bounds staleness if a binding is changed outside the API.
const bindingCacheTTL = 30 * time.Second
