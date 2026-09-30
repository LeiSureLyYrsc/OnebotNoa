package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/auth"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/config"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/model"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/store"
)

// ---------------------------------------------------------------- test setup

func (e *upstreamEnv) createAccount(t *testing.T, selfID string) model.Account {
	t.Helper()
	acc, err := e.store.CreateAccount(context.Background(), selfID, "acc-"+selfID, "test")
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	return acc
}

func (e *upstreamEnv) createBot(t *testing.T, name string) (model.Bot, string) {
	t.Helper()
	plain, hash, err := auth.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	bot, err := e.store.CreateBot(context.Background(), name, hash, "")
	if err != nil {
		t.Fatalf("create bot: %v", err)
	}
	return bot, plain
}

func (e *upstreamEnv) bind(t *testing.T, botID, accountID int64, isDefault bool, scope string) model.Binding {
	t.Helper()
	b, err := e.store.CreateBinding(context.Background(), model.Binding{
		BotID: botID, AccountID: accountID, Priority: 100, IsDefault: isDefault,
		Enabled: true, Scope: json.RawMessage(scope),
	})
	if err != nil {
		t.Fatalf("create binding: %v", err)
	}
	e.hub.InvalidateBindings()
	return b
}

// fakeImpl is a QQ-side implementation connected to the upstream endpoint.
type fakeImpl struct {
	t    *testing.T
	conn *websocket.Conn
	mu   sync.Mutex
	got  [][]byte
}

func (e *upstreamEnv) dialImpl(t *testing.T, selfID string) *fakeImpl {
	t.Helper()
	conn := e.dialOK(t, map[string]string{
		"X-Self-ID":     selfID,
		"X-Client-Role": "Universal",
		"Authorization": "Bearer " + bootstrapToken,
	})
	impl := &fakeImpl{t: t, conn: conn}
	waitFor(t, "impl session "+selfID, 3*time.Second, func() bool {
		_, ok := e.hub.Registry().Session(selfID)
		return ok
	})
	return impl
}

// readAction reads frames until one carrying an "action" field arrives.
func (f *fakeImpl) readAction(timeout time.Duration) ([]byte, string) {
	f.t.Helper()
	_ = f.conn.SetReadDeadline(time.Now().Add(timeout))
	for {
		_, data, err := f.conn.ReadMessage()
		if err != nil {
			f.t.Fatalf("impl read: %v", err)
		}
		var probe struct {
			Action string          `json:"action"`
			Echo   json.RawMessage `json:"echo"`
		}
		if json.Unmarshal(data, &probe) == nil && probe.Action != "" {
			f.mu.Lock()
			f.got = append(f.got, data)
			f.mu.Unlock()
			var key string
			_ = json.Unmarshal(probe.Echo, &key)
			return data, key
		}
	}
}

func (f *fakeImpl) reply(echo string, data string) {
	f.t.Helper()
	frame := fmt.Sprintf(`{"status":"ok","retcode":0,"data":%s,"echo":%q}`, data, echo)
	if err := f.conn.WriteMessage(websocket.TextMessage, []byte(frame)); err != nil {
		f.t.Fatalf("impl reply: %v", err)
	}
}

func (f *fakeImpl) write(raw string) {
	f.t.Helper()
	if err := f.conn.WriteMessage(websocket.TextMessage, []byte(raw)); err != nil {
		f.t.Fatalf("impl write: %v", err)
	}
}

// writeOK reports whether the frame was delivered. Used by stress tests where
// the relay may legitimately close the upstream connection midway.
func (f *fakeImpl) writeOK(raw string) bool {
	return f.conn.WriteMessage(websocket.TextMessage, []byte(raw)) == nil
}

// dialBot connects a Bot to the downstream endpoint.
func (e *upstreamEnv) dialBot(t *testing.T, token, suffix string) *websocket.Conn {
	t.Helper()
	path := "/onebot/v11/bot/ws/" + token + suffix
	url := "ws" + strings.TrimPrefix(e.ts.URL, "http") + path
	conn, resp, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		t.Fatalf("bot dial: %v (status %d)", err, status)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// readBotFrame reads one frame with a deadline, skipping the meta events the
// relay synthesises on its own (lifecycle/heartbeat) so a test always asserts on
// the traffic it actually triggered.
func readBotFrame(t *testing.T, conn *websocket.Conn, timeout time.Duration) []byte {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		data := readBotFrameRaw(t, conn, time.Until(deadline))
		if bytes.Contains(data, []byte(`"post_type":"meta_event"`)) {
			if time.Now().After(deadline) {
				t.Fatal("only synthesised meta events arrived before the deadline")
			}
			continue
		}
		return data
	}
}

