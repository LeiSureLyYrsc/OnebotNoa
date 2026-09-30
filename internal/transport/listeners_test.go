package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/config"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/store"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/model"
)

// freeAddr reserves a loopback port for the duration of a test.
func freeAddr(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()
	return addr
}

func newListenerManager(t *testing.T, env *upstreamEnv) *ListenerManager {
	t.Helper()
	manager := NewListenerManager(env.cfg, env.store, env.dp, slog.New(slog.DiscardHandler))
	manager.Start(env.ctx, nil)
	t.Cleanup(manager.Stop)
	return manager
}

// TestDedicatedUpstreamListenerBindsAndServes is the core promise of the
// milestone: a listener created at runtime owns a real socket and serves the
// data plane on it, without restarting the process.
func TestDedicatedUpstreamListenerBindsAndServes(t *testing.T) {
	env := newUpstreamEnv(t, func(cfg *config.Config) {
		cfg.OneBot.UpstreamWS.UnknownAccountPolicy = "auto"
	})
	account := env.createAccount(t, "95001")
	bot, botToken := env.createBot(t, "dedicated-bot")
	env.bind(t, bot.ID, account.ID, true, "{}")
	botConn := env.dialBot(t, botToken, "")
	waitFor(t, "downstream connection", 3*time.Second, func() bool { return env.hub.DownstreamCount() == 1 })

	addr := freeAddr(t)
	manager := newListenerManager(t, env)
	manager.Apply([]ListenerSpec{{
		Name: "dedicated-up", Kind: config.KindUpstreamListen, Addr: addr,
		Path: "/onebot/v11/ws", AccountHint: account.SelfID, Enabled: true, Source: "database",
	}})

	states := manager.States()
	if len(states) != 1 || states[0].State != "listening" {
		t.Fatalf("listener state = %+v, want listening", states)
	}
	if !strings.Contains(states[0].URL, addr) {
		t.Fatalf("state must report its address: %+v", states[0])
	}

	// A QQ implementation connects to the dedicated port and pushes an event.
	// A dedicated listener still authenticates: the address narrows the scope,
	// it does not replace the credential.
	conn := dialWS(t, "ws://"+addr+"/onebot/v11/ws", map[string]string{
		"X-Self-ID":     account.SelfID,
		"X-Client-Role": "Universal",
		"Authorization": "Bearer " + bootstrapToken,
	})
	defer func() { _ = conn.Close() }()
	waitFor(t, "session on the dedicated listener", 3*time.Second, func() bool {
		_, ok := env.hub.Registry().Session(account.SelfID)
		return ok
	})

	frame := `{"post_type":"message","message_type":"private","self_id":95001,"user_id":3,"message":"dedicated"}`
	if err := conn.WriteMessage(websocket.TextMessage, []byte(frame)); err != nil {
		t.Fatal(err)
	}
	if got := string(readBotFrame(t, botConn, 3*time.Second)); got != frame {
		t.Fatalf("event from the dedicated listener was not relayed: got %s want %s", got, frame)
	}
}

