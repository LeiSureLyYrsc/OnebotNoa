package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/transport"
)

// recordingListenerRuntime is a ListenerRuntime that records reloads.
type recordingListenerRuntime struct {
	reloads int
	states  []transport.ListenerState
}

func (r *recordingListenerRuntime) States() []transport.ListenerState { return r.states }
func (r *recordingListenerRuntime) Reload(context.Context)            { r.reloads++ }

// TestListenerAPIReloadsRuntime pins the wiring: a listener created through the
// API must reach the runtime manager, and the response must report live state.
func TestListenerAPIReloadsRuntime(t *testing.T) {
	a := newTestAPI(t)
	csrf := loginAdmin(t, a)
	account := a.seedAccount(t, "94001", "listener-api")

	// Without a runtime manager the API still works and reports "pending".
	res, payload := a.write(t, http.MethodPost, "/api/v1/listeners",
		fmt.Sprintf(`{"name":"api-listener","kind":"upstream_listen","bind_addr":"127.0.0.1:0","path":"/onebot/v11/ws","account_self_id":%q}`, account.SelfID), csrf)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create listener = %d (%v)", res.StatusCode, payload)
	}
	created := payload["listener"].(map[string]any)
	if created["runtime"] != "pending" {
		t.Fatalf("without a runtime manager the state should be pending: %v", created)
	}

	// Install a runtime manager and confirm the API reloads it on every change.
	manager := &recordingListenerRuntime{}
	a.srv.SetListenerRuntime(manager)
	res, payload = a.write(t, http.MethodPost, "/api/v1/listeners",
		fmt.Sprintf(`{"name":"api-listener-2","kind":"upstream_listen","bind_addr":"127.0.0.1:0","path":"/onebot/v11/ws2","account_id":%d}`, account.ID), csrf)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create with runtime = %d (%v)", res.StatusCode, payload)
	}
	if manager.reloads == 0 {
		t.Fatal("creating a listener must hot-apply the runtime")
	}

	// The list endpoint surfaces the live state the manager reports.
	manager.states = []transport.ListenerState{{
		Name: "api-listener-2", Kind: "upstream_listen", Addr: "127.0.0.1:9999",
		State: "listening", URL: "ws://127.0.0.1:9999/onebot/v11/ws", Source: "database", Enabled: true,
	}}
	res, payload = a.do(t, http.MethodGet, "/api/v1/listeners", "", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("list listeners = %d", res.StatusCode)
	}
	listeners, _ := payload["listeners"].([]any)
	found := false
	for _, raw := range listeners {
		entry, _ := raw.(map[string]any)
		if entry["name"] != "api-listener-2" {
			continue
		}
		found = true
		if entry["runtime"] != "listening" {
			t.Fatalf("runtime state not surfaced: %v", entry)
		}
		if !strings.Contains(fmt.Sprint(entry["url"]), "9999") {
			t.Fatalf("listener URL not surfaced: %v", entry)
		}
	}
	if !found {
		t.Fatalf("listener missing from the response: %v", payload)
	}

	// A rejected update must not touch the runtime.
	before := manager.reloads
	res, _ = a.write(t, http.MethodPatch, "/api/v1/listeners/999999", `{}`, csrf)
	if res.StatusCode == http.StatusOK {
		t.Fatal("patching a non-existent listener must not succeed")
	}
	if manager.reloads != before {
		t.Fatalf("a rejected update must not reload the runtime (%d -> %d)", before, manager.reloads)
	}
}
