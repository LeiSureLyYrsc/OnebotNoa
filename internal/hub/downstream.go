package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/model"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/onebot"
)

// DownstreamInfo describes an authenticated Bot connection.
type DownstreamInfo struct {
	Bot              model.Bot
	FixedSelfID      string
	RemoteAddr       string
	UserAgent        string
	TokenFingerprint string
	Source           string
}

// DownstreamConn is one Bot application connection. It owns the bot's binding
// map and resolves which account an action targets.
type DownstreamConn struct {
	id          string
	bot         model.Bot
	peer        Peer
	fixedSelfID string
	router      *ActionRouter
	hub         *Hub
	logger      *slog.Logger

	mu             sync.RWMutex
	bindings       map[int64]model.Binding
	fixedAccountID int64
	connected      time.Time
}

func newDownstreamConn(id string, info DownstreamInfo, peer Peer, router *ActionRouter, h *Hub, logger *slog.Logger) *DownstreamConn {
	return &DownstreamConn{
		id:          id,
		bot:         info.Bot,
		peer:        peer,
		fixedSelfID: info.FixedSelfID,
		router:      router,
		hub:         h,
		logger:      logger.With("bot", info.Bot.Name, "conn", id),
		bindings:    map[int64]model.Binding{},
		connected:   time.Now(),
	}
}

// ID returns the connection id.
func (c *DownstreamConn) ID() string { return c.id }

// Bot returns the authenticated Bot row.
func (c *DownstreamConn) Bot() model.Bot { return c.bot }

// FixedSelfID is non-empty for the transparent single-account view.
func (c *DownstreamConn) FixedSelfID() string { return c.fixedSelfID }

// ConnectedAt reports when the connection was established.
func (c *DownstreamConn) ConnectedAt() time.Time { return c.connected }

// Send forwards a frame to the Bot.
func (c *DownstreamConn) Send(raw []byte) bool { return c.peer.Send(raw) }

// Close closes the connection.
func (c *DownstreamConn) Close(code int, reason string) { c.peer.Close(code, reason) }

// Bindings returns a copy of the account grants.
func (c *DownstreamConn) Bindings() []model.Binding {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]model.Binding, 0, len(c.bindings))
	for _, b := range c.bindings {
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AccountID < out[j].AccountID })
	return out
}

// ReloadBindings refreshes the account grants from the store.
func (c *DownstreamConn) ReloadBindings(ctx context.Context) error {
	rows, err := c.hub.store.BindingsByBot(ctx, c.bot.ID)
	if err != nil {
		return err
	}
	next := make(map[int64]model.Binding, len(rows))
	for _, b := range rows {
		acc, err := c.hub.store.AccountByID(ctx, b.AccountID)
		if err != nil {
			continue
		}
		next[acc.ID] = b
	}
	c.mu.Lock()
	c.bindings = next
	c.mu.Unlock()
	return nil
}

