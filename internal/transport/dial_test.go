package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/config"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/model"
)

// ---------------------------------------------------------------- fake peers

// fakeImplServer mimics a QQ-side implementation's *forward* WebSocket server:
// the hub dials it, receives events from it and sends API calls to it.
type fakeImplServer struct {
	ts *httptest.Server

	mu    sync.Mutex
	conns []*websocket.Conn
	recv  chan []byte
	total int
}

func newFakeImplServer(t *testing.T) *fakeImplServer {
	t.Helper()
	server := &fakeImplServer{recv: make(chan []byte, 256)}
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server.ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		server.mu.Lock()
		server.conns = append(server.conns, conn)
		server.total++
		server.mu.Unlock()

		for {
			mt, data, err := conn.ReadMessage()
			if err != nil {
				_ = conn.Close()
				return
			}
			if mt == websocket.TextMessage {
				select {
				case server.recv <- data:
				default:
				}
			}
		}
	}))
	t.Cleanup(server.ts.Close)
	return server
}

func (s *fakeImplServer) url(path string) string {
	return "ws" + strings.TrimPrefix(s.ts.URL, "http") + path
}

func (s *fakeImplServer) push(t *testing.T, frame string) {
	t.Helper()
	s.mu.Lock()
	conns := append([]*websocket.Conn(nil), s.conns...)
	s.mu.Unlock()
	if len(conns) == 0 {
		t.Fatal("no implementation connection to push to")
	}
	for _, conn := range conns {
		if err := conn.WriteMessage(websocket.TextMessage, []byte(frame)); err != nil {
			t.Fatalf("push: %v", err)
		}
	}
}

// nextAction waits for an API call and returns it with its rewritten echo.
func (s *fakeImplServer) nextAction(t *testing.T, timeout time.Duration) ([]byte, string) {
	t.Helper()
	select {
	case frame := <-s.recv:
		var probe struct {
			Action string          `json:"action"`
			Echo   json.RawMessage `json:"echo"`
		}
		if err := json.Unmarshal(frame, &probe); err != nil {
			t.Fatalf("decode action %s: %v", frame, err)
		}
		var key string
		_ = json.Unmarshal(probe.Echo, &key)
		return frame, key
	case <-time.After(timeout):
		t.Fatal("timed out waiting for an API call on the dialed connection")
		return nil, ""
	}
}

func (s *fakeImplServer) reply(t *testing.T, key, data string) {
	t.Helper()
	frame := fmt.Sprintf(`{"status":"ok","retcode":0,"data":%s,"echo":%q}`, data, key)
	s.push(t, frame)
}

func (s *fakeImplServer) connections() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.total
}

func (s *fakeImplServer) dropAll() {
	s.mu.Lock()
	conns := append([]*websocket.Conn(nil), s.conns...)
	s.conns = nil
	s.mu.Unlock()
	for _, conn := range conns {
		_ = conn.Close()
	}
}

// fakeBotServer mimics a Bot framework that listens for the implementation.
type fakeBotServer struct {
	ts *httptest.Server

	mu     sync.Mutex
	conns  []*websocket.Conn
	events chan []byte
}

func newFakeBotServer(t *testing.T) *fakeBotServer {
	t.Helper()
	server := &fakeBotServer{events: make(chan []byte, 256)}
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server.ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		server.mu.Lock()
		server.conns = append(server.conns, conn)
		server.mu.Unlock()

		for {
			mt, data, err := conn.ReadMessage()
			if err != nil {
				_ = conn.Close()
				return
			}
			if mt == websocket.TextMessage {
				select {
				case server.events <- data:
				default:
				}
			}
		}
	}))
	t.Cleanup(server.ts.Close)
	return server
}

func (s *fakeBotServer) url(path string) string {
	return "ws" + strings.TrimPrefix(s.ts.URL, "http") + path
}

func (s *fakeBotServer) nextEvent(t *testing.T, timeout time.Duration) []byte {
	t.Helper()
	select {
	case frame := <-s.events:
		return frame
	case <-time.After(timeout):
		t.Fatal("timed out waiting for an event pushed by the relay")
		return nil
	}
}