// readBotFrameRaw returns the very next frame, including meta events.
func readBotFrameRaw(t *testing.T, conn *websocket.Conn, timeout time.Duration) []byte {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("bot read: %v", err)
	}
	return data
}

// expectNoBotFrame asserts the socket stays quiet for the given window.
func expectNoBotFrame(t *testing.T, conn *websocket.Conn, window time.Duration) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(window))
	if _, data, err := conn.ReadMessage(); err == nil {
		t.Fatalf("unexpected frame: %s", data)
	}
}

// ---------------------------------------------------------------- the tests

func TestEventIsMulticastToEveryBoundBot(t *testing.T) {
	env := newUpstreamEnv(t, func(cfg *config.Config) { cfg.OneBot.UpstreamWS.UnknownAccountPolicy = "auto" })
	acc := env.createAccount(t, "10001")
	botA, tokenA := env.createBot(t, "bot-a")
	botB, tokenB := env.createBot(t, "bot-b")
	env.bind(t, botA.ID, acc.ID, true, "{}")
	env.bind(t, botB.ID, acc.ID, true, "{}")

	impl := env.dialImpl(t, "10001")
	connA := env.dialBot(t, tokenA, "")
	connB := env.dialBot(t, tokenB, "")
	waitFor(t, "two downstream connections", 3*time.Second, func() bool { return env.hub.DownstreamCount() == 2 })

	frame := `{  "post_type":"message","message_type":"group","self_id":10001,"user_id":42,"group_id":7,"message":"hello"  }`
	impl.write(frame)

	gotA := string(readBotFrame(t, connA, 3*time.Second))
	gotB := string(readBotFrame(t, connB, 3*time.Second))
	if gotA != frame || gotB != frame {
		t.Fatalf("multicast was not byte-exact:\n A: %s\n B: %s\nwant: %s", gotA, gotB, frame)
	}
}

func TestEventScopeFilters(t *testing.T) {
	env := newUpstreamEnv(t, func(cfg *config.Config) { cfg.OneBot.UpstreamWS.UnknownAccountPolicy = "auto" })
	acc := env.createAccount(t, "10002")
	bot, token := env.createBot(t, "scoped")
	env.bind(t, bot.ID, acc.ID, true, `{"post_types":["message"],"exclude_self":true}`)

	impl := env.dialImpl(t, "10002")
	conn := env.dialBot(t, token, "")
	waitFor(t, "downstream connection", 3*time.Second, func() bool { return env.hub.DownstreamCount() == 1 })

	// Notice events are filtered out by post_types.
	impl.write(`{"post_type":"notice","notice_type":"group_increase","self_id":10002,"user_id":1,"group_id":5}`)
	// Messages from the account itself are filtered by exclude_self.
	impl.write(`{"post_type":"message","message_type":"group","self_id":10002,"user_id":10002,"group_id":5,"message":"self"}`)
	// This one passes.
	impl.write(`{"post_type":"message","message_type":"group","self_id":10002,"user_id":7,"group_id":5,"message":"ok"}`)

	got := string(readBotFrame(t, conn, 3*time.Second))
	if !strings.Contains(got, `"ok"`) {
		t.Fatalf("expected only the passing message, got %s", got)
	}
	expectNoBotFrame(t, conn, 300*time.Millisecond)
}

