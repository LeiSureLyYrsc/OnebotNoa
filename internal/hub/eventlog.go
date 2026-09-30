package hub

import (
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/onebot"
)

// Event kinds shown in the live stream.
const (
	EventKindAccount  = "account"
	EventKindUpstream = "upstream"
	EventKindAction   = "action"
	EventKindResponse = "response"
	EventKindPolicy   = "policy"
)

// defaultRawLimit caps how much of a frame is kept in the ring/appendix so a
// single huge frame cannot bloat memory.
const defaultRawLimit = 4096

// EventRecord is one entry of the live view. Raw holds the frame bytes (possibly
// truncated, see Bytes for the real size) so the WebUI can show exactly what
// crossed the relay.
type EventRecord struct {
	Seq      uint64          `json:"seq"`
	At       time.Time       `json:"at"`
	Kind     string          `json:"kind"`
	SelfID   string          `json:"self_id,omitempty"`
	Bot      string          `json:"bot,omitempty"`
	Role     string          `json:"role,omitempty"`
	Action   string          `json:"action,omitempty"`
	PostType string          `json:"post_type,omitempty"`
	GroupID  string          `json:"group_id,omitempty"`
	UserID   string          `json:"user_id,omitempty"`
	Retcode  *int            `json:"retcode,omitempty"`
	Note     string          `json:"note,omitempty"`
	Raw      json.RawMessage `json:"raw,omitempty"`
	Bytes    int             `json:"bytes"`
	Truncated bool           `json:"truncated,omitempty"`
}

// EventFilter narrows the stream.
type EventFilter struct {
	SelfID   string
	Bot      string
	Kind     string
	PostType string
	GroupID  string
	Query    string
}

// Matches reports whether a record passes the filter.
func (f EventFilter) Matches(rec EventRecord) bool {
	if f.SelfID != "" && rec.SelfID != f.SelfID {
		return false
	}
	if f.Bot != "" && rec.Bot != f.Bot {
		return false
	}
	if f.Kind != "" && rec.Kind != f.Kind {
		return false
	}
	if f.PostType != "" && rec.PostType != f.PostType {
		return false
	}
	if f.GroupID != "" && rec.GroupID != f.GroupID {
		return false
	}
	if f.Query != "" {
		needle := strings.ToLower(f.Query)
		haystack := strings.ToLower(rec.Note + " " + rec.Action + " " + rec.PostType + " " + string(rec.Raw))
		if !strings.Contains(haystack, needle) {
			return false
		}
	}
	return true
}

// subscriber is one live SSE consumer.
type subscriber struct {
	id     uint64
	filter EventFilter
	ch     chan EventRecord
}

// EventLog keeps a bounded ring of recent activity and fans new records out to
// subscribers. Publishing never blocks: a subscriber that cannot keep up loses
// records (counted in Dropped) instead of stalling the relay.
type EventLog struct {
	mu    sync.RWMutex
	ring  []EventRecord
	next  int
	count int
	size  int
	subs  map[uint64]*subscriber
	subID uint64

	seq      atomic.Uint64
	dropped  atomic.Uint64
	rawLimit int
}

// NewEventLog builds a ring of the given size (defaults to 2000).
func NewEventLog(size int) *EventLog {
	if size <= 0 {
		size = 2000
	}
	return &EventLog{
		ring:     make([]EventRecord, size),
		size:     size,
		subs:     map[uint64]*subscriber{},
		rawLimit: defaultRawLimit,
	}
}

// Publish stores a record and hands it to every matching subscriber.
func (l *EventLog) Publish(rec EventRecord) EventRecord {
	rec.Seq = l.seq.Add(1)
	if rec.At.IsZero() {
		rec.At = time.Now()
	}
	rec.Bytes = len(rec.Raw)
	if len(rec.Raw) > l.rawLimit {
		rec.Raw = append(json.RawMessage(nil), rec.Raw[:l.rawLimit]...)
		rec.Truncated = true
	} else if len(rec.Raw) > 0 {
		rec.Raw = append(json.RawMessage(nil), rec.Raw...)
	}

	l.mu.Lock()
	l.ring[l.next] = rec
	l.next = (l.next + 1) % l.size
	if l.count < l.size {
		l.count++
	}
	for _, sub := range l.subs {
		if !sub.filter.Matches(rec) {
			continue
		}
		select {
		case sub.ch <- rec:
		default:
			l.dropped.Add(1)
		}
	}
	l.mu.Unlock()
	return rec
}

