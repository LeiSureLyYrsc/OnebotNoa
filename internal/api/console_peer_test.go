package api

import (
	"sync"
	"time"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/onebot"
)

// consoleTestPeer is a Peer backed by channels: the hub's upstream read loop
// pulls frames from it, so a test can answer a console-invoked action exactly
// like a real implementation would.
type consoleTestPeer struct {
	id     string
	selfID string
	role   onebot.Role

	outbox chan []byte
	inbox  chan []byte
	done   chan struct{}
	once   sync.Once
}

func newConsolePeerForTest(id, selfID string) *consoleTestPeer {
	return &consoleTestPeer{
		id:     id,
		selfID: selfID,
		role:   onebot.RoleUniversal,
		outbox: make(chan []byte, 8),
		inbox:  make(chan []byte, 8),
		done:   make(chan struct{}),
	}
}

func (p *consoleTestPeer) ID() string                        { return p.id }
func (p *consoleTestPeer) Kind() string                      { return "upstream" }
func (p *consoleTestPeer) Role() onebot.Role                 { return p.role }
func (p *consoleTestPeer) SelfID() string                    { return p.selfID }
func (p *consoleTestPeer) RemoteAddr() string                { return "test" }
func (p *consoleTestPeer) TokenFingerprint() string          { return "" }
func (p *consoleTestPeer) UserAgent() string                 { return "test" }
func (p *consoleTestPeer) Label() string                     { return "test/" + p.id }
func (p *consoleTestPeer) SetReadDeadlineOverride(time.Time) {}
func (p *consoleTestPeer) WriteLoop()                        {}
func (p *consoleTestPeer) Queued() int64                     { return int64(len(p.outbox)) }
func (p *consoleTestPeer) Dropped() int64                    { return 0 }
func (p *consoleTestPeer) Done() <-chan struct{}             { return p.done }

// ReadMessage yields the next frame the test injected, or fails once closed.
func (p *consoleTestPeer) ReadMessage() ([]byte, error) {
	select {
	case frame := <-p.inbox:
		return frame, nil
	case <-p.done:
		return nil, errConsoleClosed
	}
}

// Send records a frame the relay pushed towards the implementation.
func (p *consoleTestPeer) Send(raw []byte) bool {
	select {
	case p.outbox <- append([]byte(nil), raw...):
		return true
	default:
		return false
	}
}

func (p *consoleTestPeer) Close(int, string) {
	p.once.Do(func() { close(p.done) })
}

// deliver injects a frame as if the implementation had sent it.
func (p *consoleTestPeer) deliver(raw []byte) {
	select {
	case p.inbox <- raw:
	case <-p.done:
	}
}

var errConsoleClosed = errString("console peer closed")

type errString string

func (e errString) Error() string { return string(e) }
