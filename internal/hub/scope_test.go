package hub

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/onebot"
)

func TestScopeMatch(t *testing.T) {
	groupMsg := onebot.EventMeta{PostType: "message", DetailType: "group", SelfID: "1", UserID: "100", GroupID: "200"}
	privateMsg := onebot.EventMeta{PostType: "message", DetailType: "private", SelfID: "1", UserID: "100"}
	selfMsg := onebot.EventMeta{PostType: "message", DetailType: "group", SelfID: "1", UserID: "1", GroupID: "200"}
	heartbeat := onebot.EventMeta{PostType: "meta_event", DetailType: "heartbeat", SelfID: "1"}

	cases := []struct {
		name   string
		scope  Scope
		meta   onebot.EventMeta
		global string
		want   bool
	}{
		{"empty scope matches", Scope{}, groupMsg, "synthetic", true},
		{"post type hit", Scope{PostTypes: []string{"message"}}, groupMsg, "synthetic", true},
		{"post type miss", Scope{PostTypes: []string{"notice"}}, groupMsg, "synthetic", false},
		{"group included", Scope{IncludeGroups: []string{"200"}}, groupMsg, "synthetic", true},
		{"group not included", Scope{IncludeGroups: []string{"201"}}, groupMsg, "synthetic", false},
		{"group excluded", Scope{ExcludeGroups: []string{"200"}}, groupMsg, "synthetic", false},
		{"user included", Scope{IncludeUsers: []string{"100"}}, privateMsg, "synthetic", true},
		{"user not included", Scope{IncludeUsers: []string{"101"}}, privateMsg, "synthetic", false},
		{"user excluded", Scope{ExcludeUsers: []string{"100"}}, privateMsg, "synthetic", false},
		{"exclude self", Scope{ExcludeSelf: true}, selfMsg, "synthetic", false},
		{"exclude self keeps others", Scope{ExcludeSelf: true}, groupMsg, "synthetic", true},
		{"meta synthetic never forwarded", Scope{}, heartbeat, "synthetic", false},
		{"meta passthrough when global", Scope{}, heartbeat, "passthrough", true},
		{"meta passthrough per binding", Scope{MetaEvents: "passthrough"}, heartbeat, "synthetic", true},
		{"meta drop", Scope{MetaEvents: "drop"}, heartbeat, "passthrough", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.scope.Match(tc.meta, tc.global); got != tc.want {
				t.Fatalf("Match = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestParseScope(t *testing.T) {
	s, err := ParseScope(json.RawMessage(`{"post_types":["message"],"include_groups":["1"],"exclude_self":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(s.PostTypes) != 1 || !s.ExcludeSelf || len(s.IncludeGroups) != 1 {
		t.Fatalf("unexpected scope: %+v", s)
	}

	if _, err := ParseScope(json.RawMessage(`{"post_types":`)); err == nil {
		t.Fatal("invalid scope must be rejected")
	}

	empty, err := ParseScope(nil)
	if err != nil || len(empty.PostTypes) != 0 {
		t.Fatalf("empty scope = %+v, %v", empty, err)
	}
}

func TestReplaceEchoAndStreamDetection(t *testing.T) {
	raw := []byte(`{"action":"send_msg","params":{"user_id":1},"echo":"bot-1"}`)
	out, err := replaceEcho(raw, json.RawMessage(`{"nested":{"x":1}}`), true)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(out, &fields); err != nil {
		t.Fatal(err)
	}
	if string(fields["echo"]) != `{"nested":{"x":1}}` {
		t.Fatalf("echo not replaced: %s", fields["echo"])
	}
	if string(fields["params"]) != `{"user_id":1}` {
		t.Fatalf("params changed: %s", fields["params"])
	}

	// Removing the echo must delete the field entirely.
	out, err = replaceEcho(raw, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	// Decode into a fresh map: decoding into the previous one would only merge
	// keys and hide a removed field.
	fields = map[string]json.RawMessage{}
	if err := json.Unmarshal(out, &fields); err != nil {
		t.Fatal(err)
	}
	if _, ok := fields["echo"]; ok {
		t.Fatal("echo must be removed when the Bot did not send one")
	}

	// Large integers must not be mangled by the echo rewrite.
	big := []byte(`{"action":"get_msg","params":{"message_id":123456789012345678},"echo":1}`)
	out, err = replaceEcho(big, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(out) || !contains(out, "123456789012345678") {
		t.Fatalf("large integer was mangled: %s", out)
	}

	if key, ok := responseEchoKey([]byte(`{"status":"ok","echo":"hub@1:2"}`)); !ok || key != "hub@1:2" {
		t.Fatalf("responseEchoKey = %q, %v", key, ok)
	}
	if _, ok := responseEchoKey([]byte(`{"status":"ok"}`)); ok {
		t.Fatal("absent echo must not produce a key")
	}
	if _, ok := responseEchoKey([]byte(`{"status":"ok","echo":12}`)); ok {
		t.Fatal("non-string echo must not produce a key")
	}

	if !isStreamFrame([]byte(`{"type":"stream","stream_id":"abc","echo":"hub@1:2"}`)) {
		t.Fatal("stream frame not detected")
	}
	if isStreamFrame([]byte(`{"type":"stream","status":"ok","echo":"hub@1:2"}`)) {
		t.Fatal("a frame with status is terminal, not intermediate")
	}
	if isStreamFrame([]byte(`{"status":"ok","echo":"hub@1:2"}`)) {
		t.Fatal("plain response is not a stream frame")
	}
}

func contains(haystack []byte, needle string) bool {
	return len(haystack) > 0 && len(needle) > 0 && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if string(haystack[i:i+len(needle)]) == needle {
				return true
			}
		}
		return false
	})()
}

func TestPendingTableCapsAndCleanup(t *testing.T) {
	fake := &fakePeer{id: "conn-1"}
	tbl := newPendingTable(2, 3, nil)

	add := func(key string, conn Peer) error {
		return tbl.Add(key, &pendingAction{conn: conn, createdAt: time.Now()}, time.Minute)
	}
	if err := add("a", fake); err != nil {
		t.Fatal(err)
	}
	if err := add("b", fake); err != nil {
		t.Fatal(err)
	}
	if err := add("c", fake); err != ErrPendingLimit {
		t.Fatalf("per-connection cap not enforced: %v", err)
	}

	other := &fakePeer{id: "conn-2"}
	if err := add("c", other); err != nil {
		t.Fatal(err)
	}
	if err := add("d", other); err != ErrPendingLimit {
		t.Fatalf("global cap not enforced: %v", err)
	}

	if _, ok := tbl.Take("a"); !ok {
		t.Fatal("Take(a) failed")
	}
	if err := add("e", fake); err != nil {
		t.Fatalf("capacity after Take: %v", err)
	}
	// conn-1 holds b and e; conn-2 held c only.
	if n := tbl.RemoveByConn("conn-2"); n != 1 {
		t.Fatalf("RemoveByConn = %d, want 1", n)
	}
	if tbl.Count() != 2 {
		t.Fatalf("count = %d, want 2", tbl.Count())
	}
	if n := tbl.CountForConn("conn-1"); n != 2 {
		t.Fatalf("CountForConn = %d, want 2", n)
	}
	if n := tbl.CountForConn("conn-2"); n != 0 {
		t.Fatalf("CountForConn(conn-2) = %d, want 0", n)
	}

	// Streaming bookkeeping goes through the table so the timeout goroutine and
	// the reply path never race on an entry.
	if exceeded, ok := tbl.Touch("missing", time.Second, time.Minute); ok || exceeded {
		t.Fatal("Touch on an unknown key must report nothing")
	}
	if exceeded, ok := tbl.Touch("b", time.Second, time.Minute); !ok || exceeded {
		t.Fatalf("Touch(b) = exceeded %v ok %v, want false, true", exceeded, ok)
	}
	if n := tbl.Frames("b"); n != 1 {
		t.Fatalf("Frames(b) = %d, want 1", n)
	}
	if exceeded, ok := tbl.Touch("b", time.Second, 0); !ok || exceeded {
		t.Fatal("a zero max duration must not count as exceeded")
	}
	// A cap that has already elapsed must be reported as exceeded. Use a fresh
	// table (the one above is at its global cap) and an explicitly old entry,
	// because the Windows clock is too coarse for "just added".
	fresh := newPendingTable(2, 3, nil)
	if err := fresh.Add("aged", &pendingAction{conn: fake, createdAt: time.Now().Add(-time.Hour)}, time.Minute); err != nil {
		t.Fatalf("add aged entry: %v", err)
	}
	if exceeded, ok := fresh.Touch("aged", time.Second, time.Minute); !ok || !exceeded {
		t.Fatalf("elapsed cap: exceeded %v ok %v, want true, true", exceeded, ok)
	}
}
