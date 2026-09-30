package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/config"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/hub"
)

// readBotResponse reads one frame and decodes the relay's response shape.
func readBotResponse(t *testing.T, conn *websocket.Conn, timeout time.Duration) (status string, retcode int, wording string, echo string) {
	t.Helper()
	raw := readBotFrame(t, conn, timeout)
	var resp struct {
		Status  string          `json:"status"`
		Retcode int             `json:"retcode"`
		Wording string          `json:"wording"`
		Echo    json.RawMessage `json:"echo"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("decode response %s: %v", raw, err)
	}
	// The relay restores the Bot's original echo, which may be any JSON value.
	echo = strings.Trim(string(resp.Echo), `"`)
	return resp.Status, resp.Retcode, resp.Wording, echo
}

func TestAccountRateLimitIsSharedAcrossBots(t *testing.T) {
	env := newUpstreamEnv(t, func(cfg *config.Config) {
		cfg.OneBot.UpstreamWS.UnknownAccountPolicy = "auto"
		// One message per second for the whole account, shared by every Bot.
		cfg.Policy.PerAccountRate = config.RateLimit{Rate: 0.5, Burst: 1}
		cfg.Policy.PerBotRate = config.RateLimit{} // per-Bot quota off for this test
	})

	acc := env.createAccount(t, "70001")
	botA, tokenA := env.createBot(t, "rate-a")
	botB, tokenB := env.createBot(t, "rate-b")
	env.bind(t, botA.ID, acc.ID, true, "{}")
	env.bind(t, botB.ID, acc.ID, true, "{}")

	impl := env.dialImpl(t, "70001")
	connA := env.dialBot(t, tokenA, "")
	connB := env.dialBot(t, tokenB, "")
	waitFor(t, "two downstream connections", 3*time.Second, func() bool { return env.hub.DownstreamCount() == 2 })

	if err := connA.WriteMessage(websocket.TextMessage, []byte(`{"action":"send_msg","params":{"message":"a"},"echo":"a"}`)); err != nil {
		t.Fatal(err)
	}
	// The first message consumes the only token in the burst.
	if _, key := impl.readAction(3 * time.Second); key == "" {
		t.Fatal("the first action should reach the implementation")
	}

	if err := connB.WriteMessage(websocket.TextMessage, []byte(`{"action":"send_msg","params":{"message":"b"},"echo":"b"}`)); err != nil {
		t.Fatal(err)
	}
	status, retcode, wording, echo := readBotResponse(t, connB, 3*time.Second)
	if status != "failed" || retcode != 1200 {
		t.Fatalf("the second Bot should be limited, got status=%s retcode=%d wording=%s", status, retcode, wording)
	}
	if !strings.Contains(wording, "全局限速") {
		t.Fatalf("wording should explain the account-wide limit: %q", wording)
	}
	if echo != "b" {
		t.Fatalf("echo = %q, want b", echo)
	}

	// Nothing else reached the implementation.
	expectNoImplAction(t, impl, 300*time.Millisecond)

	account, bot, _ := env.policy.Limiter().Counters()
	if account == 0 {
		t.Fatal("the account-level limit was not counted")
	}
	if bot != 0 {
		t.Fatalf("the per-Bot quota is disabled in this test but counted %d", bot)
	}
}

func TestReadOnlyActionsBypassTheAccountBucket(t *testing.T) {
	env := newUpstreamEnv(t, func(cfg *config.Config) {
		cfg.OneBot.UpstreamWS.UnknownAccountPolicy = "auto"
		cfg.Policy.PerAccountRate = config.RateLimit{Rate: 0.1, Burst: 0}
		cfg.Policy.PerBotRate = config.RateLimit{}
	})
	acc := env.createAccount(t, "70002")
	bot, token := env.createBot(t, "reader")
	env.bind(t, bot.ID, acc.ID, true, "{}")

	impl := env.dialImpl(t, "70002")
	conn := env.dialBot(t, token, "")
	waitFor(t, "downstream connection", 3*time.Second, func() bool { return env.hub.DownstreamCount() == 1 })

	for i := 0; i < 5; i++ {
		frame := fmt.Sprintf(`{"action":"get_status","echo":%d}`, i)
		if err := conn.WriteMessage(websocket.TextMessage, []byte(frame)); err != nil {
			t.Fatal(err)
		}
		if _, key := impl.readAction(2 * time.Second); key == "" {
			t.Fatalf("read-only action %d was throttled", i)
		}
	}
}

func TestActionPolicyDenyList(t *testing.T) {
	env := newUpstreamEnv(t, func(cfg *config.Config) { cfg.OneBot.UpstreamWS.UnknownAccountPolicy = "auto" })
	acc := env.createAccount(t, "70003")
	bot, token := env.createBot(t, "restricted")
	bot.ActionPolicy = []byte(`{"deny":["send_msg","set_group_*"]}`)
	if err := env.store.UpdateBot(context.Background(), bot); err != nil {
		t.Fatal(err)
	}
	env.bind(t, bot.ID, acc.ID, true, "{}")

	impl := env.dialImpl(t, "70003")
	conn := env.dialBot(t, token, "")
	waitFor(t, "downstream connection", 3*time.Second, func() bool { return env.hub.DownstreamCount() == 1 })

	// Denied by exact name.
	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"action":"send_msg","params":{"message":"x"},"echo":1}`)); err != nil {
		t.Fatal(err)
	}
	status, retcode, wording, _ := readBotResponse(t, conn, 3*time.Second)
	if status != "failed" || retcode != 1403 || !strings.Contains(wording, "黑名单") {
		t.Fatalf("deny list not enforced: %s %d %s", status, retcode, wording)
	}

	// Denied by wildcard.
	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"action":"set_group_ban","params":{"group_id":1},"echo":2}`)); err != nil {
		t.Fatal(err)
	}
	if _, retcode, _, _ := readBotResponse(t, conn, 3*time.Second); retcode != 1403 {
		t.Fatalf("wildcard deny not enforced, retcode %d", retcode)
	}

	// Read-only actions are still allowed.
	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"action":"get_status","echo":3}`)); err != nil {
		t.Fatal(err)
	}
	if _, key := impl.readAction(3 * time.Second); key == "" {
		t.Fatal("read-only action should still be forwarded")
	}
}

func TestActionPolicyAllowList(t *testing.T) {
	env := newUpstreamEnv(t, func(cfg *config.Config) { cfg.OneBot.UpstreamWS.UnknownAccountPolicy = "auto" })
	acc := env.createAccount(t, "70004")
	bot, token := env.createBot(t, "whitelisted")
	bot.ActionPolicy = []byte(`{"allow":["send_msg"]}`)
	if err := env.store.UpdateBot(context.Background(), bot); err != nil {
		t.Fatal(err)
	}
	env.bind(t, bot.ID, acc.ID, true, "{}")

	impl := env.dialImpl(t, "70004")
	conn := env.dialBot(t, token, "")
	waitFor(t, "downstream connection", 3*time.Second, func() bool { return env.hub.DownstreamCount() == 1 })

	// Not on the list -> refused, even though it is read-only.
	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"action":"get_status","echo":"no"}`)); err != nil {
		t.Fatal(err)
	}
	if _, retcode, wording, _ := readBotResponse(t, conn, 3*time.Second); retcode != 1403 || !strings.Contains(wording, "白名单") {
		t.Fatalf("allow list not enforced: retcode %d wording %s", retcode, wording)
	}

	// On the list -> forwarded.
	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"action":"send_msg","params":{"message":"ok"},"echo":"yes"}`)); err != nil {
		t.Fatal(err)
	}
	if _, key := impl.readAction(3 * time.Second); key == "" {
		t.Fatal("allowed action was not forwarded")
	}
}

func TestPerBotConcurrencyCap(t *testing.T) {
	env := newUpstreamEnv(t, func(cfg *config.Config) {
		cfg.OneBot.UpstreamWS.UnknownAccountPolicy = "auto"
		cfg.Policy.PerAccountRate = config.RateLimit{}
		cfg.Policy.PerBotRate = config.RateLimit{Concurrency: 1}
		cfg.Policy.ActionTimeout = config.Duration(5 * time.Second)
	})
	acc := env.createAccount(t, "70005")
	bot, token := env.createBot(t, "serial")
	env.bind(t, bot.ID, acc.ID, true, "{}")

	impl := env.dialImpl(t, "70005")
	conn := env.dialBot(t, token, "")
	waitFor(t, "downstream connection", 3*time.Second, func() bool { return env.hub.DownstreamCount() == 1 })

	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"action":"send_msg","params":{"message":"1"},"echo":1}`)); err != nil {
		t.Fatal(err)
	}
	_, firstKey := impl.readAction(3 * time.Second)

	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"action":"send_msg","params":{"message":"2"},"echo":2}`)); err != nil {
		t.Fatal(err)
	}
	if _, retcode, wording, _ := readBotResponse(t, conn, 3*time.Second); retcode != 1200 || !strings.Contains(wording, "上限") {
		t.Fatalf("concurrency cap not enforced: retcode %d wording %s", retcode, wording)
	}

	// Answering the first action frees the slot.
	impl.reply(firstKey, `{"message_id":1}`)
	if status, _, _, _ := readBotResponse(t, conn, 3*time.Second); status != "ok" {
		t.Fatalf("first action should succeed, got %s", status)
	}
	if got := env.policy.Limiter().Inflight(bot.Name); got != 0 {
		t.Fatalf("inflight after the reply = %d, want 0", got)
	}

	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"action":"send_msg","params":{"message":"3"},"echo":3}`)); err != nil {
		t.Fatal(err)
	}
	if _, key := impl.readAction(3 * time.Second); key == "" {
		t.Fatal("a freed slot should allow the next action")
	}
}

