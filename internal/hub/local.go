package hub

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/onebot"
)

// LocalService answers the actions the relay can serve itself and synthesises
// the meta events downstream Bot frameworks rely on.
//
// Why synthesise: upstream lifecycle/heartbeat frames describe the *QQ
// implementation*, not the account as the Bot sees it through the relay. A Bot
// bound to several accounts would receive several conflicting lifecycles, and a
// Bot whose account is offline would keep believing it is connected. The relay
// therefore re-states the account state on every connection and keeps it fresh.
type LocalService struct {
	hub    *Hub
	logger *slog.Logger

	mu       sync.Mutex
	stopped  bool
	stopCh   chan struct{}
	interval time.Duration
}

// NewLocalService builds the local action/meta service.
func NewLocalService(h *Hub, interval time.Duration, logger *slog.Logger) *LocalService {
	if logger == nil {
		logger = slog.Default()
	}
	if interval <= 0 {
		interval = 5 * time.Second
	}
	return &LocalService{hub: h, logger: logger, interval: interval, stopCh: make(chan struct{})}
}

// Start runs the heartbeat loop; Stop ends it.
func (l *LocalService) Start() {
	go l.heartbeatLoop()
}

// Stop ends the heartbeat loop (idempotent).
func (l *LocalService) Stop() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.stopped {
		return
	}
	l.stopped = true
	close(l.stopCh)
}

func (l *LocalService) heartbeatLoop() {
	ticker := time.NewTicker(l.interval)
	defer ticker.Stop()
	for {
		select {
		case <-l.stopCh:
			return
		case <-ticker.C:
			for _, conn := range l.hub.AllDownstreams() {
				l.sendHeartbeat(conn)
			}
		}
	}
}

// ------------------------------------------------------------------ meta events

// OnConnect emits the synthetic lifecycle/heartbeat that tells a freshly
// connected Bot which accounts it can use.
func (l *LocalService) OnConnect(ctx context.Context, conn *DownstreamConn) {
	for _, binding := range conn.Bindings() {
		if !binding.Enabled {
			continue
		}
		account, ok := l.hub.conns.AccountBySelfID(binding.AccountSelfID)
		if !ok || !conn.wantsAccount(account.SelfID) {
			continue
		}
		conn.Send(l.lifecycle(account, "connect"))
		conn.Send(l.heartbeatFrame(account))
	}
}

// OnDisconnect is a no-op today but keeps the lifecycle symmetric.
func (l *LocalService) OnDisconnect(context.Context, *DownstreamConn) {}

func (l *LocalService) sendHeartbeat(conn *DownstreamConn) {
	for _, binding := range conn.Bindings() {
		if !binding.Enabled {
			continue
		}
		account, ok := l.hub.conns.AccountBySelfID(binding.AccountSelfID)
		if !ok || !conn.wantsAccount(account.SelfID) {
			continue
		}
		conn.Send(l.heartbeatFrame(account))
	}
}

// lifecycle builds a OneBot lifecycle meta event for one account.
func (l *LocalService) lifecycle(account Account, subType string) []byte {
	frame := map[string]json.RawMessage{
		"post_type":       json.RawMessage(strconv.Quote("meta_event")),
		"meta_event_type": json.RawMessage(strconv.Quote("lifecycle")),
		"sub_type":        json.RawMessage(strconv.Quote(subType)),
		"time":            json.RawMessage(strconv.FormatInt(time.Now().Unix(), 10)),
		"self_id":         jsonNumber(account.SelfID),
	}
	out, err := json.Marshal(frame)
	if err != nil {
		return []byte(`{"post_type":"meta_event","meta_event_type":"lifecycle","sub_type":"connect"}`)
	}
	return out
}

// heartbeatFrame reports the account's live state so frameworks show it right.
func (l *LocalService) heartbeatFrame(account Account) []byte {
	state := StatusOffline
	if session, ok := l.hub.Registry().Session(account.SelfID); ok {
		state = session.State()
	}
	online := state == StatusOnline || state == StatusDegraded
	status, err := json.Marshal(map[string]any{
		"online": online,
		"good":   state == StatusOnline,
		"state":  state,
	})
	if err != nil {
		status = []byte("{}")
	}
	frame := map[string]json.RawMessage{
		"post_type":       json.RawMessage(strconv.Quote("meta_event")),
		"meta_event_type": json.RawMessage(strconv.Quote("heartbeat")),
		"time":            json.RawMessage(strconv.FormatInt(time.Now().Unix(), 10)),
		"self_id":         jsonNumber(account.SelfID),
		"status":          status,
		"interval":        json.RawMessage(strconv.FormatInt(l.interval.Milliseconds(), 10)),
	}
	out, err := json.Marshal(frame)
	if err != nil {
		return []byte(`{"post_type":"meta_event","meta_event_type":"heartbeat"}`)
	}
	return out
}

// ------------------------------------------------------------------ local actions

