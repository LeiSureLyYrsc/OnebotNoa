package transport

import (
	"net/http"
	"strings"

	"github.com/gorilla/websocket"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/hub"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/onebot"
)

// handleDownstreamWS accepts a Bot-side OneBot application.
//
// Views:
//
//	/onebot/v11/bot/ws                          shared endpoint, token via header/query
//	/onebot/v11/bot/ws/{token}                  aggregated view (all bound accounts)
//	/onebot/v11/bot/ws/{token}/{self_id}        transparent single-account view
func (d *DataPlane) handleDownstreamWS(w http.ResponseWriter, r *http.Request) {
	pathToken, fixedSelfID := parseDownstreamPath(r.PathValue("rest"))

	token := pathToken
	tokenSource := "path"
	if token == "" {
		token, tokenSource = d.presentedToken(r, "")
	}
	remoteAddr := d.remoteAddr(r)

	if token == "" {
		d.logger.Warn("downstream connection rejected: no token", "addr", remoteAddr)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	bot, found := d.conns.BotByToken(token)
	if !found {
		d.logger.Warn("downstream connection rejected: unknown token", "addr", remoteAddr, "token_source", tokenSource)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if !bot.Enabled {
		d.logger.Warn("downstream connection rejected: bot disabled", "bot", bot.Name, "addr", remoteAddr)
		http.Error(w, "bot disabled", http.StatusForbidden)
		return
	}

	// A dedicated downstream listener serves exactly one Bot, and may also pin
	// the single account that Bot sees on this address.
	source := "downstream_ws"
	if binding, dedicated := bindingOf(r.Context()); dedicated {
		if !d.downstreamListenerAllows(binding, token) {
			d.logger.Warn("dedicated downstream listener rejected a connection",
				"listener", binding.Name, "bot", bot.Name, "addr", remoteAddr)
			http.Error(w, "this listener belongs to another bot", http.StatusForbidden)
			return
		}
		source = "listener:" + binding.Name
		if binding.FixedSelfID != "" && fixedSelfID == "" {
			fixedSelfID = binding.FixedSelfID
		}
	}

	conn, err := d.upgrader.Upgrade(w, r, nil)
	if err != nil {
		d.logger.Warn("websocket upgrade failed", "addr", remoteAddr, "error", err)
		return
	}

	peer := hub.NewWSPeer(conn, hub.PeerOptions{
		ID:               d.nextConnID("down"),
		Kind:             hub.KindDownstream,
		Role:             onebot.RoleUniversal,
		SelfID:           fixedSelfID,
		RemoteAddr:       remoteAddr,
		UserAgent:        r.UserAgent(),
		TokenFingerprint: hub.FingerprintToken(token),
		QueueSize:        d.cfg.Policy.WriteQueueSize,
		Backpressure:     d.cfg.Policy.BotBackpressure,
		PingInterval:     d.cfg.Policy.PingInterval.Std(),
		PongTimeout:      d.cfg.Policy.PongTimeout.Std(),
		MaxFrameBytes:    16 << 20,
	}, d.logger)

	d.track(peer)
	defer d.untrack(peer)

	info := hub.DownstreamInfo{
		// The full Bot travels with the connection: the relay decides about every
		// action without a second lookup, so its policy must already be here.
		Bot: hub.Bot{
			ID: bot.ID, Name: bot.Name, Enabled: bot.Enabled,
			RateLimit: bot.RateLimit, ActionPolicy: bot.ActionPolicy,
		},
		FixedSelfID:      fixedSelfID,
		RemoteAddr:       remoteAddr,
		UserAgent:        r.UserAgent(),
		TokenFingerprint: hub.FingerprintToken(token),
		Source:           source,
	}

	if err := d.hub.HandleDownstream(r.Context(), info, peer); err != nil {
		d.logger.Info("downstream connection finished", "bot", bot.Name, "conn", peer.ID(), "error", err)
	}
}

// parseDownstreamPath splits the optional path segments of the Bot endpoint.
func parseDownstreamPath(rest string) (token, fixedSelfID string) {
	rest = strings.Trim(rest, "/")
	if rest == "" {
		return "", ""
	}
	parts := strings.Split(rest, "/")
	token = parts[0]
	if len(parts) > 1 {
		fixedSelfID = parts[1]
	}
	return token, fixedSelfID
}

var _ = websocket.TextMessage