func (s *fakeBotServer) write(t *testing.T, frame string) {
	t.Helper()
	s.mu.Lock()
	conns := append([]*websocket.Conn(nil), s.conns...)
	s.mu.Unlock()
	if len(conns) == 0 {
		t.Fatal("no bot connection for the relay to dial")
	}
	if err := conns[0].WriteMessage(websocket.TextMessage, []byte(frame)); err != nil {
		t.Fatalf("write from bot: %v", err)
	}
}

func (s *fakeBotServer) connections() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.conns)
}

// ------------------------------------------------------------------- dialer

func (e *upstreamEnv) startDialer(t *testing.T, specs ...EndpointSpec) *DialManager {
	t.Helper()
	manager := NewDialManager(e.cfg, e.store, e.hub, slog.New(slog.DiscardHandler))
	manager.Start(e.ctx, specs)
	t.Cleanup(manager.Stop)
	return manager
}

func dialReconnect(min, max time.Duration) config.Reconnect {
	return config.Reconnect{Min: config.Duration(min), Max: config.Duration(max), Jitter: 0.1}
}

func endpointState(t *testing.T, manager *DialManager, name string) EndpointState {
	t.Helper()
	for _, state := range manager.States() {
		if state.Name == name {
			return state
		}
	}
	t.Fatalf("endpoint %q not found in %+v", name, manager.States())
	return EndpointState{}
}

// -------------------------------------------------------------------- tests

func TestUpstreamDialConnectsAndRoutes(t *testing.T) {
	env := newUpstreamEnv(t, func(cfg *config.Config) { cfg.OneBot.UpstreamWS.UnknownAccountPolicy = "auto" })
	account := env.createAccount(t, "80001")
	bot, token := env.createBot(t, "dial-bot")
	env.bind(t, bot.ID, account.ID, true, "{}")

	botConn := env.dialBot(t, token, "")
	waitFor(t, "downstream connection", 3*time.Second, func() bool { return env.hub.DownstreamCount() == 1 })

	impl := newFakeImplServer(t)
	manager := env.startDialer(t, EndpointSpec{
		Name: "qq-80001", Kind: config.KindUpstreamDial, URL: impl.url("/"),
		AccountHint: "80001", Enabled: true, Source: "database",
		Reconnect: dialReconnect(50*time.Millisecond, 300*time.Millisecond),
	})

	waitFor(t, "account attached through the dialed connection", 5*time.Second, func() bool {
		_, ok := env.hub.Registry().Session("80001")
		return ok
	})
	waitFor(t, "endpoint reports online", 3*time.Second, func() bool {
		return endpointState(t, manager, "qq-80001").State == "online"
	})

	// Events flow from the dialed implementation to the bound Bot.
	impl.push(t, `{"post_type":"message","message_type":"private","self_id":80001,"user_id":1,"message":"dial-event"}`)
	got := string(readBotFrame(t, botConn, 3*time.Second))
	if !strings.Contains(got, "dial-event") {
		t.Fatalf("event was not relayed: %s", got)
	}

	// API calls flow from the Bot to the dialed implementation.
	action := `{"action":"send_msg","params":{"message":"hi"},"echo":"d1"}`
	if err := botConn.WriteMessage(websocket.TextMessage, []byte(action)); err != nil {
		t.Fatal(err)
	}
	frame, key := impl.nextAction(t, 3*time.Second)
	if !strings.Contains(string(frame), `"action":"send_msg"`) {
		t.Fatalf("unexpected action frame: %s", frame)
	}
	if !strings.HasPrefix(key, "hub@") {
		t.Fatalf("echo was not rewritten before crossing the dialed connection: %q", key)
	}
	impl.reply(t, key, `{"message_id":7}`)
	if status, _, _, echo := readBotResponse(t, botConn, 3*time.Second); status != "ok" || echo != "d1" {
		t.Fatalf("response did not come back over the dialed link: %s echo=%s", status, echo)
	}
}

