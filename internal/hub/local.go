package hub

import (
	"encoding/json"
	"strconv"
	"time"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/onebot"
)

// BuildVersion is overridden at link time and reported by get_version_info.
var BuildVersion = "dev"

// tryLocal answers actions the relay can satisfy without the upstream account.
func (h *Hub) tryLocal(botID int64, peer Peer, fixedSelfID string, frame onebot.ActionFrame) bool {
	switch frame.Action {
	case "get_status", "get_status_info":
		respondLocal(peer, frame.Echo, map[string]any{"online": true, "good": true})
		return true
	case "get_version_info", ".get_version_info":
		respondLocal(peer, frame.Echo, map[string]any{
			"app_name":         "onebotnoa",
			"app_version":      BuildVersion,
			"protocol_version": "v11",
			"version":          "v11",
		})
		return true
	case "can_send_image":
		respondLocal(peer, frame.Echo, map[string]any{"yes": true})
		return true
	case "can_send_record":
		respondLocal(peer, frame.Echo, map[string]any{"yes": true})
		return true
	case "get_login_info":
		selfID := firstNonEmpty(frame.TargetSelfID(), frame.ParamsSelfID(), fixedSelfID, h.singleBindingFor(botID))
		if selfID == "" {
			respondLocal(peer, frame.Echo, map[string]any{"user_id": 0, "nickname": ""})
			return true
		}
		var nickname string
		if acc, err := h.store.GetAccountBySelfID(selfID); err == nil {
			nickname = acc.Nickname
		}
		respondLocal(peer, frame.Echo, map[string]any{"user_id": idJSON(selfID), "nickname": nickname})
		return true
	case "hub_list_accounts":
		h.mu.RLock()
		var ids []int64
		for _, b := range h.bindByBot[botID] {
			if b.Enabled {
				ids = append(ids, b.AccountID)
			}
		}
		snap := map[string]AccountRuntime{}
		for selfID, rt := range h.accounts {
			ar := AccountRuntime{SelfID: selfID, Name: rt.name}
			snap[selfID] = ar
		}
		h.mu.RUnlock()
		var out []map[string]any
		for _, id := range ids {
			for selfID, ar := range snap {
				if selfID == h.AccountSelfID(id) {
					out = append(out, map[string]any{"self_id": idJSON(selfID), "name": ar.Name})
				}
			}
		}
		respondLocal(peer, frame.Echo, out)
		return true
	}
	return false
}

func respondLocal(peer Peer, echo json.RawMessage, data any) {
	d, err := json.Marshal(data)
	if err != nil {
		d = []byte("null")
	}
	resp := struct {
		Status  string          `json:"status"`
		Retcode int             `json:"retcode"`
		Data    json.RawMessage `json:"data"`
		Echo    json.RawMessage `json:"echo,omitempty"`
	}{Status: "ok", Retcode: 0, Data: d, Echo: echo}
	b, _ := json.Marshal(resp)
	peer.Send(b)
}

// synthesizeFor emits lifecycle/heartbeat meta events for a bot connection.
func (h *Hub) synthesizeFor(botID int64, fixedSelfID string, peer Peer) {
	if h.opts.MetaEvents == "drop" {
		return
	}
	var selfIDs []string
	if fixedSelfID != "" {
		selfIDs = []string{fixedSelfID}
	} else {
		h.mu.RLock()
		seen := map[string]bool{}
		for _, b := range h.bindByBot[botID] {
			if !b.Enabled {
				continue
			}
			if s := h.accountSelfIDLocked(b.AccountID); s != "" && !seen[s] {
				seen[s] = true
				selfIDs = append(selfIDs, s)
			}
		}
		h.mu.RUnlock()
	}
	send := func(metaType string) {
		for _, s := range selfIDs {
			peer.Send(buildMeta(metaType, s))
		}
	}
	send("lifecycle")
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-h.quit:
			return
		case <-peer.Done():
			return
		case <-ticker.C:
			send("heartbeat")
		}
	}
}

func (h *Hub) accountSelfIDLocked(accountID int64) string {
	for selfID, id := range h.accountID {
		if id == accountID {
			return selfID
		}
	}
	return ""
}

func buildMeta(metaType, selfID string) []byte {
	m := map[string]any{
		"post_type":       "meta_event",
		"meta_event_type": metaType,
		"time":            time.Now().Unix(),
		"self_id":         idJSON(selfID),
	}
	switch metaType {
	case "lifecycle":
		m["sub_type"] = "connect"
	case "heartbeat":
		m["status"] = map[string]any{"online": true, "good": true}
		m["interval"] = 30000
	}
	b, _ := json.Marshal(m)
	return b
}

func idJSON(s string) any {
	if s == "" {
		return nil
	}
	if _, err := strconv.ParseInt(s, 10, 64); err == nil {
		return json.Number(s)
	}
	return s
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
