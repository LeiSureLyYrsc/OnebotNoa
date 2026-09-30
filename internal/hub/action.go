package hub

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/config"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/onebot"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/store"
)

// LocalActionHandler answers actions that the hub serves itself (get_status,
// get_login_info, x_hub extensions). Implemented in I7.
type LocalActionHandler interface {
	HandleAction(ctx context.Context, conn *DownstreamConn, frame onebot.ActionFrame, origEcho json.RawMessage) bool
}

// PreSendHook gates an outbound action (account-wide rate limit, per-binding
// allow/deny lists). Implemented in I5; a nil hook allows everything.
type PreSendHook interface {
	Allow(selfID, action string, bindingScope Scope) (retcode int, wording string, allowed bool)
}

// ActionRouter implements the action half of the relay: it resolves the target
// account, rewrites echo values so concurrent Bots never collide, correlates
// replies (including multi-frame streaming replies) and enforces timeouts.
type ActionRouter struct {
	cfg     *config.Config
	store   *store.Store
	hub     *Hub
	logger  *slog.Logger
	pending *pendingTable
	seq     atomic.Uint64

	local   LocalActionHandler
	preSend PreSendHook
}

// NewActionRouter builds the router and its pending table.
func NewActionRouter(cfg *config.Config, st *store.Store, h *Hub, logger *slog.Logger) *ActionRouter {
	if logger == nil {
		logger = slog.Default()
	}
	r := &ActionRouter{
		cfg:    cfg,
		store:  st,
		hub:    h,
		logger: logger,
	}
	r.pending = newPendingTable(cfg.Policy.PendingPerConn, cfg.Policy.PendingGlobal, r.onTimeout)
	return r
}

// SetLocalHandler installs the local-answer handler.
func (r *ActionRouter) SetLocalHandler(h LocalActionHandler) { r.local = h }

// SetPreSend installs the policy/rate-limit hook.
func (r *ActionRouter) SetPreSend(h PreSendHook) { r.preSend = h }

// PendingCount reports in-flight actions (metrics).
func (r *ActionRouter) PendingCount() int { return r.pending.Count() }

// HandleAction processes one action frame coming from a Bot.
func (r *ActionRouter) HandleAction(ctx context.Context, conn *DownstreamConn, raw []byte) {
	var frame onebot.ActionFrame
	if err := json.Unmarshal(raw, &frame); err != nil || frame.Action == "" {
		r.logger.Warn("malformed action frame from downstream", "bot", conn.Bot().Name, "error", err)
		conn.Send(onebot.FailureResponse(frame.Echo, onebot.RetBadRequest, "invalid action frame"))
		return
	}

	if r.local != nil && r.local.HandleAction(ctx, conn, frame, frame.Echo) {
		return
	}

	selfID, binding, retcode, wording := conn.ResolveTarget(frame)
	if retcode != 0 {
		r.reject(conn, frame, retcode, wording)
		return
	}

	if r.preSend != nil {
		scope, err := ParseScope(binding.Scope)
		if err != nil {
			r.reject(conn, frame, onebot.RetImplError, "binding scope is invalid: "+err.Error())
			return
		}
		if code, msg, allowed := r.preSend.Allow(selfID, onebot.BaseAction(frame.Action), scope); !allowed {
			r.reject(conn, frame, code, msg)
			return
		}
	}

	session, ok := r.hub.Registry().Session(selfID)
	if !ok || !session.CanSendActions() {
		r.reject(conn, frame, onebot.RetImplError, "账号当前不可用（离线或缺少 API 连接）: "+selfID)
		return
	}

	key := "hub@" + conn.ID() + ":" + strconv.FormatUint(r.seq.Add(1), 10)
	upstreamFrame, err := buildUpstreamFrame(raw, key)
	if err != nil {
		r.reject(conn, frame, onebot.RetBadRequest, "invalid action frame: "+err.Error())
		return
	}

	pa := &pendingAction{
		conn:      conn.peer,
		botID:     conn.Bot().ID,
		selfID:    selfID,
		origEcho:  frame.Echo,
		hasEcho:   len(frame.Echo) > 0,
		createdAt: time.Now(),
	}
	if err := r.pending.Add(key, pa, r.cfg.Policy.ActionTimeout.Std()); err != nil {
		r.reject(conn, frame, onebot.RetImplError, "在途动作过多，请稍后重试")
		return
	}

	if !session.SendAction(upstreamFrame) {
		r.pending.Take(key) // releases the entry and its timer
		r.reject(conn, frame, onebot.RetImplError, "上游连接繁忙，动作未能发出")
		return
	}

	r.logger.Debug("action forwarded", "bot", conn.Bot().Name, "account", selfID,
		"action", frame.Action, "echo", key)
}

