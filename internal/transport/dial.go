package transport

import (
	"context"
	"errors"
	"math/rand"
	"net/http"
	"time"

	"github.com/coder/websocket"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/config"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/onebot"
)

type dialRunner struct {
	s *Server
	d config.DialTarget
}

func newDialRunner(s *Server, d config.DialTarget) *dialRunner {
	return &dialRunner{s: s, d: d}
}

func (r *dialRunner) run(ctx context.Context) {
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		err := r.connect(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			r.s.log.Warn("dial disconnected", "name", r.d.Name, "role", r.d.Role, "err", err)
		}
		jitter := time.Duration(rand.Int63n(int64(backoff / 2)))
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff + jitter):
		}
		backoff *= 2
		if backoff > 60*time.Second {
			backoff = 60 * time.Second
		}
	}
}

func (r *dialRunner) connect(ctx context.Context) error {
	opts := &websocket.DialOptions{}
	if r.d.Token != "" {
		opts.HTTPHeader = http.Header{"Authorization": []string{"Bearer " + r.d.Token}}
	}
	dialCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	conn, _, err := websocket.Dial(dialCtx, r.d.URL, opts)
	cancel()
	if err != nil {
		return err
	}
	if r.d.Role == "downstream" {
		return r.runDownstream(ctx, conn)
	}
	return r.runUpstream(ctx, conn)
}

func (r *dialRunner) runUpstream(ctx context.Context, conn *websocket.Conn) error {
	selfID := r.d.SelfID
	peer := newWSPeer(ctx, "upstream", newID(), "universal", selfID, 0, "", conn)
	if selfID == "" {
		rctx, cancel := context.WithTimeout(ctx, 8*time.Second)
		_, data, err := conn.Read(rctx)
		cancel()
		if err != nil {
			return err
		}
		if env, ok := onebot.ParseEvent(data); ok {
			selfID = onebot.RawID(env.SelfID)
		}
		peer.selfID = selfID
		if selfID == "" {
			peer.Close()
			return errors.New("upstream dial: could not determine self_id")
		}
		if r.s.hub.RegisterUpstream(selfID, "universal", peer) == "" {
			peer.Close()
			return errors.New("upstream dial: rejected")
		}
		go peer.pingLoop()
		go peer.writeLoop()
		r.s.hub.HandleUpstreamFrame(selfID, data)
		peer.readLoop(func(b []byte) { r.s.hub.HandleUpstreamFrame(selfID, b) })
	} else {
		if r.s.hub.RegisterUpstream(selfID, "universal", peer) == "" {
			peer.Close()
			return errors.New("upstream dial: rejected")
		}
		go peer.pingLoop()
		go peer.writeLoop()
		peer.readLoop(func(b []byte) { r.s.hub.HandleUpstreamFrame(selfID, b) })
	}
	r.s.hub.UnregisterUpstream(selfID, peer.ID())
	peer.Close()
	return nil
}

func (r *dialRunner) runDownstream(ctx context.Context, conn *websocket.Conn) error {
	bot, err := r.s.st.GetBotByName(r.d.BotName)
	if err != nil {
		conn.Close(websocket.StatusPolicyViolation, "unknown bot")
		return errors.New("downstream dial: bot " + r.d.BotName + " not found")
	}
	fixed := r.d.SelfID
	peer := newWSPeer(ctx, "downstream", newID(), "", "", bot.ID, fixed, conn)
	r.s.hub.RegisterBot(bot.ID, peer, fixed)
	go peer.pingLoop()
	go peer.writeLoop()
	peer.readLoop(func(b []byte) { r.s.hub.HandleBotFrame(bot.ID, peer, fixed, b) })
	r.s.hub.UnregisterBot(bot.ID, peer.ID())
	peer.Close()
	return nil
}