func TestSameEchoFromTwoBotsIsIsolated(t *testing.T) {
	env := newUpstreamEnv(t, func(cfg *config.Config) { cfg.OneBot.UpstreamWS.UnknownAccountPolicy = "auto" })
	acc := env.createAccount(t, "10003")
	botA, tokenA := env.createBot(t, "echo-a")
	botB, tokenB := env.createBot(t, "echo-b")
	env.bind(t, botA.ID, acc.ID, true, "{}")
	env.bind(t, botB.ID, acc.ID, true, "{}")

	impl := env.dialImpl(t, "10003")
	connA := env.dialBot(t, tokenA, "")
	connB := env.dialBot(t, tokenB, "")
	waitFor(t, "downstream connections", 3*time.Second, func() bool { return env.hub.DownstreamCount() == 2 })

	// Both Bots use the exact same echo value.
	actionA := `{"action":"send_msg","params":{"message":"from-a"},"echo":"dup"}`
	actionB := `{"action":"send_msg","params":{"message":"from-b"},"echo":"dup"}`
	if err := connA.WriteMessage(websocket.TextMessage, []byte(actionA)); err != nil {
		t.Fatal(err)
	}
	if err := connB.WriteMessage(websocket.TextMessage, []byte(actionB)); err != nil {
		t.Fatal(err)
	}

	// Arrival order is not deterministic, so map each rewritten echo back to its
	// Bot by inspecting the forwarded params.
	frame1, key1 := impl.readAction(3 * time.Second)
	frame2, key2 := impl.readAction(3 * time.Second)
	if key1 == key2 {
		t.Fatalf("relay must rewrite echo values, both were %q", key1)
	}
	if !strings.HasPrefix(key1, "hub@") || !strings.HasPrefix(key2, "hub@") {
		t.Fatalf("unexpected rewritten echo values: %q %q", key1, key2)
	}
	if !strings.Contains(string(frame1), "from-a") && !strings.Contains(string(frame2), "from-a") {
		t.Fatalf("neither forwarded frame came from bot A: %s | %s", frame1, frame2)
	}
	if !strings.Contains(string(frame1), "from-b") && !strings.Contains(string(frame2), "from-b") {
		t.Fatalf("neither forwarded frame came from bot B: %s | %s", frame1, frame2)
	}
	keyForA, keyForB := key1, key2
	if strings.Contains(string(frame1), "from-b") {
		keyForA, keyForB = key2, key1
	}

	// Replies must go back to the right Bot with the original echo restored.
	impl.reply(keyForA, `{"message_id":1,"message":"A"}`)
	impl.reply(keyForB, `{"message_id":2,"message":"B"}`)

	gotA := string(readBotFrame(t, connA, 3*time.Second))
	gotB := string(readBotFrame(t, connB, 3*time.Second))

	var respA, respB struct {
		Status string          `json:"status"`
		Data   json.RawMessage `json:"data"`
		Echo   string          `json:"echo"`
	}
	if err := json.Unmarshal([]byte(gotA), &respA); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(gotB), &respB); err != nil {
		t.Fatal(err)
	}
	if respA.Echo != "dup" || respB.Echo != "dup" {
		t.Fatalf("original echo not restored: %s / %s", gotA, gotB)
	}
	if !strings.Contains(string(respA.Data), "A") || !strings.Contains(string(respB.Data), "B") {
		t.Fatalf("responses were crossed:\nA: %s\nB: %s", gotA, gotB)
	}
	if env.hub.Actions().PendingCount() != 0 {
		t.Fatalf("pending actions left behind: %d", env.hub.Actions().PendingCount())
	}
}

func TestActionWithoutEchoGetsEchoRemovedOnReply(t *testing.T) {
	env := newUpstreamEnv(t, func(cfg *config.Config) { cfg.OneBot.UpstreamWS.UnknownAccountPolicy = "auto" })
	acc := env.createAccount(t, "10004")
	bot, token := env.createBot(t, "no-echo")
	env.bind(t, bot.ID, acc.ID, true, "{}")

	impl := env.dialImpl(t, "10004")
	conn := env.dialBot(t, token, "")
	waitFor(t, "downstream connection", 3*time.Second, func() bool { return env.hub.DownstreamCount() == 1 })

	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"action":"get_group_list"}`)); err != nil {
		t.Fatal(err)
	}
	_, key := impl.readAction(3 * time.Second)
	impl.reply(key, `{"online":true}`)

	got := string(readBotFrame(t, conn, 3*time.Second))
	if strings.Contains(got, "echo") {
		t.Fatalf("echo must be removed when the Bot never sent one: %s", got)
	}
	if !strings.Contains(got, `{"online":true}`) {
		t.Fatalf("data not preserved: %s", got)
	}
}

func TestActionTimeoutAnswersFailure(t *testing.T) {
	env := newUpstreamEnv(t, func(cfg *config.Config) {
		cfg.OneBot.UpstreamWS.UnknownAccountPolicy = "auto"
		cfg.Policy.ActionTimeout = config.Duration(250 * time.Millisecond)
	})
	acc := env.createAccount(t, "10005")
	bot, token := env.createBot(t, "slow")
	env.bind(t, bot.ID, acc.ID, true, "{}")

	_ = env.dialImpl(t, "10005")
	conn := env.dialBot(t, token, "")
	waitFor(t, "downstream connection", 3*time.Second, func() bool { return env.hub.DownstreamCount() == 1 })

	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"action":"send_msg","params":{"message":"x"},"echo":7}`)); err != nil {
		t.Fatal(err)
	}

	got := string(readBotFrame(t, conn, 3*time.Second))
	var resp struct {
		Status  string          `json:"status"`
		Retcode int             `json:"retcode"`
		Echo    json.RawMessage `json:"echo"`
		Wording string          `json:"wording"`
	}
	if err := json.Unmarshal([]byte(got), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Status != "failed" || resp.Retcode != 1200 {
		t.Fatalf("timeout response = %+v", resp)
	}
	if string(resp.Echo) != "7" {
		t.Fatalf("echo not restored on timeout: %s", resp.Echo)
	}
	if resp.Wording == "" {
		t.Fatal("failure must carry a wording field")
	}
	if env.hub.Actions().PendingCount() != 0 {
		t.Fatalf("timed-out action still pending: %d", env.hub.Actions().PendingCount())
	}
}

