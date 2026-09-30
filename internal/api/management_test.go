package api

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/hub"
)

// loginAdmin authenticates the fixture admin and returns its CSRF token.
func loginAdmin(t *testing.T, a *testAPI) string {
	t.Helper()
	res, payload := a.do(t, http.MethodPost, "/api/v1/auth/login",
		fmt.Sprintf(`{"username":"admin","password":%q}`, testPassword), nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("login = %d, want 200", res.StatusCode)
	}
	csrf, _ := payload["csrf_token"].(string)
	if csrf == "" {
		t.Fatal("no csrf token in the login response")
	}
	return csrf
}

func (a *testAPI) write(t *testing.T, method, path, body, csrf string) (*http.Response, map[string]any) {
	t.Helper()
	headers := map[string]string{}
	if csrf != "" {
		headers["X-CSRF-Token"] = csrf
	}
	return a.do(t, method, path, body, headers)
}

func mustStatus(t *testing.T, what string, res *http.Response, want int) map[string]any {
	t.Helper()
	if res.StatusCode != want {
		t.Fatalf("%s = %d, want %d", what, res.StatusCode, want)
	}
	return nil
}

func TestManagementEndpointsRequireAuth(t *testing.T) {
	a := newTestAPI(t)
	for _, path := range []string{
		"/api/v1/accounts",
		"/api/v1/accounts/pending",
		"/api/v1/bots",
		"/api/v1/bindings",
		"/api/v1/listeners",
		"/api/v1/events/recent",
	} {
		res, _ := a.do(t, http.MethodGet, path, "", nil)
		if res.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s without a session = %d, want 401", path, res.StatusCode)
		}
	}
}

func TestAccountLifecycleAndToken(t *testing.T) {
	a := newTestAPI(t)
	csrf := loginAdmin(t, a)

	res, payload := a.write(t, http.MethodPost, "/api/v1/accounts",
		`{"self_id":"10001","name":"主号"}`, csrf)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create account = %d (%v)", res.StatusCode, payload)
	}
	account, _ := payload["account"].(map[string]any)
	if account == nil || account["self_id"] != "10001" {
		t.Fatalf("unexpected account payload: %v", payload)
	}
	id := int64(account["id"].(float64))

	// Duplicate self_id is a conflict, not a server error.
	res, _ = a.write(t, http.MethodPost, "/api/v1/accounts", `{"self_id":"10001"}`, csrf)
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate account = %d, want 409", res.StatusCode)
	}

	// Empty self_id is rejected.
	res, _ = a.write(t, http.MethodPost, "/api/v1/accounts", `{"self_id":"  "}`, csrf)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty self_id = %d, want 400", res.StatusCode)
	}

	// A per-instance token is returned exactly once and only stored hashed.
	res, payload = a.write(t, http.MethodPost, fmt.Sprintf("/api/v1/accounts/%d/token", id), "", csrf)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("rotate account token = %d (%v)", res.StatusCode, payload)
	}
	token, _ := payload["token"].(string)
	if len(token) < 32 {
		t.Fatalf("token looks wrong: %q", token)
	}
	stored, err := a.st.AccountBySelfID(context.Background(), "10001")
	if err != nil {
		t.Fatal(err)
	}
	if !stored.HasToken {
		t.Fatal("account should report a bound token")
	}
	if strings.Contains(stored.SelfID, token) {
		t.Fatal("token must not be stored in the clear")
	}
	byToken, err := a.st.AccountByTokenHash(context.Background(), hashTokenForTest(token))
	if err != nil {
		t.Fatalf("the issued token must resolve to the account: %v", err)
	}
	if byToken.SelfID != "10001" {
		t.Fatalf("token resolved to %s", byToken.SelfID)
	}

	// Disabling an account closes its live connections and persists.
	res, _ = a.write(t, http.MethodPatch, fmt.Sprintf("/api/v1/accounts/%d", id),
		`{"enabled":false,"name":"disabled"}`, csrf)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("patch account = %d", res.StatusCode)
	}
	after, err := a.st.AccountByID(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if after.Enabled || after.Name != "disabled" {
		t.Fatalf("account not updated: %+v", after)
	}

	// Clearing the token.
	res, _ = a.write(t, http.MethodDelete, fmt.Sprintf("/api/v1/accounts/%d/token", id), "", csrf)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("clear token = %d", res.StatusCode)
	}
	cleared, _ := a.st.AccountByID(context.Background(), id)
	if cleared.HasToken {
		t.Fatal("token should be cleared")
	}

	// Listing reports live state and pending approvals.
	res, payload = a.do(t, http.MethodGet, "/api/v1/accounts", "", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("list accounts = %d", res.StatusCode)
	}
	accounts, _ := payload["accounts"].([]any)
	if len(accounts) != 1 {
		t.Fatalf("accounts = %d, want 1", len(accounts))
	}
	first, _ := accounts[0].(map[string]any)
	if first["live_state"] != "offline" {
		t.Fatalf("live_state = %v, want offline", first["live_state"])
	}
	if _, ok := payload["pending"]; !ok {
		t.Fatal("list must include the pending queue")
	}

	// Delete.
	res, _ = a.write(t, http.MethodDelete, fmt.Sprintf("/api/v1/accounts/%d", id), "", csrf)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("delete account = %d", res.StatusCode)
	}
	if _, err := a.st.AccountByID(context.Background(), id); err == nil {
		t.Fatal("account still present after delete")
	}

	// Missing account -> 404, bad id -> 400.
	res, _ = a.do(t, http.MethodGet, "/api/v1/accounts/999", "", nil)
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("missing account = %d, want 404", res.StatusCode)
	}
	res, _ = a.do(t, http.MethodGet, "/api/v1/accounts/abc", "", nil)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad id = %d, want 400", res.StatusCode)
	}
}

