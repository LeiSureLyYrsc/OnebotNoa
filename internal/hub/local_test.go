package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/config"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/model"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/onebot"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/store"
)

// newLocalTestHub builds a hub with one account, one bot and a binding.
func newLocalTestHub(t *testing.T) (*Hub, *LocalService, *store.Store, model.Bot, model.Account) {
	t.Helper()
	ctx := context.Background()

	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "local.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	account, err := st.CreateAccount(ctx, "50001", "测试号", "test")
	if err != nil {
		t.Fatal(err)
	}
	bot, err := st.CreateBot(ctx, "framework", "hash", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateBinding(ctx, model.Binding{
		BotID: bot.ID, AccountID: account.ID, IsDefault: true, Enabled: true, Scope: []byte("{}"),
	}); err != nil {
		t.Fatal(err)
	}

	cfg := testConfig(t)
	relay := New(cfg, st, slog.New(slog.DiscardHandler))
	service := NewLocalService(relay, 50*time.Millisecond, slog.New(slog.DiscardHandler))
	return relay, service, st, bot, account
}

func testConfig(t *testing.T) *config.Config {
	t.Helper()
	return config.Default()
}

// dialConn attaches a fake downstream connection that carries a bot's bindings.
func attachDownstream(t *testing.T, h *Hub, bot model.Bot, accountIDs ...int64) (*DownstreamConn, *fakePeer) {
	t.Helper()
	peer := newFakePeer("down-1", onebot.RoleUniversal)
	conn := newDownstreamConn(peer.ID(), DownstreamInfo{Bot: bot}, peer, h.actions, h, slog.New(slog.DiscardHandler))
	conn.mu.Lock()
	for _, id := range accountIDs {
		conn.bindings[id] = model.Binding{BotID: bot.ID, AccountID: id, Enabled: true, IsDefault: true}
	}
	conn.mu.Unlock()
	return conn, peer
}

func TestLocalServiceAnswersReadOnlyActions(t *testing.T) {
	relay, service, _, bot, account := newLocalTestHub(t)
	conn, peer := attachDownstream(t, relay, bot, account.ID)

	for _, action := range []string{"get_status", "get_version_info", "can_send_image", "hub_list_accounts", "hub_get_account"} {
		handled := service.HandleAction(context.Background(), conn, account.SelfID, onebot.ActionFrame{
			Action: action,
			Echo:   json.RawMessage(`1`),
		}, json.RawMessage(`1`))
		if !handled {
			t.Fatalf("%s should be answered locally", action)
		}
	}

	frames := peer.sentFrames()
	if len(frames) != 5 {
		t.Fatalf("frames = %d, want 5", len(frames))
	}
	for _, frame := range frames {
		var resp struct {
			Status string `json:"status"`
			Echo   json.RawMessage `json:"echo"`
		}
		if err := json.Unmarshal(frame, &resp); err != nil {
			t.Fatalf("decode %s: %v", frame, err)
		}
		if resp.Status != "ok" {
			t.Fatalf("local answer should be ok: %s", frame)
		}
		if string(resp.Echo) != "1" {
			t.Fatalf("echo must be preserved: %s", frame)
		}
	}

	// get_status reports the account, which is offline because nothing is attached.
	var status struct {
		Data struct {
			Online   bool              `json:"online"`
			Accounts map[string]string `json:"accounts"`
		} `json:"data"`
	}
	if err := json.Unmarshal(frames[0], &status); err != nil {
		t.Fatal(err)
	}
	if status.Data.Online {
		t.Fatal("no upstream is attached, so online must be false")
	}
	if state := status.Data.Accounts["50001"]; state != model.StatusOffline {
		t.Fatalf("account state = %q, want offline", state)
	}
}

// TestLocalServiceRequiresAResolvedAccount pins the contract with the routing
// layer: the relay never guesses which account a request meant. Ambiguity is
// resolved by the router (which answers 1404) before this handler is reached.
func TestLocalServiceRequiresAResolvedAccount(t *testing.T) {
	relay, service, _, bot, account := newLocalTestHub(t)
	conn, _ := attachDownstream(t, relay, bot, account.ID)

	if service.HandleAction(context.Background(), conn, "", onebot.ActionFrame{Action: "get_login_info"}, nil) {
		t.Fatal("an unresolved account must not be answered locally")
	}

	// With the resolved account the relay answers from the cached identity.
	single, peer := attachDownstream(t, relay, bot, account.ID)
	if !service.HandleAction(context.Background(), single, account.SelfID, onebot.ActionFrame{Action: "get_login_info"}, nil) {
		t.Fatal("a resolved get_login_info should be answered locally")
	}
	frames := peer.sentFrames()
	if len(frames) != 1 {
		t.Fatalf("frames = %d, want 1", len(frames))
	}
	var resp struct {
		Data struct {
			UserID   json.RawMessage `json:"user_id"`
			Nickname string          `json:"nickname"`
		} `json:"data"`
	}
	if err := json.Unmarshal(frames[0], &resp); err != nil {
		t.Fatal(err)
	}
	if string(resp.Data.UserID) != "50001" {
		t.Fatalf("user_id = %s, want a numeric 50001 (not a string)", resp.Data.UserID)
	}
}

func TestLocalServiceSynthesisesMetaEvents(t *testing.T) {
	relay, service, _, bot, account := newLocalTestHub(t)
	conn, peer := attachDownstream(t, relay, bot, account.ID)

	service.OnConnect(context.Background(), conn)
	frames := peer.sentFrames()
	if len(frames) != 2 {
		t.Fatalf("frames = %d, want lifecycle + heartbeat", len(frames))
	}

	var lifecycle struct {
		PostType      string          `json:"post_type"`
		MetaEventType string          `json:"meta_event_type"`
		SubType       string          `json:"sub_type"`
		SelfID        json.RawMessage `json:"self_id"`
	}
	if err := json.Unmarshal(frames[0], &lifecycle); err != nil {
		t.Fatal(err)
	}
	if lifecycle.PostType != "meta_event" || lifecycle.MetaEventType != "lifecycle" || lifecycle.SubType != "connect" {
		t.Fatalf("unexpected lifecycle: %s", frames[0])
	}
	if string(lifecycle.SelfID) != "50001" {
		t.Fatalf("lifecycle self_id = %s", lifecycle.SelfID)
	}

	var heartbeat struct {
		PostType      string `json:"post_type"`
		MetaEventType string `json:"meta_event_type"`
		Status        struct {
			Online bool   `json:"online"`
			Good   bool   `json:"good"`
			State  string `json:"state"`
		} `json:"status"`
	}
	if err := json.Unmarshal(frames[1], &heartbeat); err != nil {
		t.Fatal(err)
	}
	if heartbeat.PostType != "meta_event" || heartbeat.MetaEventType != "heartbeat" {
		t.Fatalf("unexpected heartbeat: %s", frames[1])
	}
	if heartbeat.Status.Online || heartbeat.Status.State != model.StatusOffline {
		t.Fatalf("offline account must be reported offline: %s", frames[1])
	}
}

func TestLocalServiceHeartbeatTracksAccountState(t *testing.T) {
	relay, service, _, bot, account := newLocalTestHub(t)
	conn, peer := attachDownstream(t, relay, bot, account.ID)

	// Attach a live upstream connection for the account.
	upstream := newFakePeer("up-1", onebot.RoleUniversal)
	upstream.selfID = account.SelfID
	if _, err := relay.Registry().AttachUpstream(context.Background(), UpstreamInfo{
		SelfID: account.SelfID, Role: onebot.RoleUniversal, Source: "test",
	}, upstream); err != nil {
		t.Fatalf("attach upstream: %v", err)
	}

	service.sendHeartbeat(conn)
	frames := peer.sentFrames()
	if len(frames) != 1 {
		t.Fatalf("frames = %d, want 1", len(frames))
	}
	var heartbeat struct {
		Status struct {
			Online bool   `json:"online"`
			Good   bool   `json:"good"`
			State  string `json:"state"`
		} `json:"status"`
	}
	if err := json.Unmarshal(frames[0], &heartbeat); err != nil {
		t.Fatal(err)
	}
	if !heartbeat.Status.Online || !heartbeat.Status.Good || heartbeat.Status.State != model.StatusOnline {
		t.Fatalf("an attached universal upstream means online/good: %s", frames[0])
	}
}

func TestLocalServiceStartStopIsIdempotent(t *testing.T) {
	_, service, _, _, _ := newLocalTestHub(t)
	service.Start()
	service.Stop()
	service.Stop() // must not panic on a double stop
}


// TestInvokeUsesLocalAnswersFirst pins the debugger's semantics: a relay-served
// action must be answered by the relay itself, exactly like it is for a Bot.
// Otherwise the debugger would show a different answer than the Bot gets.
func TestInvokeUsesLocalAnswersFirst(t *testing.T) {
	relay, service, _, bot, account := newLocalTestHub(t)
	relay.Actions().SetLocalHandler(service)

	// Attach a live upstream connection that answers everything with a marker,
	// so we can tell a local answer from a forwarded one.
	upstream := newFakePeer("up-local", onebot.RoleUniversal)
	upstream.selfID = account.SelfID
	if _, err := relay.Registry().AttachUpstream(context.Background(), UpstreamInfo{
		SelfID: account.SelfID, Role: onebot.RoleUniversal, Source: "test",
	}, upstream); err != nil {
		t.Fatalf("attach upstream: %v", err)
	}
	frames := [][]byte{}
	upstream.Send([]byte(`{}`))

	reply, err := relay.Invoke(context.Background(), account.SelfID,
		[]byte(`{"action":"get_version_info","params":{},"echo":"c1"}`),
		json.RawMessage(`"c1"`), false)
	if err != nil {
		t.Fatalf("invoke get_version_info: %v", err)
	}
	var response struct {
		Status string `json:"status"`
		Data   struct {
			AppName string `json:"app_name"`
		} `json:"data"`
	}
	if err := json.Unmarshal(reply, &response); err != nil {
		t.Fatalf("decode reply %s: %v", reply, err)
	}
	if response.Status != "ok" || response.Data.AppName != "OnebotNoa" {
		t.Fatalf("the relay must answer get_version_info itself, got %s", reply)
	}
	_ = bot
	_ = frames

	// hub_list_accounts must report the bound account, not an empty list: the
	// console builds its synthetic connection from the real bindings.
	listReply, err := relay.Invoke(context.Background(), account.SelfID,
		[]byte(`{"action":"hub_list_accounts","params":{},"echo":"c3"}`),
		json.RawMessage(`"c3"`), false)
	if err != nil {
		t.Fatalf("invoke hub_list_accounts: %v", err)
	}
	var listResponse struct {
		Data struct {
			Accounts []struct {
				SelfID string `json:"self_id"`
				State  string `json:"state"`
			} `json:"accounts"`
		} `json:"data"`
	}
	if err := json.Unmarshal(listReply, &listResponse); err != nil {
		t.Fatalf("decode %s: %v", listReply, err)
	}
	if len(listResponse.Data.Accounts) != 1 {
		t.Fatalf("hub_list_accounts returned %d accounts, want 1: %s", len(listResponse.Data.Accounts), listReply)
	}
	if listResponse.Data.Accounts[0].SelfID != account.SelfID {
		t.Fatalf("hub_list_accounts reported %q, want %q", listResponse.Data.Accounts[0].SelfID, account.SelfID)
	}
	if listResponse.Data.Accounts[0].State != model.StatusOnline {
		t.Fatalf("the attached upstream means online, got %q", listResponse.Data.Accounts[0].State)
	}
}

// TestInvokeForwardsUnknownActions keeps the other half honest: actions the relay
// does not implement must still reach the upstream.
func TestInvokeForwardsUnknownActions(t *testing.T) {
	relay, service, _, _, account := newLocalTestHub(t)
	relay.Actions().SetLocalHandler(service)

	upstream := newFakePeer("up-fwd", onebot.RoleUniversal)
	upstream.selfID = account.SelfID
	if _, err := relay.Registry().AttachUpstream(context.Background(), UpstreamInfo{
		SelfID: account.SelfID, Role: onebot.RoleUniversal, Source: "test",
	}, upstream); err != nil {
		t.Fatalf("attach upstream: %v", err)
	}

	result := make(chan error, 1)
	go func() {
		reply, err := relay.Invoke(context.Background(), account.SelfID,
			[]byte(`{"action":"get_group_list","params":{},"echo":"c2"}`),
			json.RawMessage(`"c2"`), false)
		if err != nil {
			result <- err
			return
		}
		if !json.Valid(reply) {
			result <- fmt.Errorf("invalid reply: %s", reply)
			return
		}
		result <- nil
	}()

	// The upstream receives the forwarded action and answers it.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		sent := upstream.sentFrames()
		if len(sent) > 0 {
			var forwarded struct {
				Echo json.RawMessage `json:"echo"`
			}
			if err := json.Unmarshal(sent[len(sent)-1], &forwarded); err == nil && len(forwarded.Echo) > 0 {
				// Answer through the normal dispatch path.
				reply := fmt.Sprintf(`{"status":"ok","retcode":0,"data":[1,2,3],"echo":%s}`, forwarded.Echo)
				relay.actions.HandleResponse([]byte(reply))
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}

	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("forwarded invoke failed: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("forwarded invoke never completed")
	}
}

func TestInvokeRejectsUnknownAccount(t *testing.T) {
	relay, _, _, _, _ := newLocalTestHub(t)
	_, err := relay.Invoke(context.Background(), "99999", []byte(`{"action":"get_status"}`), nil, false)
	if err == nil {
		t.Fatal("invoking on an unknown account must fail")
	}
}
