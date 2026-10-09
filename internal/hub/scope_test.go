package hub

import (
	"testing"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/onebot"
)

func env(json string) onebot.EventEnvelope {
	e, _ := onebot.ParseEvent([]byte(json))
	return e
}

func TestScopeMatch(t *testing.T) {
	groupMsg := env(`{"post_type":"message","message_type":"group","self_id":10001,"user_id":5,"group_id":123}`)
	privateMsg := env(`{"post_type":"message","message_type":"private","self_id":10001,"user_id":5}`)
	notice := env(`{"post_type":"notice","notice_type":"group_upload","self_id":10001,"group_id":123}`)

	if !scopeMatch("{}", groupMsg, "10001") {
		t.Fatal("empty scope should match all")
	}
	if scopeMatch(`{"post_types":["notice"]}`, groupMsg, "10001") {
		t.Fatal("post_types filter failed")
	}
	if !scopeMatch(`{"post_types":["notice"]}`, notice, "10001") {
		t.Fatal("notice should pass notice filter")
	}
	if scopeMatch(`{"include_groups":["999"]}`, groupMsg, "10001") {
		t.Fatal("include_groups should exclude non-listed group")
	}
	if !scopeMatch(`{"include_groups":["123"]}`, groupMsg, "10001") {
		t.Fatal("include_groups should allow listed group")
	}
	if scopeMatch(`{"exclude_groups":["123"]}`, groupMsg, "10001") {
		t.Fatal("exclude_groups should drop listed group")
	}
	selfMsg := env(`{"post_type":"message","message_type":"private","self_id":10001,"user_id":10001}`)
	if scopeMatch(`{"exclude_self":true}`, selfMsg, "10001") {
		t.Fatal("exclude_self should drop self messages")
	}
	if !scopeMatch(`{"exclude_self":true}`, privateMsg, "10001") {
		t.Fatal("exclude_self should keep other users")
	}
}

func TestBucketSet(t *testing.T) {
	b := newBucketSet(1, 2)
	if !b.allow("x") || !b.allow("x") {
		t.Fatal("burst tokens should be allowed")
	}
	if b.allow("x") {
		t.Fatal("third immediate token should be denied")
	}
	if !b.allow("y") {
		t.Fatal("independent key should have its own budget")
	}
}
