// Package transport implements the OneBot V11 data plane: WebSocket listeners
// (shared and dedicated) for both QQ and Bot sides, outbound dialing, and the
// HTTP compatibility layer.
package transport

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	"github.com/coder/websocket"
)

const (
	writeTimeout   = 10 * time.Second
	pingInterval   = 30 * time.Second
	peerQueueDepth = 1024
	maxMessageSize = 1 << 20
)

// wsPeer is one physical WebSocket connection satisfying hub.Peer.
type wsPeer struct {
	id          string
	kind        string
	role        string
	selfID      string
	botID       int64
	fixedSelfID string

	conn   *websocket.Conn
	out    chan []byte
	done   chan struct{}
	once   sync.Once
	ctx    context.Context
	cancel context.CancelFunc
}

func newWSPeer(parent context.Context, kind, id, role, selfID string, botID int64, fixedSelfID string, conn *websocket.Conn) *wsPeer {
	ctx, cancel := context.WithCancel(parent)
	return &wsPeer{
		id: id, kind: kind, role: role, selfID: selfID, botID: botID, fixedSelfID: fixedSelfID,
		conn: conn, out: make(chan []byte, peerQueueDepth), done: make(chan struct{}),
		ctx: ctx, cancel: cancel,
	}
}

func (p *wsPeer) ID() string            { return p.id }
func (p *wsPeer) Role() string          { return p.role }
func (p *wsPeer) Done() <-chan struct{} { return p.done }

func (p *wsPeer) Send(raw []byte) bool {
	select {
	case <-p.done:
		return false
	default:
	}
	select {
	case p.out <- raw:
		return true
	case <-p.done:
		return false
	default:
		return false
	}
}

func (p *wsPeer) Close() {
	p.once.Do(func() {
		close(p.done)
		p.cancel()
		_ = p.conn.Close(websocket.StatusNormalClosure, "bye")
	})
}

func (p *wsPeer) writeLoop() {
	for {
		select {
		case <-p.done:
			return
		case msg := <-p.out:
			ctx, cancel := context.WithTimeout(p.ctx, writeTimeout)
			err := p.conn.Write(ctx, websocket.MessageText, msg)
			cancel()
			if err != nil {
				p.Close()
				return
			}
		}
	}
}

func (p *wsPeer) pingLoop() {
	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-p.done:
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(p.ctx, writeTimeout)
			err := p.conn.Ping(ctx)
			cancel()
			if err != nil {
				p.Close()
				return
			}
		}
	}
}

func (p *wsPeer) readLoop(handle func([]byte)) {
	p.conn.SetReadLimit(maxMessageSize)
	for {
		typ, data, err := p.conn.Read(p.ctx)
		if err != nil {
			p.Close()
			return
		}
		if typ != websocket.MessageText {
			continue
		}
		handle(data)
	}
}

func newID() string {
	buf := make([]byte, 8)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}
