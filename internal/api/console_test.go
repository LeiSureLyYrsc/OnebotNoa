package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/hub"
)

func TestConsoleInvokeValidation(t *testing.T) {
	a := newTestAPI(t)
	csrf := loginAdmin(t, a)

	cases := []struct {
		name string
		body string
		want int
	}{
		{"malformed", "{nope", http.StatusBadRequest},
		{"no action", `{}`, http.StatusBadRequest},
		{"params not an object", `{"self_id":"1","action":"send_msg","params":"x"}`, http.StatusBadRequest},
		{"unknown account", `{"self_id":"424242","action":"get_status"}`, http.StatusConflict},
	}
	for _, tc := range cases {
		res, _ := a.write(t, http.MethodPost, "/api/v1/console/invoke", tc.body, csrf)
		if res.StatusCode != tc.want {
			t.Fatalf("%s = %d, want %d", tc.name, res.StatusCode, tc.want)
		}
	}

	// No accounts at all: the debugger says so instead of guessing.
	res, _ := a.write(t, http.MethodPost, "/api/v1/console/invoke", `{"action":"get_status"}`, csrf)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("invoke without any account = %d, want 400", res.StatusCode)
	}
}

func TestConsoleInvokeResolvesSingleBindingAndReplies(t *testing.T) {
	a := newTestAPI(t)
	csrf := loginAdmin(t, a)
	ctx := context.Background()

	a.seedAccount(t, "61001", "console")
	bot := a.seedBot(t, "console-bot", "console-bot-token")
	a.seedBinding(t, bot.Name, "61001", true, "{}")

	// A single binding is enough to resolve the account, but the account is not
	// connected: a clear 409 rather than a hang.
	res, payload := a.write(t, http.MethodPost, "/api/v1/console/invoke",
		fmt.Sprintf(`{"bot_id":%d,"action":"get_status"}`, bot.ID), csrf)
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("invoke against an offline account = %d, want 409 (%v)", res.StatusCode, payload)
	}

	// Attach a live upstream connection through the *real* read loop, so the
	// reply travels the same dispatcher a real implementation would use.
	peer := newConsolePeerForTest("up-1", "61001")
	peerCtx, cancelPeer := context.WithCancel(ctx)
	defer cancelPeer()
	go func() {
		_ = a.hub.HandleUpstream(peerCtx, hub.UpstreamInfo{
			SelfID: "61001", Role: "Universal", Source: "test",
		}, peer)
	}()

	// Wait until the account reports that it can take API calls.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if session, ok := a.hub.Registry().Session("61001"); ok && session.CanSendActions() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// The relay forwards the console action to this peer; answer it like an
	// implementation would and let the relay correlate the echo.
	go func() {
		select {
		case frame := <-peer.outbox:
			var action struct {
				Echo json.RawMessage `json:"echo"`
			}
			_ = json.Unmarshal(frame, &action)
			reply := fmt.Sprintf(`{"status":"ok","retcode":0,"data":{"online":true},"echo":%s}`, action.Echo)
			peer.deliver([]byte(reply))
		case <-peerCtx.Done():
		}
	}()

	res, payload = a.write(t, http.MethodPost, "/api/v1/console/invoke",
		fmt.Sprintf(`{"bot_id":%d,"action":"get_status"}`, bot.ID), csrf)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("invoke = %d (%v)", res.StatusCode, payload)
	}
	if payload["sent"] != true || payload["self_id"] != "61001" {
		t.Fatalf("unexpected console result: %v", payload)
	}
	response, _ := payload["response"].(map[string]any)
	if response == nil || response["status"] != "ok" {
		t.Fatalf("console did not capture the upstream reply: %v", payload)
	}
	if _, ok := payload["elapsed_ms"]; !ok {
		t.Fatalf("console should report the round-trip time: %v", payload)
	}
}

func TestConsoleInvokeAmbiguousAccount(t *testing.T) {
	a := newTestAPI(t)
	csrf := loginAdmin(t, a)

	a.seedAccount(t, "62001", "")
	a.seedAccount(t, "62002", "")
	bot := a.seedBot(t, "many-bindings", "many-bindings-token")
	a.seedBinding(t, bot.Name, "62001", false, "{}")
	a.seedBinding(t, bot.Name, "62002", false, "{}")

	res, _ := a.write(t, http.MethodPost, "/api/v1/console/invoke",
		fmt.Sprintf(`{"bot_id":%d,"action":"get_status"}`, bot.ID), csrf)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("ambiguous account = %d, want 400", res.StatusCode)
	}
}

func TestAuditAndLogsEndpoints(t *testing.T) {
	a := newTestAPI(t)
	csrf := loginAdmin(t, a)

	// Generate one audit entry through a normal management call.
	if res, _ := a.write(t, http.MethodPost, "/api/v1/accounts", `{"self_id":"63001"}`, csrf); res.StatusCode != http.StatusCreated {
		t.Fatalf("seed account = %d", res.StatusCode)
	}

	res, payload := a.do(t, http.MethodGet, "/api/v1/audit?limit=10", "", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("audit = %d", res.StatusCode)
	}
	entries, _ := payload["entries"].([]any)
	if len(entries) == 0 {
		t.Fatalf("audit should contain the account creation: %v", payload)
	}
	if payload["total"] == nil {
		t.Fatalf("audit should report a total: %v", payload)
	}
	first, _ := entries[0].(map[string]any)
	if first["actor"] != "admin" || first["action"] != "account.create" {
		t.Fatalf("unexpected newest audit entry: %v", first)
	}

	// Filtering is applied in the API.
	res, payload = a.do(t, http.MethodGet, "/api/v1/audit?action=account.create", "", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("filtered audit = %d", res.StatusCode)
	}
	for _, raw := range payload["entries"].([]any) {
		entry, _ := raw.(map[string]any)
		if !strings.Contains(fmt.Sprint(entry["action"]), "account.create") {
			t.Fatalf("filter leaked a non-matching entry: %v", entry)
		}
	}

	// The log pane mirrors the live-view ring, newest first.
	a.events.Publish(hub.EventRecord{Kind: hub.EventKindUpstream, SelfID: "63001", PostType: "message"})
	a.events.Publish(hub.EventRecord{Kind: hub.EventKindAction, Bot: "bot-x", Action: "send_msg"})

	res, payload = a.do(t, http.MethodGet, "/api/v1/logs?limit=10", "", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("logs = %d", res.StatusCode)
	}
	logs, _ := payload["logs"].([]any)
	if len(logs) != 2 {
		t.Fatalf("logs = %d, want 2", len(logs))
	}
	newest, _ := logs[0].(map[string]any)
	if newest["kind"] != "action" {
		t.Fatalf("logs must be newest first, got %v", newest)
	}

	res, payload = a.do(t, http.MethodGet, "/api/v1/logs?kind=upstream", "", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("filtered logs = %d", res.StatusCode)
	}
	if logs, _ := payload["logs"].([]any); len(logs) != 1 {
		t.Fatalf("filtered logs = %d, want 1", len(logs))
	}
}

var _ = time.Second