// TestDedicatedListenerRejectsForeignAccount pins the isolation rule: a listener
// created for one account must not accept another.
func TestDedicatedListenerRejectsForeignAccount(t *testing.T) {
	env := newUpstreamEnv(t, func(cfg *config.Config) {
		cfg.OneBot.UpstreamWS.UnknownAccountPolicy = "auto"
	})
	mine := env.createAccount(t, "95002")
	env.createAccount(t, "95003")

	addr := freeAddr(t)
	manager := newListenerManager(t, env)
	manager.Apply([]ListenerSpec{{
		Name: "pinned", Kind: config.KindUpstreamListen, Addr: addr,
		AccountHint: mine.SelfID, Enabled: true, Source: "database",
	}})
	if state := manager.States()[0]; state.State != "listening" {
		t.Fatalf("listener did not start: %+v", state)
	}

	// Without a credential the listener refuses the connection outright.
	_, res, err := websocket.DefaultDialer.Dial(
		"ws://"+addr+"/onebot/v11/ws",
		http.Header{"X-Self-ID": []string{mine.SelfID}, "X-Client-Role": []string{"Universal"}})
	if err == nil {
		t.Fatal("a dedicated listener must still require a token")
	}
	if res == nil || res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 without a token, got %+v", res)
	}

	// A valid credential for the *wrong* account is refused too: the listener is
	// pinned, so holding the bootstrap token does not let another account in.
	_, res, err = websocket.DefaultDialer.Dial(
		"ws://"+addr+"/onebot/v11/ws",
		http.Header{
			"X-Self-ID":     []string{"95003"},
			"X-Client-Role": []string{"Universal"},
			"Authorization": []string{"Bearer " + bootstrapToken},
		})
	if err == nil {
		t.Fatal("a foreign self_id must not be accepted by a pinned listener")
	}
	if res == nil || res.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for a foreign account, got %+v", res)
	}

	// The account the listener was created for is accepted.
	accepted := dialWS(t, "ws://"+addr+"/onebot/v11/ws", map[string]string{
		"X-Self-ID":     mine.SelfID,
		"X-Client-Role": "Universal",
		"Authorization": "Bearer " + bootstrapToken,
	})
	defer func() { _ = accepted.Close() }()
}

// TestListenerHotRebuild covers create / change / disable without a restart.
func TestListenerHotRebuild(t *testing.T) {
	env := newUpstreamEnv(t, func(cfg *config.Config) {
		cfg.OneBot.UpstreamWS.UnknownAccountPolicy = "auto"
	})
	account := env.createAccount(t, "95004")
	manager := newListenerManager(t, env)

	first := freeAddr(t)
	manager.Apply([]ListenerSpec{{
		Name: "hot", Kind: config.KindUpstreamListen, Addr: first,
		AccountHint: account.SelfID, Enabled: true, Source: "database",
	}})
	if !portOpen(t, first) {
		t.Fatalf("listener did not bind %s", first)
	}

	// Moving it to another port must release the old socket and bind the new one.
	second := freeAddr(t)
	manager.Apply([]ListenerSpec{{
		Name: "hot", Kind: config.KindUpstreamListen, Addr: second,
		AccountHint: account.SelfID, Enabled: true, Source: "database",
	}})
	waitFor(t, "new port bound", 3*time.Second, func() bool { return portOpen(t, second) })
	waitFor(t, "old port released", 3*time.Second, func() bool { return !portOpen(t, first) })

	// Disabling stops the socket entirely.
	manager.Apply([]ListenerSpec{{
		Name: "hot", Kind: config.KindUpstreamListen, Addr: second,
		AccountHint: account.SelfID, Enabled: false, Source: "database",
	}})
	waitFor(t, "listener stopped", 3*time.Second, func() bool { return !portOpen(t, second) })
	if state := manager.States()[0]; state.State != "disabled" {
		t.Fatalf("state = %q, want disabled", state.State)
	}

	// Removing it drops it from the runtime view.
	manager.Apply(nil)
	if len(manager.States()) != 0 {
		t.Fatalf("listener survived removal: %+v", manager.States())
	}
}

// TestListenerPortConflictIsReported keeps a bad configuration diagnosable.
func TestListenerPortConflictIsReported(t *testing.T) {
	env := newUpstreamEnv(t, nil)
	account := env.createAccount(t, "95005")

	addr := freeAddr(t)
	blocker, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Close() }()

	manager := newListenerManager(t, env)
	manager.Apply([]ListenerSpec{{
		Name: "conflict", Kind: config.KindUpstreamListen, Addr: addr,
		AccountHint: account.SelfID, Enabled: true, Source: "database",
	}})
	state := manager.States()[0]
	if state.State != "error" {
		t.Fatalf("state = %q, want error", state.State)
	}
	if state.LastError == "" {
		t.Fatal("a port conflict must be reported to the operator")
	}
}