func TestBotLifecycleAndTokenRotation(t *testing.T) {
	a := newTestAPI(t)
	csrf := loginAdmin(t, a)

	res, payload := a.write(t, http.MethodPost, "/api/v1/bots", `{"name":"nonebot","note":"主框架"}`, csrf)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create bot = %d (%v)", res.StatusCode, payload)
	}
	token1, _ := payload["token"].(string)
	bot, _ := payload["bot"].(map[string]any)
	if token1 == "" || bot == nil {
		t.Fatalf("create bot payload incomplete: %v", payload)
	}
	if _, leaked := bot["token_hash"]; leaked {
		t.Fatal("token hash must never be serialised")
	}
	id := int64(bot["id"].(float64))

	res, _ = a.write(t, http.MethodPost, "/api/v1/bots", `{"name":"nonebot"}`, csrf)
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate bot name = %d, want 409", res.StatusCode)
	}

	res, payload = a.write(t, http.MethodPost, fmt.Sprintf("/api/v1/bots/%d/token/rotate", id), "", csrf)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("rotate = %d (%v)", res.StatusCode, payload)
	}
	token2, _ := payload["token"].(string)
	if token2 == token1 || token2 == "" {
		t.Fatalf("rotation did not change the token (%q -> %q)", token1, token2)
	}
	if _, err := a.st.BotByTokenHash(context.Background(), hashTokenForTest(token1)); err == nil {
		t.Fatal("the old token must stop working")
	}

	// Policies round-trip through JSON columns.
	res, _ = a.write(t, http.MethodPatch, fmt.Sprintf("/api/v1/bots/%d", id),
		`{"enabled":false,"rate_limit":{"rate":2},"action_policy":{"deny":["set_group_ban"]}}`, csrf)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("patch bot = %d", res.StatusCode)
	}
	updated, err := a.st.BotByID(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Enabled {
		t.Fatal("bot should be disabled")
	}
	if !strings.Contains(string(updated.RateLimit), "rate") || !strings.Contains(string(updated.ActionPolicy), "set_group_ban") {
		t.Fatalf("policies not stored: %s / %s", updated.RateLimit, updated.ActionPolicy)
	}

	res, _ = a.write(t, http.MethodPatch, fmt.Sprintf("/api/v1/bots/%d", id), `{"action_policy":"{oops"}`, csrf)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid policy JSON = %d, want 400", res.StatusCode)
	}

	res, payload = a.do(t, http.MethodGet, "/api/v1/bots", "", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("list bots = %d", res.StatusCode)
	}
	bots, _ := payload["bots"].([]any)
	if len(bots) != 1 {
		t.Fatalf("bots = %d, want 1", len(bots))
	}

	res, _ = a.write(t, http.MethodDelete, fmt.Sprintf("/api/v1/bots/%d", id), "", csrf)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("delete bot = %d", res.StatusCode)
	}
}

