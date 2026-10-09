package onebot

import (
	"encoding/json"
	"testing"
)

func TestRawID(t *testing.T) {
	cases := map[string]string{
		`12345`:   "12345",
		`"67890"`: "67890",
		`null`:    "",
		`  `:      "",
		`"abc"`:   "abc",
	}
	for in, want := range cases {
		if got := RawID(json.RawMessage(in)); got != want {
			t.Errorf("RawID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseEvent(t *testing.T) {
	raw := []byte(`{"post_type":"message","message_type":"group","self_id":10001,"group_id":"123"}`)
	env, ok := ParseEvent(raw)
	if !ok {
		t.Fatal("expected event")
	}
	if env.PostType != PostMessage || env.MessageType != "group" {
		t.Fatalf("unexpected envelope: %+v", env)
	}
	if RawID(env.SelfID) != "10001" || RawID(env.GroupID) != "123" {
		t.Fatalf("unexpected ids: %s / %s", RawID(env.SelfID), RawID(env.GroupID))
	}
	if _, ok := ParseEvent([]byte(`{"foo":1}`)); ok {
		t.Fatal("non-event should not parse")
	}
}

func TestErrorResponseEcho(t *testing.T) {
	out := ErrorResponse(json.RawMessage(`"abc"`), RetNotFound, "nope")
	var resp map[string]any
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatal(err)
	}
	if resp["echo"] != "abc" || resp["status"] != "failed" {
		t.Fatalf("unexpected response: %v", resp)
	}
}
