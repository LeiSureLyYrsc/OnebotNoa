package hub

import (
	"sync"
	"time"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/onebot"
)

// fakePeer is a Peer for unit tests that never touches a socket.
type fakePeer struct {
	id       string
	kind     string
	role     onebot.Role
	selfID   string
	addr     string
	tokenFP  string
	userAgnt string

	mu      sync.Mutex
	sent    [][]byte
	closed  bool
	closeCh chan struct{}
	once    sync.Once
	frames  chan []byte
}

func newFakePeer(id string, role onebot.Role) *fakePeer {
	return &fakePeer{
		id: id, kind: KindUpstream, role: role, addr: "127.0.0.1", closeCh: make(chan struct{}),
		frames: make(chan []byte, 16),
	}
}

func (f *fakePeer) ID() string               { return f.id }
func (f *fakePeer) Kind() string             { return f.kind }
func (f *fakePeer) Role() onebot.Role        { return f.role }
func (f *fakePeer) SelfID() string           { return f.selfID }
func (f *fakePeer) RemoteAddr() string       { return f.addr }
func (f *fakePeer) TokenFingerprint() string { return f.tokenFP }
func (f *fakePeer) UserAgent() string        { return f.userAgnt }
func (f *fakePeer) Label() string            { return f.kind + "/" + f.id }
func (f *fakePeer) Done() <-chan struct{}    { return f.closeCh }
func (f *fakePeer) Queued() int64            { return 0 }
func (f *fakePeer) Dropped() int64           { return 0 }
func (f *fakePeer) SetReadDeadlineOverride(time.Time) {}
func (f *fakePeer) WriteLoop()               {}
func (f *fakePeer) ReadMessage() ([]byte, error) {
	select {
	case <-f.closeCh:
		return nil, errFakeClosed
	case frame := <-f.frames:
		return frame, nil
	}
}

func (f *fakePeer) Send(raw []byte) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return false
	}
	copied := make([]byte, len(raw))
	copy(copied, raw)
	f.sent = append(f.sent, copied)
	return true
}

func (f *fakePeer) sentFrames() [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([][]byte, len(f.sent))
	copy(out, f.sent)
	return out
}

func (f *fakePeer) Close(int, string) {
	f.once.Do(func() {
		f.mu.Lock()
		f.closed = true
		f.mu.Unlock()
		close(f.closeCh)
	})
}

func (f *fakePeer) isClosed() bool {
	select {
	case <-f.closeCh:
		return true
	default:
		return false
	}
}

type fakeError string

func (e fakeError) Error() string { return string(e) }

const errFakeClosed = fakeError("fake peer closed")