func TestStreamingReplyIsNotTruncated(t *testing.T) {
	env := newUpstreamEnv(t, func(cfg *config.Config) {
		cfg.OneBot.UpstreamWS.UnknownAccountPolicy = "auto"
		cfg.Policy.ActionTimeout = config.Duration(200 * time.Millisecond)
		cfg.Policy.StreamIdleTimeout = config.Duration(2 * time.Second)
	})
	acc := env.createAccount(t, "10006")
	bot, token := env.createBot(t, "streamer")
	env.bind(t, bot.ID, acc.ID, true, "{}")

	impl := env.dialImpl(t, "10006")
	conn := env.dialBot(t, token, "")
	waitFor(t, "downstream connection", 3*time.Second, func() bool { return env.hub.DownstreamCount() == 1 })

	if err := conn.WriteMessage(websocket.TextMessage,
		[]byte(`{"action":"download_file_stream","params":{"stream_id":"s1"},"echo":"s"}`)); err != nil {
		t.Fatal(err)
	}
	_, key := impl.readAction(3 * time.Second)

	// Three intermediate frames, each spaced beyond the normal action timeout:
	// the stream idle timer must keep the entry alive.
	for i := 0; i < 3; i++ {
		time.Sleep(120 * time.Millisecond)
		impl.write(fmt.Sprintf(`{"type":"stream","stream_id":"s1","chunk_index":%d,"echo":%q}`, i, key))
		got := string(readBotFrame(t, conn, 2*time.Second))
		// Replies are re-encoded when the relay restores the echo, so assert on
		// content rather than on byte order.
		if !strings.Contains(got, `"type":"stream"`) ||
			!strings.Contains(got, `"echo":"s"`) ||
			!strings.Contains(got, fmt.Sprintf(`"chunk_index":%d`, i)) {
			t.Fatalf("stream frame %d mangled: %s", i, got)
		}
	}
	impl.reply(key, `{"file_size":3}`)
	final := string(readBotFrame(t, conn, 2*time.Second))
	if !strings.Contains(final, `"status":"ok"`) {
		t.Fatalf("terminal frame missing: %s", final)
	}
	if strings.Contains(final, "hub@") {
		t.Fatalf("relay echo leaked to the Bot: %s", final)
	}
	if env.hub.Actions().PendingCount() != 0 {
		t.Fatalf("stream entry not released: %d", env.hub.Actions().PendingCount())
	}
}

func TestPendingCapRejectsExtraActions(t *testing.T) {
	env := newUpstreamEnv(t, func(cfg *config.Config) {
		cfg.OneBot.UpstreamWS.UnknownAccountPolicy = "auto"
		cfg.Policy.PendingPerConn = 2
		cfg.Policy.ActionTimeout = config.Duration(5 * time.Second)
	})
	acc := env.createAccount(t, "10007")
	bot, token := env.createBot(t, "flooder")
	env.bind(t, bot.ID, acc.ID, true, "{}")

	_ = env.dialImpl(t, "10007")
	conn := env.dialBot(t, token, "")
	waitFor(t, "downstream connection", 3*time.Second, func() bool { return env.hub.DownstreamCount() == 1 })

	for i := 0; i < 3; i++ {
		frame := fmt.Sprintf(`{"action":"send_msg","params":{"message":"%d"},"echo":%d}`, i, i)
		if err := conn.WriteMessage(websocket.TextMessage, []byte(frame)); err != nil {
			t.Fatal(err)
		}
	}

	got := string(readBotFrame(t, conn, 3*time.Second))
	if !strings.Contains(got, `"retcode":1200`) {
		t.Fatalf("third action should be refused, got %s", got)
	}
	if env.hub.Actions().PendingCount() != 2 {
		t.Fatalf("pending = %d, want 2", env.hub.Actions().PendingCount())
	}
}

