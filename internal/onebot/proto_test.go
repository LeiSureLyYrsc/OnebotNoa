package onebot

import (
	"encoding/json"
	"testing"
)

func TestParseRole(t *testing.T) {
	cases := map[string]Role{
		"API": RoleAPI, "api": RoleAPI, " api ": RoleAPI,
		"Event": RoleEvent, "EVENT": RoleEvent,
		"Universal": RoleUniversal, "": RoleUniversal, "nonsense": RoleUniversal,
	}
	for in, want := range cases {
		if got := ParseRole(in); got != want {
			t.Fatalf("ParseRole(%q) = %q, want %q", in, got, want)
		}
	}
	if !RoleUniversal.CanSendActions() || !RoleUniversal.CanReceiveEvents() {
		t.Fatal("Universal must do both")
	}
	if RoleEvent.CanSendActions() || !RoleEvent.CanReceiveEvents() {
		t.Fatal("Event must only receive")
	}
	if !RoleAPI.CanSendActions() || RoleAPI.CanReceiveEvents() {
		t.Fatal("API must only send")
	}
}

func TestParseEventMeta(t *testing.T) {
	raw := []byte(`{"post_type":"message","message_type":"group","sub_type":"normal","self_id":10001000,"user_id":10001,"group_id":20002,"time":1700000000,"message":"hi"}`)
	meta, ok := ParseEventMeta(raw)
	if !ok {
		t.Fatal("expected an event")
	}
	if meta.PostType != "message" || meta.DetailType != "group" || meta.SubType != "normal" {
		t.Fatalf("unexpected meta: %+v", meta)
	}
	// Ids must survive as exact strings, never via float64.
	if meta.SelfID != "10001000" || meta.UserID != "10001" || meta.GroupID != "20002" {
		t.Fatalf("id precision lost: %+v", meta)
	}

	if _, ok := ParseEventMeta([]byte(`{"status":"ok","retcode":0,"echo":"1"}`)); ok {
		t.Fatal("an API result is not an event")
	}
	if _, ok := ParseEventMeta([]byte("not json")); ok {
		t.Fatal("garbage is not an event")
	}

	meta, ok = ParseEventMeta([]byte(`{"post_type":"meta_event","meta_event_type":"lifecycle","sub_type":"connect","self_id":123}`))
	if !ok || meta.DetailType != "lifecycle" || meta.SelfID != "123" {
		t.Fatalf("meta event parsed wrong: %+v ok=%v", meta, ok)
	}
}

func TestExtractSelfID(t *testing.T) {
	if got := ExtractSelfID([]byte(`{"post_type":"meta_event","self_id":123456789012345}`)); got != "123456789012345" {
		t.Fatalf("ExtractSelfID = %q", got)
	}
	if got := ExtractSelfID([]byte(`{"post_type":"message"}`)); got != "" {
		t.Fatalf("missing self_id must yield empty, got %q", got)
	}
	if got := ExtractSelfID([]byte("{broken")); got != "" {
		t.Fatalf("invalid json must yield empty, got %q", got)
	}
}

func TestActionAndResponseHelpers(t *testing.T) {
	if BaseAction("send_msg_async") != "send_msg" {
		t.Fatal("_async suffix not stripped")
	}
	if BaseAction("send_msg_rate_limited") != "send_msg" {
		t.Fatal("_rate_limited suffix not stripped")
	}
	if BaseAction("send_msg") != "send_msg" {
		t.Fatal("plain action changed")
	}

	echo := json.RawMessage(`{"nested":true}`)
	fail := FailureResponse(echo, RetForbidden, "not bound")
	var frame ResponseFrame
	if err := json.Unmarshal(fail, &frame); err != nil {
		t.Fatal(err)
	}
	if frame.Status != "failed" || frame.Retcode != RetForbidden || frame.Wording != "not bound" {
		t.Fatalf("unexpected failure frame: %+v", frame)
	}
	if string(frame.Echo) != string(echo) {
		t.Fatalf("echo not preserved: %s", frame.Echo)
	}
	if string(frame.Data) != "null" {
		t.Fatalf("data = %s, want null", frame.Data)
	}

	ok := SuccessResponse(nil, json.RawMessage(`{"user_id":1}`))
	var okFrame ResponseFrame
	if err := json.Unmarshal(ok, &okFrame); err != nil {
		t.Fatal(err)
	}
	if okFrame.Status != "ok" || okFrame.Retcode != 0 || string(okFrame.Data) != `{"user_id":1}` {
		t.Fatalf("unexpected success frame: %+v", okFrame)
	}
	if len(okFrame.Echo) != 0 {
		t.Fatalf("absent echo must be omitted, got %s", okFrame.Echo)
	}

	if FrameType([]byte(`{"type":"stream","echo":"1"}`)) != "stream" {
		t.Fatal("FrameType did not read type")
	}
	if IsActionResult([]byte(`{"type":"stream","echo":"1"}`)) {
		t.Fatal("stream frame without status is not a terminal result")
	}
	if !IsActionResult([]byte(`{"status":"ok","retcode":0}`)) {
		t.Fatal("frame with status is a result")
	}
}
