package hub

import (
	"encoding/json"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/onebot"
)

// HandleUpstreamFrame processes one frame received from a QQ-side connection.
func (h *Hub) HandleUpstreamFrame(selfID string, raw []byte) {
	if onebot.IsEvent(raw) {
		h.routeEvent(selfID, raw)
		return
	}
	h.handleResponse(raw)
}

func (h *Hub) routeEvent(selfID string, raw []byte) {
	env, ok := onebot.ParseEvent(raw)
	if !ok {
		h.stats.malformed.Add(1)
		return
	}
	h.stats.eventsTotal.Add(1)

	isMeta := env.PostType == onebot.PostMeta || env.MetaEventType != ""
	if isMeta && h.opts.MetaEvents != "passthrough" {
		h.bus.publish(Event{
			Time: time.Now().UnixMilli(), Direction: "up", Account: selfID,
			Kind: metaKind(env), Summary: "meta event (suppressed)", Raw: raw,
		})
		return
	}

	type delivery struct {
		peer  Peer
		fixed string
	}
	var deliveries []delivery

	h.mu.RLock()
	accountID, known := h.accountID[selfID]
	if known {
		for _, b := range h.bindings[accountID] {
			if !b.Enabled {
				continue
			}
			rt := h.bots[b.BotID]
			if rt == nil || !rt.enabled {
				continue
			}
			if !scopeMatch(b.Scope, env, selfID) {
				continue
			}
			for _, bc := range rt.peers {
				if bc.fixedSelfID != "" && bc.fixedSelfID != selfID {
					continue
				}
				deliveries = append(deliveries, delivery{peer: bc.peer, fixed: bc.fixedSelfID})
			}
		}
	}
	h.mu.RUnlock()

	h.bus.publish(Event{
		Time: time.Now().UnixMilli(), Direction: "up", Account: selfID,
		Kind: eventKind(env), Summary: eventSummary(env), Raw: raw,
	})

	for _, d := range deliveries {
		if !d.peer.Send(raw) {
			h.stats.dropped.Add(1)
		}
	}
}

func (h *Hub) handleResponse(raw []byte) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		h.stats.malformed.Add(1)
		return
	}
	echo := m["echo"]
	key := onebot.RawID(echo)
	if key == "" {
		return
	}
	h.pendingMu.Lock()
	inf := h.inflight[key]
	if inf != nil {
		delete(h.inflight, key)
	}
	h.pendingMu.Unlock()
	if inf == nil {
		return
	}
	inf.timer.Stop()
	if inf.hasEcho {
		m["echo"] = inf.origEcho
	} else {
		delete(m, "echo")
	}
	out, err := json.Marshal(m)
	if err != nil {
		return
	}
	h.stats.responsesTotal.Add(1)
	h.bus.publish(Event{Time: time.Now().UnixMilli(), Direction: "down", Kind: "response", Summary: "action response", Raw: out})
	inf.peer.Send(out)
}

// HandleBotFrame processes one frame received from a downstream bot connection.
func (h *Hub) HandleBotFrame(botID int64, peer Peer, fixedSelfID string, raw []byte) {
	var frame onebot.ActionFrame
	if err := json.Unmarshal(raw, &frame); err != nil || frame.Action == "" {
		h.stats.malformed.Add(1)
		return
	}
	h.stats.actionsTotal.Add(1)

	if h.tryLocal(botID, peer, fixedSelfID, frame) {
		return
	}

	target := frame.TargetSelfID()
	if target == "" {
		target = frame.ParamsSelfID()
	}
	if target == "" {
		target = fixedSelfID
	}
	if target == "" {
		target = h.defaultAccountFor(botID)
	}
	if target == "" {
		target = h.singleBindingFor(botID)
	}

	refusal := func(retcode int, msg string) {
		h.stats.refused.Add(1)
		peer.Send(onebot.ErrorResponse(frame.Echo, retcode, msg))
	}

	if target == "" {
		refusal(onebot.RetNotFound, "no target account; specify self_id")
		return
	}
	if !h.isBound(botID, target) {
		refusal(onebot.RetForbidden, "account not bound to this bot")
		return
	}

	apiPeer := h.apiPeer(target)
	if apiPeer == nil {
		refusal(onebot.RetInternal, "target account has no API connection")
		return
	}
	if !h.buckets.allow(target) {
		refusal(onebot.RetInternal, "account rate limit exceeded")
		return
	}

	key := h.nextKey(peer.ID())
	if !h.setInflight(key, peer, target, frame.Echo, len(frame.Echo) > 0) {
		refusal(onebot.RetInternal, "too many in-flight actions")
		return
	}
	rewritten, err := rewriteEcho(raw, key)
	if err != nil {
		h.clearInflight(key)
		refusal(onebot.RetBadReq, "invalid action frame")
		return
	}
	h.bus.publish(Event{
		Time: time.Now().UnixMilli(), Direction: "down", Bot: botName(h, botID),
		Account: target, Kind: "action", Summary: frame.Action, Raw: raw,
	})
	if !apiPeer.Send(rewritten) {
		h.clearInflight(key)
		refusal(onebot.RetInternal, "upstream send buffer full")
	}
}