func TestBindingLifecycleAndScopeValidation(t *testing.T) {
	a := newTestAPI(t)
	csrf := loginAdmin(t, a)
	ctx := context.Background()

	account, err := a.st.CreateAccount(ctx, "20001", "acc", "test")
	if err != nil {
		t.Fatal(err)
	}
	bot, err := a.st.CreateBot(ctx, "bot-x", hashTokenForTest("seed"), "")
	if err != nil {
		t.Fatal(err)
	}

	body := fmt.Sprintf(`{"bot_id":%d,"account_id":%d,"is_default":true,"scope":{"post_types":["message"]}}`, bot.ID, account.ID)
	res, payload := a.write(t, http.MethodPost, "/api/v1/bindings", body, csrf)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create binding = %d (%v)", res.StatusCode, payload)
	}
	if !strings.Contains(fmt.Sprintf("%v", payload), "bot-x") {
		t.Fatalf("binding view should carry names: %v", payload)
	}

	// The same pair twice is a conflict.
	res, _ = a.write(t, http.MethodPost, "/api/v1/bindings", body, csrf)
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate binding = %d, want 409", res.StatusCode)
	}

	// Unknown references are rejected before touching the database.
	res, _ = a.write(t, http.MethodPost, "/api/v1/bindings",
		fmt.Sprintf(`{"bot_id":999,"account_id":%d}`, account.ID), csrf)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown bot_id = %d, want 400", res.StatusCode)
	}

	// Invalid scope (bad meta_events) and malformed JSON.
	bindings, err := a.st.ListBindings(ctx)
	if err != nil || len(bindings) != 1 {
		t.Fatalf("bindings = %d, %v", len(bindings), err)
	}
	bindingID := bindings[0].ID

	res, _ = a.write(t, http.MethodPatch, fmt.Sprintf("/api/v1/bindings/%d", bindingID),
		`{"scope":{"meta_events":"sometimes"}}`, csrf)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad meta_events = %d, want 400", res.StatusCode)
	}
	res, _ = a.write(t, http.MethodPatch, fmt.Sprintf("/api/v1/bindings/%d", bindingID),
		`{"scope":{"post_types":`, csrf)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("malformed scope = %d, want 400", res.StatusCode)
	}

	// A valid scope update is persisted and reflected in the hub cache.
	res, _ = a.write(t, http.MethodPatch, fmt.Sprintf("/api/v1/bindings/%d", bindingID),
		`{"scope":{"exclude_self":true},"priority":5,"enabled":false}`, csrf)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("patch binding = %d", res.StatusCode)
	}
	updated, _ := a.st.BindingByID(ctx, bindingID)
	if updated.Priority != 5 || updated.Enabled || !strings.Contains(string(updated.Scope), "exclude_self") {
		t.Fatalf("binding not updated: %+v", updated)
	}

	res, _ = a.write(t, http.MethodDelete, fmt.Sprintf("/api/v1/bindings/%d", bindingID), "", csrf)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("delete binding = %d", res.StatusCode)
	}
	if remaining, _ := a.st.ListBindings(ctx); len(remaining) != 0 {
		t.Fatalf("binding survived delete: %+v", remaining)
	}
}

func TestPendingApprovalEndpoints(t *testing.T) {
	a := newTestAPI(t)
	csrf := loginAdmin(t, a)

	res, payload := a.do(t, http.MethodGet, "/api/v1/accounts/pending", "", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("list pending = %d", res.StatusCode)
	}
	if pending, _ := payload["pending"].([]any); len(pending) != 0 {
		t.Fatalf("pending should start empty: %v", pending)
	}

	res, _ = a.write(t, http.MethodPost, "/api/v1/accounts/pending/approve", `{}`, csrf)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("approve without id = %d, want 400", res.StatusCode)
	}
	res, _ = a.write(t, http.MethodPost, "/api/v1/accounts/pending/approve", `{"id":"nope|Universal"}`, csrf)
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("approve unknown = %d, want 404", res.StatusCode)
	}
	res, _ = a.write(t, http.MethodPost, "/api/v1/accounts/pending/reject", `{"id":"nope|Universal"}`, csrf)
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("reject unknown = %d, want 404", res.StatusCode)
	}
}