func TestDownstreamDialPushesEventsAndReceivesActions(t *testing.T) {
	env := newUpstreamEnv(t, func(cfg *config.Config) { cfg.OneBot.UpstreamWS.UnknownAccountPolicy = "auto" })
	account := env.createAccount(t, "80002")
	bot, _ := env.createBot(t, "downdial-bot")
	env.bind(t, bot.ID, account.ID, true, "{}")

	impl := env.dialImpl(t, "80002")
	botServer := newFakeBotServer(t)
	manager := env.startDialer(t, EndpointSpec{
		Name: "bot-nonebot", Kind: config.KindDownstreamDial, URL: botServer.url("/onebot/v11/ws"),
		BotID: bot.ID, BotName: bot.Name, Enabled: true, Source: "database",
		Reconnect: dialReconnect(50*time.Millisecond, 300*time.Millisecond),
	})

	waitFor(t, "relay dialed the Bot server", 5*time.Second, func() bool {
		return env.hub.DownstreamCount() == 1 && botServer.connections() == 1
	})
	waitFor(t, "endpoint reports online", 3*time.Second, func() bool {
		return endpointState(t, manager, "bot-nonebot").State == "online"
	})

	// The QQ side pushes an event: the dialed Bot must receive it.
	impl.write(`{"post_type":"message","message_type":"private","self_id":80002,"user_id":2,"message":"pushed"}`)
	if got := string(botServer.nextEvent(t, 3*time.Second)); !strings.Contains(got, "pushed") {
		t.Fatalf("event was not pushed to the dialed Bot: %s", got)
	}

	// The dialed Bot calls an API: the QQ side must receive it.
	botServer.write(t, `{"action":"get_status","echo":"s1"}`)
	if _, key := impl.readAction(3 * time.Second); key == "" {
		t.Fatal("action from the dialed Bot did not reach the implementation")
	}
}

func TestDialReconnectsAfterTheImplementationDrops(t *testing.T) {
	env := newUpstreamEnv(t, func(cfg *config.Config) { cfg.OneBot.UpstreamWS.UnknownAccountPolicy = "auto" })
	env.createAccount(t, "80003")

	impl := newFakeImplServer(t)
	manager := env.startDialer(t, EndpointSpec{
		Name: "qq-80003", Kind: config.KindUpstreamDial, URL: impl.url("/"),
		AccountHint: "80003", Enabled: true, Source: "database",
		Reconnect: dialReconnect(50*time.Millisecond, 200*time.Millisecond),
	})

	waitFor(t, "first connection", 5*time.Second, func() bool { return impl.connections() == 1 })
	waitFor(t, "online state", 3*time.Second, func() bool {
		return endpointState(t, manager, "qq-80003").State == "online"
	})

	// The implementation goes away: the worker must come back on its own.
	impl.dropAll()
	waitFor(t, "automatic reconnect", 8*time.Second, func() bool { return impl.connections() >= 2 })
	waitFor(t, "online again", 5*time.Second, func() bool {
		return endpointState(t, manager, "qq-80003").State == "online"
	})
}

func TestForceReconnectEndpoint(t *testing.T) {
	env := newUpstreamEnv(t, func(cfg *config.Config) { cfg.OneBot.UpstreamWS.UnknownAccountPolicy = "auto" })
	env.createAccount(t, "80004")

	impl := newFakeImplServer(t)
	manager := env.startDialer(t, EndpointSpec{
		Name: "qq-80004", Kind: config.KindUpstreamDial, URL: impl.url("/"),
		AccountHint: "80004", Enabled: true, Source: "database",
		Reconnect: dialReconnect(50*time.Millisecond, 200*time.Millisecond),
	})
	waitFor(t, "first connection", 5*time.Second, func() bool { return impl.connections() == 1 })

	if !manager.ForceReconnect("qq-80004") {
		t.Fatal("ForceReconnect should report success for a known endpoint")
	}
	waitFor(t, "second connection after the forced reconnect", 5*time.Second, func() bool {
		return impl.connections() >= 2
	})
	if manager.ForceReconnect("nope") {
		t.Fatal("ForceReconnect must fail for an unknown endpoint")
	}
}