// Recent returns the newest records (oldest first) that pass the filter.
func (l *EventLog) Recent(limit int, filter EventFilter) []EventRecord {
	if limit <= 0 || limit > l.size {
		limit = l.size
	}
	l.mu.RLock()
	defer l.mu.RUnlock()

	out := make([]EventRecord, 0, limit)
	total := l.count
	for i := 0; i < total && len(out) < limit; i++ {
		idx := (l.next - 1 - i + l.size*2) % l.size
		rec := l.ring[idx]
		if rec.Seq == 0 || !filter.Matches(rec) {
			continue
		}
		out = append(out, rec)
	}
	// newest first
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// Subscribe registers a live consumer. The returned cancel function must be
// called when the consumer goes away.
func (l *EventLog) Subscribe(filter EventFilter, buffer int) (<-chan EventRecord, func()) {
	if buffer <= 0 {
		buffer = 256
	}
	l.mu.Lock()
	l.subID++
	sub := &subscriber{id: l.subID, filter: filter, ch: make(chan EventRecord, buffer)}
	l.subs[sub.id] = sub
	l.mu.Unlock()

	cancel := func() {
		l.mu.Lock()
		if _, ok := l.subs[sub.id]; ok {
			delete(l.subs, sub.id)
			close(sub.ch)
		}
		l.mu.Unlock()
	}
	return sub.ch, cancel
}

// Subscribers reports the current number of live consumers.
func (l *EventLog) Subscribers() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return len(l.subs)
}

// Dropped reports how many records were dropped for slow consumers.
func (l *EventLog) Dropped() uint64 { return l.dropped.Load() }

// Len reports how many records the ring currently holds.
func (l *EventLog) Len() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.count
}

// ------------------------------------------------------------- hub integration

// AccountChanged implements Observer.
func (l *EventLog) AccountChanged(ev AccountEvent) {
	note := "连接建立"
	switch ev.Type {
	case EventPeerDisconnected:
		note = "连接断开"
	}
	l.Publish(EventRecord{
		Kind:   EventKindAccount,
		SelfID: ev.SelfID,
		Role:   string(ev.Role),
		Note:   note + "（" + ev.State + "）",
	})
}

// UpstreamFrame implements Observer.
func (l *EventLog) UpstreamFrame(selfID string, role onebot.Role, raw []byte) {
	rec := EventRecord{Kind: EventKindUpstream, SelfID: selfID, Role: string(role), Raw: raw}
	if meta, ok := onebot.ParseEventMeta(raw); ok {
		rec.PostType = meta.PostType
		rec.GroupID = meta.GroupID
		rec.UserID = meta.UserID
	} else if onebot.IsActionResult(raw) {
		rec.Kind = EventKindResponse
		rec.Note = "上游响应"
	}
	l.Publish(rec)
}

// BotAction records an action forwarded from a Bot to a QQ instance.
func (l *EventLog) BotAction(bot, selfID string, raw []byte) {
	rec := EventRecord{Kind: EventKindAction, Bot: bot, SelfID: selfID, Raw: raw}
	var probe struct {
		Action string `json:"action"`
	}
	if json.Unmarshal(raw, &probe) == nil {
		rec.Action = probe.Action
	}
	l.Publish(rec)
}

// BotResponse records a reply on its way back to a Bot.
func (l *EventLog) BotResponse(bot string, raw []byte) {
	rec := EventRecord{Kind: EventKindResponse, Bot: bot, Raw: raw}
	var probe struct {
		Status  string `json:"status"`
		Retcode int    `json:"retcode"`
	}
	if json.Unmarshal(raw, &probe) == nil {
		code := probe.Retcode
		rec.Retcode = &code
		if probe.Status != "" {
			rec.Note = probe.Status
		}
	}
	l.Publish(rec)
}

// PolicyRejected records a refused action.
func (l *EventLog) PolicyRejected(bot, action string, retcode int, reason string) {
	code := retcode
	l.Publish(EventRecord{
		Kind:    EventKindPolicy,
		Bot:     bot,
		Action:  action,
		Retcode: &code,
		Note:    reason,
	})
}
