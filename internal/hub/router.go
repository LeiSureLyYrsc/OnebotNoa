package hub

import (
	"context"
	"log/slog"
	"time"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/model"
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
	account := session.Account()
	bindings := r.hub.bindingsForAccount(context.Background(), account.ID)
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
			if !conn.wantsAccount(account.ID) {
				continue
			}
			if conn.Send(raw) {
				delivered++
			}
		}
	}
	if delivered == 0 {
		r.logger.Debug("event had no subscriber", "self_id", account.SelfID, "post_type", meta.PostType)
	}
}

// RouteActionResult hands an upstream reply to the action router.
func (r *Router) RouteActionResult(_ *AccountSession, _ Peer, raw []byte) {
	r.actions.HandleResponse(raw)
}

// wantsAccount reports whether this connection subscribes to an account. The
// transparent single-account view is deliberately blind to every other account
// bound to the same Bot.
func (c *DownstreamConn) wantsAccount(accountID int64) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.fixedSelfID != "" {
		return accountID == c.fixedAccountID
	}
	b, ok := c.bindings[accountID]
	return ok && b.Enabled
}

// ------------------------------------------------------------ binding cache

// bindingsForAccount serves the account -> bindings lookup from a short-lived
// cache so the per-event path never hits SQLite. Cache entries are invalidated
// whenever the management API changes a binding.
func (h *Hub) bindingsForAccount(ctx context.Context, accountID int64) []model.Binding {
	h.bindingMu.RLock()
	if h.bindingCache != nil && time.Since(h.bindingLoaded) < bindingCacheTTL {
		out := h.bindingCache[accountID]
		h.bindingMu.RUnlock()
		return out
	}
	h.bindingMu.RUnlock()

	rows, err := h.store.ListBindings(ctx)
	if err != nil {
		h.logger.Warn("could not load bindings", "error", err)
		h.bindingMu.RLock()
		out := h.bindingCache[accountID]
		h.bindingMu.RUnlock()
		return out
	}
	cache := map[int64][]model.Binding{}
	for _, b := range rows {
		cache[b.AccountID] = append(cache[b.AccountID], b)
	}
	h.bindingMu.Lock()
	h.bindingCache = cache
	h.bindingLoaded = time.Now()
	h.bindingMu.Unlock()
	return cache[accountID]
}

// InvalidateBindings forces the next lookup to reload from the store.
func (h *Hub) InvalidateBindings() {
	h.bindingMu.Lock()
	h.bindingCache = nil
	h.bindingLoaded = time.Time{}
	h.bindingMu.Unlock()
}

// bindingCacheTTL bounds staleness if a binding is changed outside the API.
const bindingCacheTTL = 30 * time.Second