func TestDisabledEndpointDoesNotDial(t *testing.T) {
	env := newUpstreamEnv(t, nil)
	impl := newFakeImplServer(t)
	manager := env.startDialer(t, EndpointSpec{
		Name: "off", Kind: config.KindUpstreamDial, URL: impl.url("/"),
		AccountHint: "80005", Enabled: false, Source: "database",
	})
	time.Sleep(300 * time.Millisecond)
	if impl.connections() != 0 {
		t.Fatalf("a disabled endpoint dialed %d times", impl.connections())
	}
	if state := endpointState(t, manager, "off"); state.State != "disabled" {
		t.Fatalf("state = %q, want disabled", state.State)
	}
}

func TestLoadEndpointSpecsMergesConfigAndDatabase(t *testing.T) {
	ctx := context.Background()
	env := newUpstreamEnv(t, nil)

	bot, _ := env.createBot(t, "spec-bot")
	cfg := config.Default()
	enabled := true
	cfg.Endpoints = []config.Endpoint{
		{Name: "from-config", Kind: config.KindUpstreamDial, URL: "ws://127.0.0.1:6700/", AccountHint: "90001", Enabled: &enabled},
		{Name: "overridden", Kind: config.KindUpstreamDial, URL: "ws://old/", AccountHint: "1"},
		// config.yaml refers to a Bot by name; the loader resolves it.
		{Name: "config-bot", Kind: config.KindDownstreamDial, URL: "ws://127.0.0.1:8080/onebot/v11/ws", BotName: bot.Name},
		// ... and an unknown name is reported rather than silently dialed.
		{Name: "ghost-bot", Kind: config.KindDownstreamDial, URL: "ws://127.0.0.1:8080/onebot/v11/ws", BotName: "nope"},
	}

	botID := bot.ID
	if _, err := env.store.CreateEndpoint(ctx, model.Endpoint{
		Name: "from-db", Kind: config.KindDownstreamDial, URL: "ws://127.0.0.1:8080/onebot/v11/ws",
		BotID: &botID, Mode: "universal", Enabled: true,
		Reconnect: []byte(`{"min":"2s","max":"30s","jitter":0.25}`),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := env.store.CreateEndpoint(ctx, model.Endpoint{
		Name: "overridden", Kind: config.KindUpstreamDial, URL: "ws://new/", AccountHint: "2",
		Mode: "split", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	specs := LoadEndpointSpecs(ctx, cfg, env.store, slog.New(slog.DiscardHandler))
	byName := map[string]EndpointSpec{}
	for _, spec := range specs {
		byName[spec.Name] = spec
	}
	// 4 from config.yaml (one of them overriding, one referencing an unknown
	// Bot) plus 1 from the database.
	if len(specs) != 5 {
		t.Fatalf("specs = %d, want 5: %+v", len(specs), specs)
	}
	if spec := byName["config-bot"]; spec.BotID != bot.ID || spec.BotName != bot.Name {
		t.Fatalf("config Bot name not resolved: %+v", spec)
	}
	if spec, ok := byName["ghost-bot"]; !ok || spec.BotID != 0 {
		t.Fatalf("a config endpoint with an unknown Bot should stay unresolved: %+v", spec)
	}
	if spec := byName["from-config"]; spec.Source != "config" || spec.URL != "ws://127.0.0.1:6700/" {
		t.Fatalf("config endpoint not carried over: %+v", spec)
	}
	// The database row wins on a name clash.
	if spec := byName["overridden"]; spec.Source != "database" || spec.URL != "ws://new/" || spec.Mode != "split" {
		t.Fatalf("database override not applied: %+v", spec)
	}
	dbSpec := byName["from-db"]
	if dbSpec.BotID != bot.ID || dbSpec.BotName != bot.Name {
		t.Fatalf("bot not resolved: %+v", dbSpec)
	}
	if dbSpec.Reconnect.Min.Std() != 2*time.Second || dbSpec.Reconnect.Max.Std() != 30*time.Second || dbSpec.Reconnect.Jitter != 0.25 {
		t.Fatalf("reconnect not parsed: %+v", dbSpec.Reconnect)
	}
	if dbSpec.DBID == 0 {
		t.Fatal("database endpoints must carry their id so the UI can edit them")
	}
}