func TestListenerCRUDValidation(t *testing.T) {
	a := newTestAPI(t)
	csrf := loginAdmin(t, a)

	res, payload := a.write(t, http.MethodPost, "/api/v1/listeners",
		`{"name":"qq-10001","kind":"upstream_listen","bind_addr":"0.0.0.0:6710","path":"/onebot/v11/ws"}`, csrf)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create listener = %d (%v)", res.StatusCode, payload)
	}
	listener, _ := payload["listener"].(map[string]any)
	id := int64(listener["id"].(float64))
	if listener["runtime"] != "pending" {
		t.Fatalf("runtime = %v, want pending until I9", listener["runtime"])
	}

	res, _ = a.write(t, http.MethodPost, "/api/v1/listeners",
		`{"name":"bad","kind":"sideways","bind_addr":"0.0.0.0:1","path":"/x"}`, csrf)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad kind = %d, want 400", res.StatusCode)
	}
	res, _ = a.write(t, http.MethodPost, "/api/v1/listeners",
		`{"name":"bad","kind":"upstream_listen","bind_addr":"0.0.0.0","path":"/x"}`, csrf)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("addr without port = %d, want 400", res.StatusCode)
	}
	res, _ = a.write(t, http.MethodPost, "/api/v1/listeners",
		`{"name":"bad","kind":"upstream_listen","bind_addr":"0.0.0.0:1","path":"x"}`, csrf)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("path without slash = %d, want 400", res.StatusCode)
	}
	res, _ = a.write(t, http.MethodPost, "/api/v1/listeners",
		`{"name":"dup","kind":"upstream_listen","bind_addr":"0.0.0.0:6710","path":"/onebot/v11/ws"}`, csrf)
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate addr+path = %d, want 409", res.StatusCode)
	}

	res, payload = a.do(t, http.MethodGet, "/api/v1/listeners", "", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("list listeners = %d", res.StatusCode)
	}
	if _, ok := payload["shared"]; !ok {
		t.Fatal("listeners response should describe the shared endpoints")
	}

	res, _ = a.write(t, http.MethodDelete, fmt.Sprintf("/api/v1/listeners/%d", id), "", csrf)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("delete listener = %d", res.StatusCode)
	}
}

