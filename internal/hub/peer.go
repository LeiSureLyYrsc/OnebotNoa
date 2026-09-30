// Package hub owns the live relay state: physical connections, account
// sessions, binding-based routing, backpressure and rate limiting.
package hub

import (
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/onebot"
)

// Connection kinds.
const (
	KindUpstream   = "upstream"
	KindDownstream = "downstream"
)

// Peer is one physical WebSocket connection. Everything the router needs is on
// this interface so tests can substitute fakes.
type Peer interface {
	ID() string
	Kind() string
	Role() onebot.Role
	SelfID() string
	RemoteAddr() string
	TokenFingerprint() string
	UserAgent() string
	Label() string // "self_id/role/connID" for logs

	// ReadMessage returns the next data frame, applying the read deadline.
	ReadMessage() ([]byte, error)
	// SetReadDeadlineOverride forces an earlier deadline (identity window).
	SetReadDeadlineOverride(t time.Time)

	// WriteLoop drains the send queue until the connection closes; exactly one
	// goroutine may run it.
	WriteLoop()
	// Send enqueues a frame; false means the frame was dropped.
	Send(raw []byte) bool
	// Close closes the connection once.
	Close(code int, reason string)
	// Done is closed when the connection is gone.
	Done() <-chan struct{}

	Queued() int64
	Dropped() int64
}

// PeerOptions configures a wsPeer.
type PeerOptions struct {
	ID               string
	Kind             string
	Role             onebot.Role
	SelfID           string
	RemoteAddr       string
	UserAgent        string
	TokenFingerprint string
	QueueSize        int
	Backpressure     string
	PingInterval     time.Duration
	PongTimeout      time.Duration
	WriteTimeout     time.Duration
	ReadTimeout      time.Duration
	MaxFrameBytes    int64
}

type wsPeer struct {
	opt    PeerOptions
	conn   *websocket.Conn
	logger *slog.Logger

	send    chan []byte
	done    chan struct{}
	closeFn sync.Once
	writeMu sync.Mutex // gorilla allows one writer; ReadMessage owns reads

	dropped atomic.Int64
	sent    atomic.Int64

	deadlineMu     sync.RWMutex
	deadlineOverride time.Time
}

// NewWSPeer wraps an upgraded WebSocket connection.
func NewWSPeer(conn *websocket.Conn, opt PeerOptions, logger *slog.Logger) *wsPeer {
	if opt.QueueSize <= 0 {
		opt.QueueSize = 1024
	}
	if opt.PingInterval <= 0 {
		opt.PingInterval = 30 * time.Second
	}
	if opt.PongTimeout <= 0 {
		opt.PongTimeout = 10 * time.Second
	}
	if opt.WriteTimeout <= 0 {
		opt.WriteTimeout = 10 * time.Second
	}
	if opt.ReadTimeout <= 0 {
		opt.ReadTimeout = opt.PingInterval + opt.PongTimeout
	}
	if opt.MaxFrameBytes <= 0 {
		opt.MaxFrameBytes = 16 << 20
	}
	if logger == nil {
		logger = slog.Default()
	}

	p := &wsPeer{
		opt:    opt,
		conn:   conn,
		logger: logger.With("conn", opt.ID, "kind", opt.Kind, "role", string(opt.Role), "self_id", opt.SelfID),
		send:   make(chan []byte, opt.QueueSize),
		done:   make(chan struct{}),
	}

	conn.SetReadLimit(opt.MaxFrameBytes)
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(opt.ReadTimeout))
	})
	_ = conn.SetReadDeadline(time.Now().Add(opt.ReadTimeout))
	return p
}

func (p *wsPeer) ID() string               { return p.opt.ID }
func (p *wsPeer) Kind() string             { return p.opt.Kind }
func (p *wsPeer) Role() onebot.Role        { return p.opt.Role }
func (p *wsPeer) SelfID() string           { return p.opt.SelfID }
func (p *wsPeer) RemoteAddr() string       { return p.opt.RemoteAddr }
func (p *wsPeer) UserAgent() string        { return p.opt.UserAgent }
func (p *wsPeer) TokenFingerprint() string { return p.opt.TokenFingerprint }
func (p *wsPeer) Done() <-chan struct{}    { return p.done }
func (p *wsPeer) Dropped() int64           { return p.dropped.Load() }
func (p *wsPeer) Sent() int64              { return p.sent.Load() }

