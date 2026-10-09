package hub

import (
	"encoding/json"
	"time"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/onebot"
)

// maxInflight bounds the echo pending table.
const maxInflight = 4096

type inflight struct {
	peer     Peer
	selfID   string
	origEcho json.RawMessage
	hasEcho  bool
	timer    *time.Timer
}

func (h *Hub) setInflight(key string, peer Peer, selfID string, origEcho json.RawMessage, hasEcho bool) bool {
	h.pendingMu.Lock()
	if len(h.inflight) >= maxInflight {
		h.pendingMu.Unlock()
		return false
	}
	inf := &inflight{peer: peer, selfID: selfID, origEcho: origEcho, hasEcho: hasEcho}
	h.inflight[key] = inf
	h.pendingMu.Unlock()

	inf.timer = time.AfterFunc(h.opts.ActionTimeout, func() {
		h.pendingMu.Lock()
		var live *inflight
		if h.inflight[key] == inf {
			live = inf
			delete(h.inflight, key)
		}
		h.pendingMu.Unlock()
		if live != nil {
			h.stats.timeouts.Add(1)
			inf.peer.Send(onebot.ErrorResponse(inf.origEcho, onebot.RetInternal, "upstream timeout"))
		}
	})
	return true
}

func (h *Hub) clearInflight(key string) {
	h.pendingMu.Lock()
	inf := h.inflight[key]
	delete(h.inflight, key)
	h.pendingMu.Unlock()
	if inf != nil {
		inf.timer.Stop()
	}
}