func TestSlowBotIsIsolatedByBackpressure(t *testing.T) {
	env := newUpstreamEnv(t, func(cfg *config.Config) {
		cfg.OneBot.UpstreamWS.UnknownAccountPolicy = "auto"
		cfg.Policy.WriteQueueSize = 64
		cfg.Policy.BotBackpressure = hub.PolicyDropOldest
		cfg.Policy.PerAccountRate = config.RateLimit{}
		cfg.Policy.PerBotRate = config.RateLimit{}
	})
	acc := env.createAccount(t, "70006")
	fast, fastToken := env.createBot(t, "fast")
	slow, slowToken := env.createBot(t, "slow")
	env.bind(t, fast.ID, acc.ID, true, "{}")
	env.bind(t, slow.ID, acc.ID, true, "{}")

	impl := env.dialImpl(t, "70006")
	fastConn := env.dialBot(t, fastToken, "")
	slowConn := env.dialBot(t, slowToken, "")
	waitFor(t, "two downstream connections", 3*time.Second, func() bool { return env.hub.DownstreamCount() == 2 })

	// The slow client never reads: the relay must drop its frames and keep
	// serving the fast one.
	// Frames are paced so the fast reader has ample slack while the slow peer
	// backs up (8 KiB * 300 = 2.4 MiB, far beyond its socket buffers).
	const total = 2000
	payload := strings.Repeat("x", 8*1024)
	go func() {
		for i := 0; i < total; i++ {
			impl.write(fmt.Sprintf(`{"post_type":"message","message_type":"group","self_id":70006,"user_id":%d,"message":"%s"}`, i, payload))
			time.Sleep(2 * time.Millisecond)
		}
	}()

	_ = slowConn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	received := 0
	deadline := time.Now().Add(25 * time.Second)
	for received < total && time.Now().Before(deadline) {
		_ = fastConn.SetReadDeadline(time.Now().Add(3 * time.Second))
		if _, _, err := fastConn.ReadMessage(); err != nil {
			t.Fatalf("fast bot read failed after %d frames: %v", received, err)
		}
		received++
	}
	if received != total {
		t.Fatalf("fast bot received %d/%d frames: a slow peer must not stall it", received, total)
	}

	// The slow peer either lost frames (drop_oldest) or was disconnected. Both
	// are acceptable; the guarantee under test is that the fast peer was never
	// stalled. Socket buffers can absorb a surprising amount, so the drop count
	// is reported rather than asserted.
	var slowDropped int64
	slowAlive := false
	for _, slowConnection := range env.hub.DownstreamsForBot(slow.ID) {
		slowAlive = true
		slowDropped += slowConnection.Dropped()
	}
	if !slowAlive {
		t.Log("the slow peer was disconnected by the backpressure policy")
	} else if slowDropped == 0 {
		t.Fatalf("%d frames fit in the socket buffer, so backpressure never engaged", total)
	} else {
		t.Logf("the slow peer dropped %d of %d frames", slowDropped, total)
	}
	_ = slowConn
}