func (c *DownstreamConn) bindingFor(selfID string) (model.Binding, bool) {
	acc, err := c.hub.store.AccountBySelfID(context.Background(), selfID)
	if err != nil {
		return model.Binding{}, false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	b, ok := c.bindings[acc.ID]
	if !ok || !b.Enabled {
		return model.Binding{}, false
	}
	return b, true
}

func (c *DownstreamConn) defaultBinding() (model.Binding, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var (
		fallback model.Binding
		found    bool
	)
	for _, b := range c.bindings {
		if !b.Enabled {
			continue
		}
		if b.IsDefault {
			return b, true
		}
		if !found {
			fallback, found = b, true
		}
	}
	if found && len(c.bindings) == 1 {
		return fallback, true
	}
	return model.Binding{}, false
}

// ResolveTarget implements the documented precedence:
//
//  1. transparent view -> the fixed account
//  2. frame-level self_id (relay extension)
//  3. params.self_id
//  4. the binding flagged as default
//  5. the only enabled binding
//
// An explicit target that is not bound to this Bot is refused with 1403 so a
// Bot can never silently reach a foreign account.
func (c *DownstreamConn) ResolveTarget(frame onebot.ActionFrame) (string, model.Binding, int, string) {
	if c.fixedSelfID != "" {
		c.mu.RLock()
		b, ok := c.bindings[c.fixedAccountID]
		c.mu.RUnlock()
		if !ok || !b.Enabled {
			return "", model.Binding{}, onebot.RetForbidden, "账号 " + c.fixedSelfID + " 未授权给当前 Bot"
		}
		return c.fixedSelfID, b, 0, ""
	}

	explicit := ""
	if id := frame.SelfID.String(); id != "" {
		explicit = id
	} else if id := paramsSelfID(frame.Params); id != "" {
		explicit = id
	}
	if explicit != "" {
		b, ok := c.bindingFor(explicit)
		if !ok {
			return "", model.Binding{}, onebot.RetForbidden, "账号 " + explicit + " 未授权给当前 Bot"
		}
		return explicit, b, 0, ""
	}

	if b, ok := c.defaultBinding(); ok {
		acc, err := c.hub.store.AccountByID(context.Background(), b.AccountID)
		if err != nil {
			return "", model.Binding{}, onebot.RetImplError, "绑定账号不存在"
		}
		return acc.SelfID, b, 0, ""
	}

	return "", model.Binding{}, onebot.RetNotFound, "无法确定目标账号：请在动作中指定 self_id，或将该 Bot 只绑定一个账号"
}

// paramsSelfID reads the loose params.self_id hint.
func paramsSelfID(params json.RawMessage) string {
	if len(params) == 0 {
		return ""
	}
	var probe struct {
		SelfID onebot.ID `json:"self_id"`
	}
	if err := json.Unmarshal(params, &probe); err != nil {
		return ""
	}
	return probe.SelfID.String()
}

// HandleDownstream runs the lifetime of a Bot connection.
func (h *Hub) HandleDownstream(ctx context.Context, info DownstreamInfo, peer Peer) error {
	// Any early return must still tear the socket down.
	defer peer.Close(1000, "closing")

	conn := newDownstreamConn(peer.ID(), info, peer, h.actions, h, h.logger)
	if err := conn.ReloadBindings(ctx); err != nil {
		return err
	}
	if info.FixedSelfID != "" {
		account, err := h.store.AccountBySelfID(ctx, info.FixedSelfID)
		if err != nil {
			return fmt.Errorf("hub: transparent view requested for unknown account %s", info.FixedSelfID)
		}
		conn.mu.Lock()
		_, bound := conn.bindings[account.ID]
		if bound {
			conn.fixedAccountID = account.ID
		}
		conn.mu.Unlock()
		if !bound {
			return fmt.Errorf("hub: account %s is not bound to bot %s", info.FixedSelfID, info.Bot.Name)
		}
	}
	h.addDownstream(conn)
	defer h.removeDownstream(conn)

	if err := h.store.TouchBotSeen(ctx, info.Bot.ID, time.Now()); err != nil {
		h.logger.Warn("could not record bot activity", "bot", info.Bot.Name, "error", err)
	}
	h.logger.Info("downstream connection attached",
		"bot", info.Bot.Name, "conn", peer.ID(), "addr", info.RemoteAddr,
		"bindings", len(conn.Bindings()), "fixed_self_id", info.FixedSelfID)

	writeDone := make(chan struct{})
	go func() {
		peer.WriteLoop()
		close(writeDone)
	}()

	if h.onDownstreamConnect != nil {
		h.onDownstreamConnect(ctx, conn)
	}

	err := h.downstreamReadLoop(ctx, conn)

	if h.onDownstreamDisconnect != nil {
		h.onDownstreamDisconnect(ctx, conn)
	}
	pending := h.actions.DropConn(peer.ID())
	peer.Close(1000, "closing")
	<-writeDone
	h.logger.Info("downstream connection detached",
		"bot", info.Bot.Name, "conn", peer.ID(), "dropped_pending", pending)
	return err
}

func (h *Hub) downstreamReadLoop(ctx context.Context, conn *DownstreamConn) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		raw, err := conn.peer.ReadMessage()
		if err != nil {
			return err
		}

		// The probe only decides *what kind* of frame this is. It must not
		// reject a frame merely because echo or self_id are strings.
		var probe struct {
			Action   *string `json:"action"`
			PostType string  `json:"post_type"`
		}
		if err := json.Unmarshal(raw, &probe); err != nil {
			h.logger.Warn("ignoring malformed frame from downstream", "bot", conn.Bot().Name)
			continue
		}
		if probe.Action == nil {
			if probe.PostType != "" {
				// A Bot must not push events upstream; log and ignore.
				h.logger.Warn("ignoring event pushed by a downstream connection",
					"bot", conn.Bot().Name, "post_type", probe.PostType)
			}
			continue
		}
		h.actions.HandleAction(ctx, conn, raw)
	}
}

// ------------------------------------------------------- downstream registry

func (h *Hub) addDownstream(conn *DownstreamConn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.downstreams[conn.bot.ID] == nil {
		h.downstreams[conn.bot.ID] = map[string]*DownstreamConn{}
	}
	h.downstreams[conn.bot.ID][conn.id] = conn
}

func (h *Hub) removeDownstream(conn *DownstreamConn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if m := h.downstreams[conn.bot.ID]; m != nil {
		delete(m, conn.id)
		if len(m) == 0 {
			delete(h.downstreams, conn.bot.ID)
		}
	}
}

// DownstreamCount reports how many Bot connections are live.
func (h *Hub) DownstreamCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, m := range h.downstreams {
		n += len(m)
	}
	return n
}

// DownstreamsForBot returns the live connections of one Bot.
func (h *Hub) DownstreamsForBot(botID int64) []*DownstreamConn {
	h.mu.Lock()
	defer h.mu.Unlock()
	m := h.downstreams[botID]
	out := make([]*DownstreamConn, 0, len(m))
	for _, c := range m {
		out = append(out, c)
	}
	return out
}

// AllDownstreams returns every live Bot connection.
func (h *Hub) AllDownstreams() []*DownstreamConn {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := []*DownstreamConn{}
	for _, m := range h.downstreams {
		for _, c := range m {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	return out
}