func TestTargetResolutionRules(t *testing.T) {
	env := newUpstreamEnv(t, func(cfg *config.Config) { cfg.OneBot.UpstreamWS.UnknownAccountPolicy = "auto" })
	accA := env.createAccount(t, "10008")
	accB := env.createAccount(t, "10009")
	bot, token := env.createBot(t, "resolver")
	env.bind(t, bot.ID, accA.ID, true, "{}")
	env.bind(t, bot.ID, accB.ID, false, "{}")

	implA := env.dialImpl(t, "10008")
	implB := env.dialImpl(t, "10009")
	conn := env.dialBot(t, token, "")
	waitFor(t, "downstream connection", 3*time.Second, func() bool { return env.hub.DownstreamCount() == 1 })

	// 1. No hint and two bindings (one default) -> the default account.
	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"action":"get_group_list","echo":"d"}`)); err != nil {
		t.Fatal(err)
	}
	if _, key := implA.readAction(3 * time.Second); key == "" {
		t.Fatal("default binding did not route to account A")
	}

	// 2. frame-level self_id wins.
	if err := conn.WriteMessage(websocket.TextMessage,
		[]byte(`{"action":"get_group_list","self_id":10009,"echo":"f"}`)); err != nil {
		t.Fatal(err)
	}
	if _, key := implB.readAction(3 * time.Second); key == "" {
		t.Fatal("frame self_id did not route to account B")
	}

	// 3. params.self_id also works.
	if err := conn.WriteMessage(websocket.TextMessage,
		[]byte(`{"action":"get_group_list","params":{"self_id":10009},"echo":"p"}`)); err != nil {
		t.Fatal(err)
	}
	if _, key := implB.readAction(3 * time.Second); key == "" {
		t.Fatal("params.self_id did not route to account B")
	}

	// 4. An unbound account is refused with 1403.
	if err := conn.WriteMessage(websocket.TextMessage,
		[]byte(`{"action":"get_group_list","self_id":99999,"echo":"x"}`)); err != nil {
		t.Fatal(err)
	}
	got := string(readBotFrame(t, conn, 3*time.Second))
	if !strings.Contains(got, `"retcode":1403`) {
		t.Fatalf("unbound target should be 1403: %s", got)
	}
}

func TestAmbiguousTargetReturns1404(t *testing.T) {
	env := newUpstreamEnv(t, func(cfg *config.Config) { cfg.OneBot.UpstreamWS.UnknownAccountPolicy = "auto" })
	accA := env.createAccount(t, "10010")
	accB := env.createAccount(t, "10011")
	bot, token := env.createBot(t, "ambiguous")
	env.bind(t, bot.ID, accA.ID, false, "{}")
	env.bind(t, bot.ID, accB.ID, false, "{}")

	_ = env.dialImpl(t, "10010")
	_ = env.dialImpl(t, "10011")
	conn := env.dialBot(t, token, "")
	waitFor(t, "downstream connection", 3*time.Second, func() bool { return env.hub.DownstreamCount() == 1 })

	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"action":"get_group_list","echo":"a"}`)); err != nil {
		t.Fatal(err)
	}
	got := string(readBotFrame(t, conn, 3*time.Second))
	if !strings.Contains(got, `"retcode":1404`) {
		t.Fatalf("ambiguous target should be 1404: %s", got)
	}
	if !strings.Contains(got, `"echo":"a"`) {
		t.Fatalf("echo must be echoed back on failure: %s", got)
	}
}