func TestEndpointCRUDAndValidation(t *testing.T) {
	a := newTestAPI(t)
	csrf := loginAdmin(t, a)

	// Validation catches the common mistakes before anything is stored.
	cases := []struct {
		name string
		body string
		want int
	}{
		{"bad kind", `{"name":"x","kind":"sideways","url":"ws://h:1/"}`, http.StatusBadRequest},
		{"bad scheme", `{"name":"x","kind":"upstream_dial","url":"http://h:1/"}`, http.StatusBadRequest},
		{"bad mode", `{"name":"x","kind":"upstream_dial","url":"ws://h:1/","mode":"triple"}`, http.StatusBadRequest},
		{"no name", `{"kind":"upstream_dial","url":"ws://h:1/"}`, http.StatusBadRequest},
		{"downstream without bot", `{"name":"x","kind":"downstream_dial","url":"ws://h:1/"}`, http.StatusBadRequest},
		{"upstream with bot", `{"name":"x","kind":"upstream_dial","url":"ws://h:1/","bot_id":1}`, http.StatusBadRequest},
		{"bad reconnect", `{"name":"x","kind":"upstream_dial","url":"ws://h:1/","reconnect":{"min":"soon"}}`, http.StatusBadRequest},
		{"bad jitter", `{"name":"x","kind":"upstream_dial","url":"ws://h:1/","reconnect":{"jitter":2}}`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		res, _ := a.write(t, http.MethodPost, "/api/v1/endpoints", tc.body, csrf)
		if res.StatusCode != tc.want {
			t.Fatalf("%s = %d, want %d", tc.name, res.StatusCode, tc.want)
		}
	}

	res, payload := a.write(t, http.MethodPost, "/api/v1/endpoints",
		`{"name":"qq-10001","kind":"upstream_dial","url":"ws://127.0.0.1:6700/","account_hint":"10001","token":"sec","reconnect":{"min":"2s","max":"30s","jitter":0.2}}`, csrf)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create endpoint = %d (%v)", res.StatusCode, payload)
	}
	stored, err := a.st.EndpointByName(context.Background(), "qq-10001")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Token != "sec" || !strings.Contains(string(stored.Reconnect), "30s") {
		t.Fatalf("endpoint not stored as expected: %+v", stored)
	}

	res, _ = a.write(t, http.MethodPost, "/api/v1/endpoints",
		`{"name":"qq-10001","kind":"upstream_dial","url":"ws://127.0.0.1:6700/"}`, csrf)
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate endpoint = %d, want 409", res.StatusCode)
	}

	// The token is never serialised back to the client.
	res, payload = a.do(t, http.MethodGet, "/api/v1/endpoints", "", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("list endpoints = %d", res.StatusCode)
	}
	if strings.Contains(fmt.Sprintf("%v", payload), `"token":"sec"`) {
		t.Fatalf("token leaked in the response: %v", payload)
	}

	// Updating without a token keeps the stored one.
	res, _ = a.write(t, http.MethodPatch, fmt.Sprintf("/api/v1/endpoints/%d", stored.ID),
		`{"name":"qq-10001","kind":"upstream_dial","url":"ws://127.0.0.1:6701/","account_hint":"10001","enabled":false}`, csrf)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("patch endpoint = %d", res.StatusCode)
	}
	updated, _ := a.st.EndpointByID(context.Background(), stored.ID)
	if updated.URL != "ws://127.0.0.1:6701/" || updated.Enabled || updated.Token != "sec" {
		t.Fatalf("endpoint not updated correctly: %+v", updated)
	}

	// Forced reconnect without a running dialer is a 404, not a crash.
	res, _ = a.write(t, http.MethodPost, "/api/v1/endpoints/qq-10001/reconnect", "", csrf)
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("reconnect without a dialer = %d, want 404", res.StatusCode)
	}

	res, _ = a.write(t, http.MethodDelete, fmt.Sprintf("/api/v1/endpoints/%d", stored.ID), "", csrf)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("delete endpoint = %d", res.StatusCode)
	}
	if _, err := a.st.EndpointByName(context.Background(), "qq-10001"); err == nil {
		t.Fatal("endpoint survived delete")
	}
}

func TestEventsRecentAndFilters(t *testing.T) {
	a := newTestAPI(t)
	loginAdmin(t, a)

	a.events.Publish(hub.EventRecord{Kind: hub.EventKindUpstream, SelfID: "30001", PostType: "message", GroupID: "5"})
	a.events.Publish(hub.EventRecord{Kind: hub.EventKindAction, Bot: "bot-a", SelfID: "30001", Action: "send_msg"})
	a.events.Publish(hub.EventRecord{Kind: hub.EventKindUpstream, SelfID: "30002", PostType: "notice"})

	res, payload := a.do(t, http.MethodGet, "/api/v1/events/recent?limit=10", "", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("recent = %d", res.StatusCode)
	}
	events, _ := payload["events"].([]any)
	if len(events) != 3 {
		t.Fatalf("events = %d, want 3", len(events))
	}
	if payload["ring_size"] == nil {
		t.Fatal("recent response should report the ring size")
	}

	res, payload = a.do(t, http.MethodGet, "/api/v1/events/recent?self_id=30001&kind=upstream", "", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("filtered recent = %d", res.StatusCode)
	}
	events, _ = payload["events"].([]any)
	if len(events) != 1 {
		t.Fatalf("filtered events = %d, want 1", len(events))
	}
	if rec, _ := events[0].(map[string]any); rec["self_id"] != "30001" || rec["kind"] != "upstream" {
		t.Fatalf("unexpected record: %v", rec)
	}
}

