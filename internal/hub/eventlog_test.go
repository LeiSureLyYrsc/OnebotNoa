package hub

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/onebot"
)

func TestEventLogRingAndBackfill(t *testing.T) {
	log := NewEventLog(3)
	for i := 0; i < 5; i++ {
		log.Publish(EventRecord{Kind: EventKindUpstream, SelfID: "10001", Note: "n"})
	}
	if log.Len() != 3 {
		t.Fatalf("ring length = %d, want 3", log.Len())
	}
	recent := log.Recent(10, EventFilter{})
	if len(recent) != 3 {
		t.Fatalf("recent = %d, want 3 (bounded by the ring)", len(recent))
	}
	// Recent() documents oldest-first ordering, newest records kept.
	if recent[0].Seq != 3 || recent[2].Seq != 5 {
		t.Fatalf("unexpected order: %d .. %d", recent[0].Seq, recent[2].Seq)
	}
	// A limit takes the newest entries, still oldest-first inside the window.
	if tail := log.Recent(2, EventFilter{}); len(tail) != 2 || tail[0].Seq != 4 || tail[1].Seq != 5 {
		t.Fatalf("limit window = %+v", tail)
	}
	if recent[0].At.IsZero() {
		t.Fatal("timestamp must be filled in")
	}
}

func TestEventLogFilters(t *testing.T) {
	log := NewEventLog(16)
	log.Publish(EventRecord{Kind: EventKindUpstream, SelfID: "1", PostType: "message", GroupID: "9", Raw: json.RawMessage(`{"message":"hello world"}`)})
	log.Publish(EventRecord{Kind: EventKindAction, Bot: "bot-a", SelfID: "1", Action: "send_msg"})
	log.Publish(EventRecord{Kind: EventKindUpstream, SelfID: "2", PostType: "notice", GroupID: "8"})

	cases := []struct {
		name   string
		filter EventFilter
		want   int
	}{
		{"no filter", EventFilter{}, 3},
		{"by account", EventFilter{SelfID: "2"}, 1},
		{"by bot", EventFilter{Bot: "bot-a"}, 1},
		{"by kind", EventFilter{Kind: EventKindUpstream}, 2},
		{"by post type", EventFilter{PostType: "message"}, 1},
		{"by group", EventFilter{GroupID: "9"}, 1},
		{"by query in raw", EventFilter{Query: "hello"}, 1},
		{"by query in action", EventFilter{Query: "SEND_MSG"}, 1},
		{"no match", EventFilter{Query: "nope"}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := len(log.Recent(50, tc.filter)); got != tc.want {
				t.Fatalf("matches = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestEventLogFanoutAndCancel(t *testing.T) {
	log := NewEventLog(8)
	ch, cancel := log.Subscribe(EventFilter{Kind: EventKindUpstream}, 4)
	if log.Subscribers() != 1 {
		t.Fatalf("subscribers = %d, want 1", log.Subscribers())
	}

	log.Publish(EventRecord{Kind: EventKindAction, Bot: "b"})      // filtered out
	log.Publish(EventRecord{Kind: EventKindUpstream, SelfID: "1"}) // delivered

	select {
	case rec := <-ch:
		if rec.Kind != EventKindUpstream || rec.SelfID != "1" {
			t.Fatalf("unexpected record: %+v", rec)
		}
	case <-time.After(time.Second):
		t.Fatal("matching record was not delivered")
	}

	select {
	case rec := <-ch:
		t.Fatalf("filtered record leaked: %+v", rec)
	default:
	}

	cancel()
	if log.Subscribers() != 0 {
		t.Fatalf("subscribers after cancel = %d, want 0", log.Subscribers())
	}
}

func TestEventLogSlowSubscriberDoesNotBlock(t *testing.T) {
	log := NewEventLog(8)
	ch, cancel := log.Subscribe(EventFilter{}, 1)
	defer cancel()

	done := make(chan struct{})
	go func() {
		for i := 0; i < 200; i++ {
			log.Publish(EventRecord{Kind: EventKindUpstream, SelfID: "1"})
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("publishing blocked on a slow subscriber")
	}
	if log.Dropped() == 0 {
		t.Fatal("expected drops for an unread subscriber")
	}
	<-ch // the buffered record is still available
}

func TestEventLogTruncatesHugeFrames(t *testing.T) {
	log := NewEventLog(4)
	big := make([]byte, defaultRawLimit*2)
	for i := range big {
		big[i] = 'x'
	}
	rec := log.Publish(EventRecord{Kind: EventKindUpstream, SelfID: "1", Raw: big})
	if !rec.Truncated {
		t.Fatal("record should be marked truncated")
	}
	if len(rec.Raw) != defaultRawLimit {
		t.Fatalf("raw length = %d, want %d", len(rec.Raw), defaultRawLimit)
	}
	if rec.Bytes != len(big) {
		t.Fatalf("bytes = %d, want %d (full size must be kept)", rec.Bytes, len(big))
	}
}

func TestEventLogObserverAdapters(t *testing.T) {
	log := NewEventLog(16)
	log.AccountChanged(AccountEvent{Type: EventPeerConnected, SelfID: "42", Role: onebot.RoleUniversal, State: "online"})
	log.UpstreamFrame("42", onebot.RoleEvent, []byte(`{"post_type":"message","message_type":"group","self_id":42,"group_id":7,"user_id":9}`))
	log.BotAction("bot-a", "42", []byte(`{"action":"send_msg","params":{"message":"x"},"echo":"hub@1:1"}`))
	log.BotResponse("bot-a", []byte(`{"status":"ok","retcode":0,"echo":"hub@1:1"}`))
	log.PolicyRejected("bot-a", "set_group_ban", onebot.RetForbidden, "action not allowed")

	all := log.Recent(10, EventFilter{})
	if len(all) != 5 {
		t.Fatalf("records = %d, want 5", len(all))
	}
	byKind := map[string]EventRecord{}
	for _, rec := range all {
		byKind[rec.Kind] = rec
	}
	if rec := byKind[EventKindAccount]; rec.SelfID != "42" || rec.Note == "" {
		t.Fatalf("account record incomplete: %+v", rec)
	}
	if rec := byKind[EventKindUpstream]; rec.PostType != "message" || rec.GroupID != "7" || rec.UserID != "9" {
		t.Fatalf("upstream record missing metadata: %+v", rec)
	}
	if rec := byKind[EventKindAction]; rec.Action != "send_msg" || rec.Bot != "bot-a" {
		t.Fatalf("action record incomplete: %+v", rec)
	}
	if rec := byKind[EventKindResponse]; rec.Retcode == nil || *rec.Retcode != 0 {
		t.Fatalf("response record missing retcode: %+v", rec)
	}
	if rec := byKind[EventKindPolicy]; rec.Retcode == nil || *rec.Retcode != onebot.RetForbidden || rec.Note == "" {
		t.Fatalf("policy record incomplete: %+v", rec)
	}
}

func TestEventLogConcurrentPublish(t *testing.T) {
	log := NewEventLog(64)
	ch, cancel := log.Subscribe(EventFilter{}, 1024)
	defer cancel()

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				log.Publish(EventRecord{Kind: EventKindUpstream, SelfID: "1"})
			}
		}()
	}
	drained := make(chan int)
	go func() {
		n := 0
		for {
			select {
			case <-ch:
				n++
				if n == 400 {
					drained <- n
					return
				}
			case <-time.After(2 * time.Second):
				drained <- n
				return
			}
		}
	}()
	wg.Wait()
	if n := <-drained; n != 400 {
		t.Fatalf("delivered %d records, want 400", n)
	}
	if log.Len() != 64 {
		t.Fatalf("ring length = %d, want 64", log.Len())
	}
}
