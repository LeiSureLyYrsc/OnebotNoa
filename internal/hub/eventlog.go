package hub

import (
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"
)

// Event is one entry in the live event feed consumed by the WebUI SSE stream.
type Event struct {
	Time      int64           `json:"time"`
	Direction string          `json:"direction"` // up | down | system
	Account   string          `json:"account,omitempty"`
	Bot       string          `json:"bot,omitempty"`
	Kind      string          `json:"kind"`
	Summary   string          `json:"summary"`
	Raw       json.RawMessage `json:"raw,omitempty"`
}

type EventBus struct {
	mu     sync.Mutex
	ring   []Event
	size   int
	subs   map[int]chan Event
	nextID int
}

func newBus(size int) *EventBus {
	if size <= 0 {
		size = 1000
	}
	return &EventBus{size: size, subs: map[int]chan Event{}}
}

func (b *EventBus) publish(e Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ring = append(b.ring, e)
	if len(b.ring) > b.size {
		b.ring = b.ring[len(b.ring)-b.size:]
	}
	for _, ch := range b.subs {
		select {
		case ch <- e:
		default:
		}
	}
}

func (b *EventBus) Subscribe() (int, chan Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.nextID++
	id := b.nextID
	ch := make(chan Event, 256)
	b.subs[id] = ch
	return id, ch
}

func (b *EventBus) Unsubscribe(id int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if ch, ok := b.subs[id]; ok {
		delete(b.subs, id)
		close(ch)
	}
}

func (b *EventBus) Recent(limit int) []Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	if limit <= 0 || limit > len(b.ring) {
		limit = len(b.ring)
	}
	out := make([]Event, limit)
	copy(out, b.ring[len(b.ring)-limit:])
	return out
}

// metrics holds atomic data-plane counters.
type Metrics struct {
	eventsTotal    atomic.Int64
	actionsTotal   atomic.Int64
	responsesTotal atomic.Int64
	dropped        atomic.Int64
	refused        atomic.Int64
	timeouts       atomic.Int64
	malformed      atomic.Int64
	upstreamConns  atomic.Int64
	botConns       atomic.Int64
	pendingConns   atomic.Int64
}

func newMetrics() *Metrics { return &Metrics{} }

// Snapshot returns a point-in-time copy of all counters.
func (m *Metrics) Snapshot() map[string]int64 {
	return map[string]int64{
		"events_total":    m.eventsTotal.Load(),
		"actions_total":   m.actionsTotal.Load(),
		"responses_total": m.responsesTotal.Load(),
		"dropped_total":   m.dropped.Load(),
		"refused_total":   m.refused.Load(),
		"timeouts_total":  m.timeouts.Load(),
		"malformed_total": m.malformed.Load(),
		"upstream_conns":  m.upstreamConns.Load(),
		"bot_conns":       m.botConns.Load(),
		"pending_conns":   m.pendingConns.Load(),
	}
}

type bucket struct {
	tokens float64
	last   time.Time
}

type bucketSet struct {
	mu    sync.Mutex
	rate  float64
	burst int
	m     map[string]*bucket
}

func newBucketSet(rate float64, burst int) *bucketSet {
	return &bucketSet{rate: rate, burst: burst, m: map[string]*bucket{}}
}

// allow reports whether the key may consume one token now.
func (b *bucketSet) allow(key string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	bk := b.m[key]
	if bk == nil {
		bk = &bucket{tokens: float64(b.burst), last: now}
		b.m[key] = bk
	}
	bk.tokens += now.Sub(bk.last).Seconds() * b.rate
	if bk.tokens > float64(b.burst) {
		bk.tokens = float64(b.burst)
	}
	bk.last = now
	if bk.tokens >= 1 {
		bk.tokens--
		return true
	}
	return false
}