// readSSE opens the stream and returns a reader plus a close function.
func readSSE(t *testing.T, a *testAPI, query string, headers map[string]string) (*bufio.Reader, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.ts.URL+"/api/v1/events/stream"+query, nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := a.http.Do(req)
	if err != nil {
		cancel()
		t.Fatalf("open stream: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		cancel()
		t.Fatalf("stream = %d, want 200", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); ct != "text/event-stream" {
		cancel()
		t.Fatalf("content type = %q", ct)
	}
	return bufio.NewReader(res.Body), func() {
		cancel()
		_ = res.Body.Close()
	}
}

func waitForSSEData(t *testing.T, reader *bufio.Reader, timeout time.Duration, want string) string {
	t.Helper()
	type result struct{ line string }
	done := make(chan result, 1)
	go func() {
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				done <- result{}
				return
			}
			if strings.HasPrefix(line, "data: ") {
				done <- result{line}
				return
			}
		}
	}()
	select {
	case r := <-done:
		if r.line == "" {
			t.Fatal("stream closed before delivering data")
		}
		if want != "" && !strings.Contains(r.line, want) {
			t.Fatalf("SSE payload %q does not contain %q", r.line, want)
		}
		return r.line
	case <-time.After(timeout):
		t.Fatal("timed out waiting for SSE data")
		return ""
	}
}

func TestEventStreamDeliversLiveRecords(t *testing.T) {
	a := newTestAPI(t)
	loginAdmin(t, a)

	reader, closeStream := readSSE(t, a, "?kind=upstream", nil)
	defer closeStream()

	// Deterministic handshake: wait until the handler subscribed.
	deadline := time.Now().Add(2 * time.Second)
	for a.events.Subscribers() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if a.events.Subscribers() == 0 {
		t.Fatal("stream handler never subscribed")
	}

	a.events.Publish(hub.EventRecord{Kind: hub.EventKindUpstream, SelfID: "40001", PostType: "message"})
	waitForSSEData(t, reader, 3*time.Second, "40001")

	// Records of another kind must not appear on this filtered stream.
	a.events.Publish(hub.EventRecord{Kind: hub.EventKindAction, Bot: "bot-z", Action: "get_status"})
	a.events.Publish(hub.EventRecord{Kind: hub.EventKindUpstream, SelfID: "40002"})
	waitForSSEData(t, reader, 3*time.Second, "40002")
}

func TestEventStreamResumesFromLastEventID(t *testing.T) {
	a := newTestAPI(t)
	loginAdmin(t, a)

	first := a.events.Publish(hub.EventRecord{Kind: hub.EventKindUpstream, SelfID: "50001"})
	a.events.Publish(hub.EventRecord{Kind: hub.EventKindUpstream, SelfID: "50002"})
	a.events.Publish(hub.EventRecord{Kind: hub.EventKindUpstream, SelfID: "50003"})

	reader, closeStream := readSSE(t, a, "", map[string]string{
		"Last-Event-ID": strconv.FormatUint(first.Seq, 10),
	})
	defer closeStream()

	// The two records published after the last seen id are replayed.
	waitForSSEData(t, reader, 3*time.Second, "50002")
	waitForSSEData(t, reader, 3*time.Second, "50003")
}

func TestSystemStatusReportsRelayCounters(t *testing.T) {
	a := newTestAPI(t)
	loginAdmin(t, a)

	res, payload := a.do(t, http.MethodGet, "/api/v1/system/status", "", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	relay, ok := payload["relay"].(map[string]any)
	if !ok {
		t.Fatalf("status lacks relay counters: %v", payload)
	}
	for _, key := range []string{"accounts", "online", "degraded", "offline", "upstream_conns", "downstream_conns", "pending_actions"} {
		if _, ok := relay[key]; !ok {
			t.Fatalf("relay counters lack %q: %v", key, relay)
		}
	}
	events, ok := payload["events"].(map[string]any)
	if !ok {
		t.Fatalf("status lacks event counters: %v", payload)
	}
	if _, ok := events["ring_size"]; !ok {
		t.Fatalf("event counters incomplete: %v", events)
	}
}

// hashTokenForTest mirrors auth.HashToken for assertions on stored hashes.
func hashTokenForTest(token string) string {
	return authHashToken(token)
}

var _ = json.Valid
