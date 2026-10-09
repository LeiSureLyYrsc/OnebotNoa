package transport

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/auth"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/onebot"
)

func acceptOpts() *websocket.AcceptOptions {
	return &websocket.AcceptOptions{InsecureSkipVerify: true}
}

func (s *Server) serveUpstream(w http.ResponseWriter, r *http.Request, presetSelfID, pathToken, forcedRole string, requireToken bool) {
	token := firstNonEmpty(extractToken(r), pathToken)
	selfID := presetSelfID
	if selfID == "" {
		selfID = r.Header.Get("X-Self-ID")
	}
	role := forcedRole
	if role == "" {
		role = strings.ToLower(r.Header.Get("X-Client-Role"))
	}
	if role == "" {
		role = "universal"
	}

	if token != "" {
		acc, err := s.st.GetAccountByTokenHash(auth.HashToken(token))
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if selfID != "" && selfID != acc.SelfID {
			http.Error(w, "self_id does not match token", http.StatusForbidden)
			return
		}
		selfID = acc.SelfID
	} else if requireToken {
		http.Error(w, "missing access token", http.StatusUnauthorized)
		return
	}

	conn, err := websocket.Accept(w, r, acceptOpts())
	if err != nil {
		return
	}
	peer := newWSPeer(s.ctx, "upstream", newID(), role, selfID, 0, "", conn)

	handle := func(data []byte) { s.hub.HandleUpstreamFrame(selfID, data) }

	if selfID == "" {
		ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
		_, data, err := conn.Read(ctx)
		cancel()
		if err != nil {
			peer.Close()
			return
		}
		if env, ok := onebot.ParseEvent(data); ok {
			selfID = onebot.RawID(env.SelfID)
		}
		peer.selfID = selfID
		if selfID == "" {
			peer.Close()
			return
		}
		if s.hub.RegisterUpstream(selfID, role, peer) == "" {
			peer.Close()
			return
		}
		go peer.pingLoop()
		go peer.writeLoop()
		handle(data)
		peer.readLoop(handle)
		s.hub.UnregisterUpstream(selfID, peer.ID())
		peer.Close()
		return
	}

	if s.hub.RegisterUpstream(selfID, role, peer) == "" {
		peer.Close()
		return
	}
	go peer.pingLoop()
	go peer.writeLoop()
	peer.readLoop(handle)
	s.hub.UnregisterUpstream(selfID, peer.ID())
	peer.Close()
}

func (s *Server) serveDownstream(w http.ResponseWriter, r *http.Request, presetBotID int64, pathToken, fixedSelfID string) {
	var botID int64
	if presetBotID != 0 {
		if _, err := s.st.GetBot(presetBotID); err != nil {
			http.Error(w, "unknown bot", http.StatusUnauthorized)
			return
		}
		botID = presetBotID
	} else {
		token := firstNonEmpty(extractToken(r), pathToken)
		if token == "" {
			http.Error(w, "missing access token", http.StatusUnauthorized)
			return
		}
		bot, err := s.st.GetBotByTokenHash(auth.HashToken(token))
		if err != nil || !bot.Enabled {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		botID = bot.ID
	}

	conn, err := websocket.Accept(w, r, acceptOpts())
	if err != nil {
		return
	}
	peer := newWSPeer(s.ctx, "downstream", newID(), "", "", botID, fixedSelfID, conn)
	s.hub.RegisterBot(botID, peer, fixedSelfID)
	go peer.pingLoop()
	go peer.writeLoop()
	peer.readLoop(func(data []byte) { s.hub.HandleBotFrame(botID, peer, fixedSelfID, data) })
	s.hub.UnregisterBot(botID, peer.ID())
	peer.Close()
}