func (h *Hub) apiPeer(selfID string) Peer {
	h.mu.RLock()
	defer h.mu.RUnlock()
	rt := h.accounts[selfID]
	if rt == nil {
		return nil
	}
	for _, c := range rt.conns {
		if c.role == "api" || c.role == "universal" {
			return c.peer
		}
	}
	return nil
}

func (h *Hub) defaultAccountFor(botID int64) string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, b := range h.bindByBot[botID] {
		if !b.Enabled || !b.IsDefault {
			continue
		}
		for selfID, id := range h.accountID {
			if id == b.AccountID {
				return selfID
			}
		}
	}
	return ""
}

func (h *Hub) singleBindingFor(botID int64) string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	var found string
	n := 0
	for _, b := range h.bindByBot[botID] {
		if !b.Enabled {
			continue
		}
		n++
		for selfID, id := range h.accountID {
			if id == b.AccountID {
				found = selfID
			}
		}
	}
	if n == 1 {
		return found
	}
	return ""
}

func (h *Hub) isBound(botID int64, selfID string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	accountID, ok := h.accountID[selfID]
	if !ok {
		return false
	}
	for _, b := range h.bindByBot[botID] {
		if b.Enabled && b.AccountID == accountID {
			return true
		}
	}
	return false
}

func (h *Hub) nextKey(peerID string) string {
	n := atomic.AddUint64(&h.seq, 1)
	return "hub#" + peerID + "#" + strconv.FormatUint(n, 10)
}

func rewriteEcho(raw []byte, key string) ([]byte, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	m["echo"] = json.RawMessage(strconv.Quote(key))
	return json.Marshal(m)
}

// metaKind returns a compact kind label for a meta event.
func metaKind(env onebot.EventEnvelope) string {
	if env.MetaEventType != "" {
		return "meta/" + env.MetaEventType
	}
	return "meta"
}

func eventKind(env onebot.EventEnvelope) string {
	if env.PostType == onebot.PostMessage {
		if env.MessageType != "" {
			return "message/" + env.MessageType
		}
		return "message"
	}
	if env.PostType != "" {
		return env.PostType
	}
	return "event"
}

func eventSummary(env onebot.EventEnvelope) string {
	summary := env.PostType
	if env.MessageType != "" {
		summary += "/" + env.MessageType
	}
	if g := onebot.RawID(env.GroupID); g != "" {
		summary += " group=" + g
	}
	if u := onebot.RawID(env.UserID); u != "" {
		summary += " user=" + u
	}
	if summary == "" {
		summary = "event"
	}
	return summary
}

// failPendingOf fails in-flight actions targeting an account that went offline.
func (h *Hub) failPendingOf(selfID string) {
	h.pendingMu.Lock()
	keys := make([]string, 0)
	for k, inf := range h.inflight {
		if inf.selfID == selfID {
			keys = append(keys, k)
		}
	}
	infos := make([]*inflight, 0, len(keys))
	for _, k := range keys {
		infos = append(infos, h.inflight[k])
		delete(h.inflight, k)
	}
	h.pendingMu.Unlock()
	for _, inf := range infos {
		inf.timer.Stop()
		inf.peer.Send(onebot.ErrorResponse(inf.origEcho, onebot.RetInternal, "account went offline"))
	}
}