func (p *wsPeer) Queued() int64 { return int64(len(p.send)) }

// Label identifies a connection in logs and in the WebUI.
func (p *wsPeer) Label() string {
	return p.opt.Kind + "/" + p.opt.SelfID + "/" + string(p.opt.Role) + "/" + p.opt.ID
}

// SetSelfID updates the identity once the first frame revealed it.
func (p *wsPeer) SetSelfID(selfID string) {
	p.opt.SelfID = selfID
	p.logger = p.logger.With("self_id", selfID)
}

// SetReadDeadlineOverride forces an earlier read deadline; the zero value clears
// the override.
func (p *wsPeer) SetReadDeadlineOverride(t time.Time) {
	p.deadlineMu.Lock()
	p.deadlineOverride = t
	p.deadlineMu.Unlock()
}

func (p *wsPeer) effectiveDeadline() time.Time {
	deadline := time.Now().Add(p.opt.ReadTimeout)
	p.deadlineMu.RLock()
	override := p.deadlineOverride
	p.deadlineMu.RUnlock()
	if !override.IsZero() && override.Before(deadline) {
		return override
	}
	return deadline
}

// ReadMessage reads one data frame. Control frames are handled by the
// underlying library.
func (p *wsPeer) ReadMessage() ([]byte, error) {
	_ = p.conn.SetReadDeadline(p.effectiveDeadline())
	for {
		mt, data, err := p.conn.ReadMessage()
		if err != nil {
			return nil, err
		}
		if mt == websocket.TextMessage || mt == websocket.BinaryMessage {
			return data, nil
		}
	}
}

// Send enqueues a frame applying the configured backpressure policy.
func (p *wsPeer) Send(raw []byte) bool {
	select {
	case <-p.done:
		return false
	default:
	}

	switch p.opt.Backpressure {
	case "drop_newest":
		select {
		case p.send <- raw:
			return true
		default:
			p.dropped.Add(1)
			return false
		}
	case "disconnect":
		select {
		case p.send <- raw:
			return true
		default:
			p.dropped.Add(1)
			p.Close(websocket.CloseTryAgainLater, "slow consumer")
			return false
		}
	case "block":
		timer := time.NewTimer(5 * time.Second)
		defer timer.Stop()
		select {
		case p.send <- raw:
			return true
		case <-timer.C:
			p.dropped.Add(1)
			return false
		case <-p.done:
			return false
		}
	default: // drop_oldest
		select {
		case p.send <- raw:
			return true
		default:
		}
		select {
		case <-p.send:
			p.dropped.Add(1)
		default:
		}
		select {
		case p.send <- raw:
			return true
		default:
			p.dropped.Add(1)
			return false
		}
	}
}

// WriteLoop drains the send queue and keeps the connection warm with pings. It
// returns when the connection closes; exactly one goroutine may run it.
func (p *wsPeer) WriteLoop() {
	ticker := time.NewTicker(p.opt.PingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-p.done:
			return
		case msg := <-p.send:
			if !p.write(websocket.TextMessage, msg) {
				return
			}
		case <-ticker.C:
			if !p.write(websocket.PingMessage, nil) {
				return
			}
		}
	}
}

func (p *wsPeer) write(messageType int, payload []byte) bool {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	_ = p.conn.SetWriteDeadline(time.Now().Add(p.opt.WriteTimeout))
	if err := p.conn.WriteMessage(messageType, payload); err != nil {
		p.logger.Debug("write failed", "error", err)
		p.Close(websocket.CloseInternalServerErr, "write failed")
		return false
	}
	if payload != nil {
		p.sent.Add(1)
	}
	return true
}

// Close closes the connection once and wakes the write loop.
func (p *wsPeer) Close(code int, reason string) {
	p.closeFn.Do(func() {
		close(p.done)
		p.writeMu.Lock()
		_ = p.conn.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(code, reason), time.Now().Add(2*time.Second))
		p.writeMu.Unlock()
		_ = p.conn.Close()
	})
}