func TestOfflineActionQueueIsFlushedOnReconnect(t *testing.T) {
	env := newUpstreamEnv(t, func(cfg *config.Config) {
		cfg.Policy.OfflineActionPolicy = "queue"
		cfg.Policy.Queue = config.Queue{Size: 4, TTL: config.Duration(30 * time.Second)}
		cfg.Policy.PerAccountRate = config.RateLimit{}
		cfg.Policy.PerBotRate = config.RateLimit{}
		cfg.Policy.ActionTimeout = config.Duration(10 * time.Second)
	})
	acc := env.createAccount(t, "70007")
	bot, token := env.createBot(t, "queued")
	env.bind(t, bot.ID, acc.ID, true, "{}")

	// No implementation connected yet: actions are buffered.
	conn := env.dialBot(t, token, "")
	waitFor(t, "downstream connection", 3*time.Second, func() bool { return env.hub.DownstreamCount() == 1 })

	for i := 0; i < 2; i++ {
		frame := fmt.Sprintf(`{"action":"send_msg","params":{"message":"%d"},"echo":%d}`, i, i)
		if err := conn.WriteMessage(websocket.TextMessage, []byte(frame)); err != nil {
			t.Fatal(err)
		}
	}
	// Nothing can have been answered yet: the queue holds both actions and the
	// Bot side is still waiting (reading the socket with a deadline would poison
	// the client connection, so the policy state is the assertion here).
	waitFor(t, "two queued actions", 2*time.Second, func() bool { return env.policy.OfflineLen() == 2 })

	// The account comes online: both actions are flushed in order.
	impl := env.dialImpl(t, "70007")
	first, key1 := impl.readAction(3 * time.Second)
	second, key2 := impl.readAction(3 * time.Second)
	if !strings.Contains(string(first), `"message":"0"`) || !strings.Contains(string(second), `"message":"1"`) {
		t.Fatalf("queued actions came out of order: %s | %s", first, second)
	}
	if env.policy.OfflineLen() != 0 {
		t.Fatalf("queue should be empty after the flush, has %d", env.policy.OfflineLen())
	}

	impl.reply(key1, `{"message_id":1}`)
	impl.reply(key2, `{"message_id":2}`)
	if status, _, _, echo := readBotResponse(t, conn, 3*time.Second); status != "ok" || echo != "0" {
		t.Fatalf("unexpected first response: %s echo=%s", status, echo)
	}
	if status, _, _, echo := readBotResponse(t, conn, 3*time.Second); status != "ok" || echo != "1" {
		t.Fatalf("unexpected second response: %s echo=%s", status, echo)
	}
}