// HandleResponse processes one frame coming back from an upstream connection.
func (r *ActionRouter) HandleResponse(raw []byte) {
	key, ok := responseEchoKey(raw)
	if !ok {
		r.logger.Debug("upstream frame without a relay echo discarded")
		return
	}
	pa, ok := r.pending.Peek(key)
	if !ok {
		r.logger.Warn("upstream reply for an unknown or expired echo", "echo", key)
		return
	}

	out, err := replaceEcho(raw, pa.origEcho, pa.hasEcho)
	if err != nil {
		r.logger.Warn("could not restore echo on upstream reply", "echo", key, "error", err)
		return
	}

	// Streaming replies (SnowLuma extension) arrive as several type=stream
	// frames followed by a terminal response. The pending entry must survive
	// every intermediate frame, otherwise the stream would be truncated.
	if isStreamFrame(raw) {
		exceeded, ok := r.pending.Touch(key,
			r.cfg.Policy.StreamIdleTimeout.Std(), r.cfg.Policy.StreamMaxTimeout.Std())
		if !ok {
			r.logger.Warn("stream frame for an action that is no longer tracked", "echo", key)
			return
		}
		if exceeded {
			r.logger.Warn("stream exceeded its maximum duration", "echo", key)
			r.finish(key)
		}
		pa.conn.Send(out)
		return
	}

	r.finish(key)
	pa.conn.Send(out)
}

// finish releases a pending entry together with its timer.
func (r *ActionRouter) finish(key string) {
	r.pending.Take(key)
}

// DropConn removes every in-flight action of a disconnected Bot connection.
func (r *ActionRouter) DropConn(connID string) int {
	return r.pending.RemoveByConn(connID)
}

// onTimeout answers a Bot whose action never came back.
func (r *ActionRouter) onTimeout(pa *pendingAction) {
	var echo json.RawMessage
	if pa.hasEcho {
		echo = pa.origEcho
	}
	wording := "上游超时未响应"
	if pa.streaming {
		wording = "上游流式响应中断"
	}
	if pa.conn.Send(onebot.FailureResponse(echo, onebot.RetImplError, wording)) {
		r.logger.Warn("action timed out", "account", pa.selfID, "conn", pa.conn.ID(), "streaming", pa.streaming)
	}
}

func (r *ActionRouter) reject(conn *DownstreamConn, frame onebot.ActionFrame, retcode int, wording string) {
	var echo json.RawMessage
	if len(frame.Echo) > 0 {
		echo = frame.Echo
	}
	conn.Send(onebot.FailureResponse(echo, retcode, wording))
	conn.hub.audit(context.Background(), "hub", "action.rejected", conn.Bot().Name,
		frame.Action+" -> "+wording)
	r.logger.Info("action rejected", "bot", conn.Bot().Name, "action", frame.Action,
		"retcode", retcode, "reason", wording)
}

// buildUpstreamFrame rewrites the echo and drops the relay-only self_id hint
// while leaving every other value untouched (params stay raw).
func buildUpstreamFrame(raw []byte, key string) ([]byte, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	delete(fields, "self_id")
	fields["echo"] = json.RawMessage(strconv.Quote(key))
	return json.Marshal(fields)
}