func TestTransparentSingleAccountView(t *testing.T) {
	env := newUpstreamEnv(t, func(cfg *config.Config) { cfg.OneBot.UpstreamWS.UnknownAccountPolicy = "auto" })
	accA := env.createAccount(t, "10012")
	accB := env.createAccount(t, "10013")
	bot, token := env.createBot(t, "legacy")
	env.bind(t, bot.ID, accA.ID, false, "{}")
	env.bind(t, bot.ID, accB.ID, false, "{}")

	implA := env.dialImpl(t, "10012")
	implB := env.dialImpl(t, "10013")

	// The Bot asks for the transparent view of account A only.
	conn := env.dialBot(t, token, "/10012")
	waitFor(t, "downstream connection", 3*time.Second, func() bool { return env.hub.DownstreamCount() == 1 })

	// Actions need no self_id at all.
	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"action":"get_group_list","echo":"t"}`)); err != nil {
		t.Fatal(err)
	}
	if _, key := implA.readAction(3 * time.Second); key == "" {
		t.Fatal("transparent view did not route to the fixed account")
	}

	// Events of the other account must not leak into this view.
	implB.write(`{"post_type":"message","message_type":"private","self_id":10013,"user_id":1,"message":"other"}`)
	implA.write(`{"post_type":"message","message_type":"private","self_id":10012,"user_id":1,"message":"mine"}`)
	got := string(readBotFrame(t, conn, 3*time.Second))
	if !strings.Contains(got, `"mine"`) {
		t.Fatalf("expected the fixed account's event, got %s", got)
	}
	expectNoBotFrame(t, conn, 300*time.Millisecond)
}

func TestMalformedAndNonActionFrames(t *testing.T) {
	env := newUpstreamEnv(t, func(cfg *config.Config) { cfg.OneBot.UpstreamWS.UnknownAccountPolicy = "auto" })
	acc := env.createAccount(t, "10014")
	bot, token := env.createBot(t, "sloppy")
	env.bind(t, bot.ID, acc.ID, true, "{}")

	impl := env.dialImpl(t, "10014")
	conn := env.dialBot(t, token, "")
	waitFor(t, "downstream connection", 3*time.Second, func() bool { return env.hub.DownstreamCount() == 1 })

	// An action frame without an action name is refused.
	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"action":"","echo":"m"}`)); err != nil {
		t.Fatal(err)
	}
	got := string(readBotFrame(t, conn, 3*time.Second))
	if !strings.Contains(got, `"retcode":1400`) {
		t.Fatalf("empty action should be 1400: %s", got)
	}

	// An event pushed by a Bot is ignored without breaking the connection.
	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"post_type":"message","self_id":1}`)); err != nil {
		t.Fatal(err)
	}
	expectNoBotFrame(t, conn, 200*time.Millisecond)

	// The connection is still usable afterwards.
	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"action":"get_group_list","echo":"still-alive"}`)); err != nil {
		t.Fatal(err)
	}
	if _, key := impl.readAction(2 * time.Second); key == "" {
		t.Fatal("connection did not recover")
	}
}

func TestDownstreamRequiresValidToken(t *testing.T) {
	env := newUpstreamEnv(t, nil)

	_, resp, err := env.dial(t, "/onebot/v11/bot/ws", nil)
	if err == nil {
		t.Fatal("dial without a token must fail")
	}
	if resp == nil || resp.StatusCode != 401 {
		t.Fatalf("status = %v, want 401", resp)
	}

	_, resp, err = env.dial(t, "/onebot/v11/bot/ws/not-a-real-token", nil)
	if err == nil {
		t.Fatal("dial with an unknown token must fail")
	}
	if resp == nil || resp.StatusCode != 401 {
		t.Fatalf("status = %v, want 401", resp)
	}

	// A disabled Bot is refused with 403.
	bot, token := env.createBot(t, "disabled-bot")
	bot.Enabled = false
	if err := env.store.UpdateBot(context.Background(), bot); err != nil {
		t.Fatal(err)
	}
	_, resp, err = env.dial(t, "/onebot/v11/bot/ws/"+token, nil)
	if err == nil {
		t.Fatal("dial for a disabled bot must fail")
	}
	if resp == nil || resp.StatusCode != 403 {
		t.Fatalf("status = %v, want 403", resp)
	}
}

func TestActionToOfflineAccountFailsFast(t *testing.T) {
	env := newUpstreamEnv(t, nil)
	acc := env.createAccount(t, "10015")
	bot, token := env.createBot(t, "offline-target")
	env.bind(t, bot.ID, acc.ID, true, "{}")

	// No upstream connection at all: the action must fail immediately.
	conn := env.dialBot(t, token, "")
	waitFor(t, "downstream connection", 3*time.Second, func() bool { return env.hub.DownstreamCount() == 1 })

	if err := conn.WriteMessage(websocket.TextMessage,
		[]byte(`{"action":"send_msg","params":{"message":"x"},"echo":"off"}`)); err != nil {
		t.Fatal(err)
	}
	got := string(readBotFrame(t, conn, 3*time.Second))
	if !strings.Contains(got, `"retcode":1200`) || !strings.Contains(got, `离线`) {
		t.Fatalf("offline action should fail fast with a clear reason: %s", got)
	}
}

var _ = store.ErrNotFound