// TestListenerValidationRejectsIncompleteSpecs checks the guard rails.
func TestListenerValidationRejectsIncompleteSpecs(t *testing.T) {
	env := newUpstreamEnv(t, nil)
	manager := newListenerManager(t, env)

	manager.Apply([]ListenerSpec{
		{Name: "no-account", Kind: config.KindUpstreamListen, Addr: freeAddr(t), Enabled: true},
		{Name: "no-bot", Kind: config.KindDownstreamListen, Addr: freeAddr(t), Enabled: true},
		{Name: "weird-kind", Kind: "sideways", Addr: freeAddr(t), Enabled: true},
	})
	for _, state := range manager.States() {
		if state.State != "error" {
			t.Fatalf("%s should be rejected, got %+v", state.Name, state)
		}
	}
}

// TestLoadListenerSpecsMergesConfigAndDatabase mirrors the endpoint loader.
func TestLoadListenerSpecsMergesConfigAndDatabase(t *testing.T) {
	ctx := context.Background()
	env := newUpstreamEnv(t, nil)

	account := env.createAccount(t, "95006")
	bot, _ := env.createBot(t, "listener-bot")

	cfg := config.Default()
	enabled := true
	cfg.Dedicated = []config.Listener{
		{Name: "from-config", Kind: config.KindUpstreamListen, Addr: "127.0.0.1:0", AccountSelfID: "95006", Enabled: &enabled},
	}
	if _, err := env.store.CreateListener(ctx, model.Listener{
		Name: "from-db", Kind: config.KindDownstreamListen, BindAddr: "127.0.0.1:0",
		Path: "/onebot/v11/bot/ws", BotID: &bot.ID, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	accountID := account.ID
	if _, err := env.store.CreateListener(ctx, model.Listener{
		Name: "by-account-id", Kind: config.KindUpstreamListen, BindAddr: "127.0.0.1:0",
		AccountID: &accountID, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	specs := LoadListenerSpecs(ctx, cfg, env.store, slog.New(slog.DiscardHandler))
	byName := map[string]ListenerSpec{}
	for _, spec := range specs {
		byName[spec.Name] = spec
	}
	if len(specs) != 3 {
		t.Fatalf("specs = %d, want 3: %+v", len(specs), specs)
	}
	if spec := byName["from-config"]; spec.Source != "config" || spec.AccountHint != "95006" {
		t.Fatalf("config listener not carried over: %+v", spec)
	}
	if spec := byName["from-db"]; spec.BotID != bot.ID || spec.BotName != bot.Name {
		t.Fatalf("Bot not resolved: %+v", spec)
	}
	// An account referenced by id is resolved to its self_id for the runtime.
	if spec := byName["by-account-id"]; spec.AccountHint != account.SelfID {
		t.Fatalf("account id not resolved: %+v", spec)
	}
}

func TestListenerSpecsFromTheStoreUseAddressAndPath(t *testing.T) {
	ctx := context.Background()
	env := newUpstreamEnv(t, nil)
	account := env.createAccount(t, "95007")

	accountID := account.ID
	created, err := env.store.CreateListener(ctx, model.Listener{
		Name: "store-driven", Kind: config.KindUpstreamListen, BindAddr: "127.0.0.1:0",
		Path: "/custom", AccountID: &accountID, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	specs := LoadListenerSpecs(ctx, config.Default(), env.store, slog.New(slog.DiscardHandler))
	if len(specs) != 1 {
		t.Fatalf("specs = %+v", specs)
	}
	if specs[0].DBID != created.ID || specs[0].Addr != "127.0.0.1:0" || specs[0].Path != "/custom" {
		t.Fatalf("stored listener not mapped: %+v", specs[0])
	}
}

// dialWS opens a WebSocket with the given headers.
func dialWS(t *testing.T, url string, headers map[string]string) *websocket.Conn {
	t.Helper()
	header := http.Header{}
	for key, value := range headers {
		header.Set(key, value)
	}
	conn, res, err := websocket.DefaultDialer.Dial(url, header)
	if err != nil {
		status := 0
		if res != nil {
			status = res.StatusCode
		}
		t.Fatalf("dial %s: %v (status %d)", url, err, status)
	}
	return conn
}

// portOpen reports whether something is listening on addr.
func portOpen(t *testing.T, addr string) bool {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

var _ = json.Marshal
var _ = store.ErrNotFound
var _ = fmt.Sprintf
