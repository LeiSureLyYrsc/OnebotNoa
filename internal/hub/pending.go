package hub

import (
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/onebot"
)

// ErrPendingLimit is returned when a Bot has too many actions in flight.
var ErrPendingLimit = errors.New("hub: too many actions in flight")

// pendingAction correlates one outbound action with its reply.
type pendingAction struct {
	conn      Peer
	botID     int64
	botName   string
	selfID    string
	origEcho  json.RawMessage
	hasEcho   bool
	createdAt time.Time
	timer     *time.Timer

	// streaming bookkeeping (SnowLuma-style multi-frame replies)
	streaming bool
	frames    int
}

// pendingTable maps rewritten echo values to in-flight actions. Keys are unique
// per bot connection, so two Bots using the same echo value never collide.
type pendingTable struct {
	mu       sync.Mutex
	items    map[string]*pendingAction
	perConn  map[string]int
	global   int
	maxPer   int
	maxTotal int

	// timeout is invoked with the expired action so the caller can answer the
	// Bot with a failure response.
	timeout func(*pendingAction)
}

func newPendingTable(maxPerConn, maxGlobal int, onTimeout func(*pendingAction)) *pendingTable {
	if maxPerConn <= 0 {
		maxPerConn = 512
	}
	if maxGlobal <= 0 {
		maxGlobal = 8192
	}
	return &pendingTable{
		items:    map[string]*pendingAction{},
		perConn:  map[string]int{},
		maxPer:   maxPerConn,
		maxTotal: maxGlobal,
		timeout:  onTimeout,
	}
}

// Add registers a new in-flight action, enforcing both caps and arming its
// timeout. The timer is created while holding the table lock: the timeout
// goroutine and the reply path must never touch an entry's fields unsynchronised.
func (t *pendingTable) Add(key string, pa *pendingAction, timeout time.Duration) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.global >= t.maxTotal || t.perConn[pa.conn.ID()] >= t.maxPer {
		return ErrPendingLimit
	}
	if timeout > 0 {
		pa.timer = time.AfterFunc(timeout, func() { t.expire(key) })
	}
	t.items[key] = pa
	t.perConn[pa.conn.ID()]++
	t.global++
	return nil
}

// Touch records one intermediate streaming frame and re-arms the idle timeout.
// It reports whether the entry is still tracked, and whether the absolute
// duration cap has been exceeded (the caller then finishes the stream).
func (t *pendingTable) Touch(key string, idle, maxTotal time.Duration) (exceeded, ok bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	pa, ok := t.items[key]
	if !ok {
		return false, false
	}
	pa.frames++
	pa.streaming = true
	if maxTotal > 0 && time.Since(pa.createdAt) > maxTotal {
		return true, true
	}
	if pa.timer != nil {
		pa.timer.Stop()
		pa.timer = nil
	}
	if idle > 0 {
		pa.timer = time.AfterFunc(idle, func() { t.expire(key) })
	}
	return false, true
}

// Frames reports how many streaming frames an entry has delivered.
func (t *pendingTable) Frames(key string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	if pa, ok := t.items[key]; ok {
		return pa.frames
	}
	return 0
}

// Peek looks up an action without removing it (streaming replies use this).
func (t *pendingTable) Peek(key string) (*pendingAction, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	pa, ok := t.items[key]
	return pa, ok
}

// Take removes and returns an action.
func (t *pendingTable) Take(key string) (*pendingAction, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	pa, ok := t.items[key]
	if !ok {
		return nil, false
	}
	t.removeLocked(key, pa)
	return pa, true
}

// RemoveByConn drops every in-flight action of a connection (used on disconnect).
func (t *pendingTable) RemoveByConn(connID string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	removed := 0
	for key, pa := range t.items {
		if pa.conn.ID() == connID {
			t.removeLocked(key, pa)
			removed++
		}
	}
	return removed
}

func (t *pendingTable) removeLocked(key string, pa *pendingAction) {
	if pa.timer != nil {
		pa.timer.Stop()
	}
	delete(t.items, key)
	connID := pa.conn.ID()
	if n := t.perConn[connID] - 1; n > 0 {
		t.perConn[connID] = n
	} else {
		delete(t.perConn, connID)
	}
	if t.global > 0 {
		t.global--
	}
}

// Count reports the number of in-flight actions.
func (t *pendingTable) Count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.global
}

// CountForConn reports the in-flight actions of one connection.
func (t *pendingTable) CountForConn(connID string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.perConn[connID]
}

// expire is called by the timer installed for an action.
func (t *pendingTable) expire(key string) {
	pa, ok := t.Take(key)
	if !ok || t.timeout == nil {
		return
	}
	t.timeout(pa)
}

// responseEchoKey extracts the rewritten echo (always a JSON string) from a
// reply frame.
func responseEchoKey(raw []byte) (string, bool) {
	var probe struct {
		Echo json.RawMessage `json:"echo"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil || len(probe.Echo) == 0 {
		return "", false
	}
	var key string
	if err := json.Unmarshal(probe.Echo, &key); err != nil {
		return "", false
	}
	return key, true
}

// replaceEcho rewrites (or removes) the echo field of a frame while leaving
// every other value byte-identical.
func replaceEcho(raw []byte, echo json.RawMessage, hasEcho bool) ([]byte, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	if hasEcho && len(echo) > 0 {
		fields["echo"] = echo
	} else {
		delete(fields, "echo")
	}
	return json.Marshal(fields)
}

// isStreamFrame reports whether a reply is an intermediate streaming frame
// (SnowLuma extension) rather than the terminal response.
func isStreamFrame(raw []byte) bool {
	if onebot.FrameType(raw) != "stream" {
		return false
	}
	return !onebot.IsActionResult(raw)
}