func TestOfflineFailFastIsTheDefault(t *testing.T) {
	env := newUpstreamEnv(t, nil)
	acc := env.createAccount(t, "70008")
	bot, token := env.createBot(t, "failfast")
	env.bind(t, bot.ID, acc.ID, true, "{}")

	conn := env.dialBot(t, token, "")
	waitFor(t, "downstream connection", 3*time.Second, func() bool { return env.hub.DownstreamCount() == 1 })

	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"action":"send_msg","params":{"message":"x"},"echo":"ff"}`)); err != nil {
		t.Fatal(err)
	}
	status, retcode, wording, _ := readBotResponse(t, conn, 3*time.Second)
	if status != "failed" || retcode != 1200 || !strings.Contains(wording, "不可用") {
		t.Fatalf("fail_fast expected, got %s %d %s", status, retcode, wording)
	}
	if env.policy.OfflineLen() != 0 {
		t.Fatal("nothing should be queued in fail_fast mode")
	}
}

func TestMetricsEndpoint(t *testing.T) {
	env := newUpstreamEnv(t, func(cfg *config.Config) { cfg.OneBot.UpstreamWS.UnknownAccountPolicy = "auto" })
	acc := env.createAccount(t, "70009")
	bot, token := env.createBot(t, "metered")
	env.bind(t, bot.ID, acc.ID, true, "{}")

	impl := env.dialImpl(t, "70009")
	conn := env.dialBot(t, token, "")
	waitFor(t, "downstream connection", 3*time.Second, func() bool { return env.hub.DownstreamCount() == 1 })

	impl.write(`{"post_type":"message","message_type":"group","self_id":70009,"user_id":1,"message":"hi"}`)
	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"action":"get_status","echo":"m"}`)); err != nil {
		t.Fatal(err)
	}
	_, key := impl.readAction(3 * time.Second)
	impl.reply(key, `{"online":true}`)
	_ = readBotFrame(t, conn, 3*time.Second)

	res, err := http.Get(env.ts.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("metrics status = %d", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("content type = %q", ct)
	}
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, want := range []string{
		"onebotnoa_up 1",
		"onebotnoa_uptime_seconds",
		`onebotnoa_accounts{state="online"}`,
		"onebotnoa_upstream_connections 1",
		"onebotnoa_downstream_connections 1",
		`onebotnoa_frames_total{direction="upstream"}`,
		`onebotnoa_actions_total{result="forwarded"}`,
		"onebotnoa_pending_actions 0",
		`onebotnoa_rate_limited_total{scope="account"}`,
		"onebotnoa_event_ring_size",
		"onebotnoa_offline_queue",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("metrics output lacks %q:\n%s", want, text)
		}
	}
	if !strings.HasPrefix(text, "# HELP") {
		t.Fatalf("metrics output should start with HELP lines:\n%s", text)
	}
}

// expectNoImplAction asserts the implementation receives nothing for a while.
func expectNoImplAction(t *testing.T, impl *fakeImpl, window time.Duration) {
	t.Helper()
	_ = impl.conn.SetReadDeadline(time.Now().Add(window))
	if _, data, err := impl.conn.ReadMessage(); err == nil {
		t.Fatalf("unexpected frame reached the implementation: %s", data)
	}
}