// HandleAction implements LocalActionHandler. It returns true when the action was
// answered locally.
// HandleAction answers an action for the account the router already resolved.
//
// selfID is authoritative: the relay never guesses which account a request meant
// (that decision belongs to the routing layer, which returns 1404 when it is
// ambiguous), and it never answers for an account the caller cannot address.
func (l *LocalService) HandleAction(ctx context.Context, conn *DownstreamConn, selfID string, frame onebot.ActionFrame, origEcho json.RawMessage) bool {
	// The action name may carry the _async/_rate_limited suffixes.
	action := onebot.BaseAction(frame.Action)
	if selfID == "" {
		selfID = conn.FixedSelfID()
	}
	if selfID == "" {
		return false
	}

	switch action {
	case "get_status":
		conn.Send(onebot.SuccessResponse(origEcho, l.statusData(ctx, conn, selfID)))
		return true
	case "get_version_info":
		conn.Send(onebot.SuccessResponse(origEcho, []byte(`{"app_name":"OnebotNoa","protocol_version":"v11","app_version":"relay"}`)))
		return true
	case "get_login_info":
		account, ok := l.hub.conns.AccountBySelfID(selfID)
		if !ok {
			return false
		}
		data, err := json.Marshal(map[string]json.RawMessage{
			"user_id":  jsonNumber(account.SelfID),
			"nickname": json.RawMessage(strconv.Quote(firstNonEmpty(account.Nickname, account.Name, account.SelfID))),
		})
		if err != nil {
			return false
		}
		conn.Send(onebot.SuccessResponse(origEcho, data))
		return true
	case "can_send_image", "can_send_record":
		conn.Send(onebot.SuccessResponse(origEcho, []byte(`{"yes":true}`)))
		return true
	case "hub_list_accounts":
		conn.Send(onebot.SuccessResponse(origEcho, l.accountsData(conn)))
		return true
	case "hub_get_account":
		if data, ok := l.accountData(ctx, selfID); ok {
			conn.Send(onebot.SuccessResponse(origEcho, data))
			return true
		}
		return false
	}
	return false
}

// statusData reports the resolved account's state plus the relay's own health.
//
// Reporting only the addressed account matters for a multi-account Bot: a single
// "online" flag covering every binding would let one offline account silently
// mask another's failure.
func (l *LocalService) statusData(ctx context.Context, conn *DownstreamConn, selfID string) []byte {
	state := StatusOffline
	if session, ok := l.hub.Registry().Session(selfID); ok {
		state = session.State()
	}
	online := state == StatusOnline || state == StatusDegraded
	payload := map[string]any{
		"online":        online,
		"good":          state == StatusOnline,
		"state":         state,
		"self_id":       selfID,
		"app_name":      "OnebotNoa",
		"accounts":      map[string]any{selfID: state},
		"account_count": 1,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return []byte("{}")
	}
	return data
}

// accountData describes one account (the hub_get_account extension).
func (l *LocalService) accountData(ctx context.Context, selfID string) ([]byte, bool) {
	account, ok := l.hub.conns.AccountBySelfID(selfID)
	if !ok {
		return nil, false
	}
	state := StatusOffline
	peers := 0
	if session, ok := l.hub.Registry().Session(selfID); ok {
		state = session.State()
		peers = len(session.Peers())
	}
	data, err := json.Marshal(map[string]any{
		"self_id":  account.SelfID,
		"name":     account.Name,
		"nickname": account.Nickname,
		"state":    state,
		"peers":    peers,
	})
	if err != nil {
		return nil, false
	}
	return data, true
}

// accountsData backs the hub_list_accounts extension action.
func (l *LocalService) accountsData(conn *DownstreamConn) []byte {
	list := []map[string]any{}
	for _, binding := range conn.Bindings() {
		if !binding.Enabled {
			continue
		}
		account, ok := l.hub.conns.AccountBySelfID(binding.AccountSelfID)
		if !ok {
			continue
		}
		state := StatusOffline
		if session, ok := l.hub.Registry().Session(account.SelfID); ok {
			state = session.State()
		}
		list = append(list, map[string]any{
			"self_id":    account.SelfID,
			"name":       account.Name,
			"nickname":   account.Nickname,
			"state":      state,
			"is_default": binding.IsDefault,
		})
	}
	data, err := json.Marshal(map[string]any{"accounts": list})
	if err != nil {
		return []byte(`{"accounts":[]}`)
	}
	return data
}

// jsonNumber keeps an id numeric in JSON (OneBot sends ids as numbers).
func jsonNumber(id string) json.RawMessage {
	if id == "" {
		return json.RawMessage("0")
	}
	for _, ch := range id {
		if ch < '0' || ch > '9' {
			quoted, err := json.Marshal(id)
			if err != nil {
				return json.RawMessage("0")
			}
			return quoted
		}
	}
	return json.RawMessage(id)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
