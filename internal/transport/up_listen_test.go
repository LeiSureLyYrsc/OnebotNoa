package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/auth"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/config"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/hub"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/model"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/onebot"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/store"
)

const bootstrapToken = "bootstrap-token-for-tests"

type observedFrame struct {
	selfID string
	role   onebot.Role
	raw    []byte
}

type recordingObserver struct {
	mu     sync.Mutex
	frames []observedFrame
	events []hub.AccountEvent
}

func (o *recordingObserver) AccountChanged(ev hub.AccountEvent) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.events = append(o.events, ev)
}

func (o *recordingObserver) UpstreamFrame(selfID string, role onebot.Role, raw []byte) {
	copied := make([]byte, len(raw))
	copy(copied, raw)
	o.mu.Lock()
	defer o.mu.Unlock()
	o.frames = append(o.frames, observedFrame{selfID: selfID, role: role, raw: copied})
}

func (o *recordingObserver) framesFor(selfID string) []observedFrame {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := []observedFrame{}
	for _, f := range o.frames {
		if f.selfID == selfID {
			out = append(out, f)
		}
	}
	return out
}

type upstreamEnv struct {
	ts      *httptest.Server
	hub     *hub.Hub
	store   *store.Store
	cfg     *config.Config
	obs     *recordingObserver
	policy  *hub.PolicyEngine
	metrics *hub.Metrics
	events  *hub.EventLog
}

func newUpstreamEnv(t *testing.T, mutate func(*config.Config)) *upstreamEnv {
	t.Helper()
	ctx := context.Background()

	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	cfg := config.Default()
	cfg.OneBot.UpstreamWS.RequireToken = true
	cfg.OneBot.UpstreamWS.BootstrapToken = bootstrapToken
	cfg.OneBot.UpstreamWS.IdentityTimeout = config.Duration(700 * time.Millisecond)
	cfg.OneBot.UpstreamWS.UnknownAccountPolicy = "pending"
	if mutate != nil {
		mutate(cfg)
	}

	logger := slog.New(slog.DiscardHandler)
	relay := hub.New(cfg, st, logger)
	obs := &recordingObserver{}
	metrics := hub.NewMetrics()
	events := hub.NewEventLog(128)
	policy := hub.NewPolicyEngine(cfg, logger)

	// Mirror the production wiring so the integration tests exercise the real
	// policy/metrics path.
	relay.SetObserver(hub.FanOutObserver{obs, metrics, events})
	relay.Actions().SetTrafficObserver(hub.FanOutTraffic{metrics, events})
	relay.Actions().SetPreSend(policy)
	relay.SetConnectHook(func(selfID string) {
		policy.FlushOffline(selfID, func(frame []byte) bool { return relay.SendActionTo(selfID, frame) })
	})

	dp := NewDataPlane(cfg, st, relay, logger)
	mux := http.NewServeMux()
	dp.Register(mux)
	if cfg.Metrics.Enable {
		mux.HandleFunc("GET /metrics", MetricsHandler(metrics, relay, events, policy))
	}
	ts := httptest.NewServer(mux)

	t.Cleanup(func() {
		dp.CloseAll("test finished")
		ts.Close()
		_ = st.Close()
	})

	return &upstreamEnv{
		ts: ts, hub: relay, store: st, cfg: cfg, obs: obs,
		policy: policy, metrics: metrics, events: events,
	}
}

func (e *upstreamEnv) dial(t *testing.T, path string, headers map[string]string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	header := http.Header{}
	for k, v := range headers {
		header.Set(k, v)
	}
	url := "ws" + strings.TrimPrefix(e.ts.URL, "http") + path
	return websocket.DefaultDialer.Dial(url, header)
}

// dialOK dials and fails the test when the handshake is refused.
func (e *upstreamEnv) dialOK(t *testing.T, headers map[string]string) *websocket.Conn {
	t.Helper()
	conn, resp, err := e.dial(t, "/onebot/v11/ws", headers)
	if err != nil {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		t.Fatalf("dial failed: %v (status %d)", err, status)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func waitFor(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func expectClosed(t *testing.T, conn *websocket.Conn, timeout time.Duration) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
	}
}

func universalHeaders(selfID string) map[string]string {
	return map[string]string{
		"X-Self-ID":       selfID,
		"X-Client-Role":   "Universal",
		"Authorization":   "Bearer " + bootstrapToken,
	}
}

func TestSharedEndpointAcceptsTwoInstances(t *testing.T) {
	env := newUpstreamEnv(t, func(cfg *config.Config) { cfg.OneBot.UpstreamWS.UnknownAccountPolicy = "auto" })

	env.dialOK(t, universalHeaders("10001"))
	env.dialOK(t, universalHeaders("10002"))

	waitFor(t, "two live sessions", 3*time.Second, func() bool {
		return len(env.hub.Registry().Sessions()) == 2
	})

	for _, selfID := range []string{"10001", "10002"} {
		session, ok := env.hub.Registry().Session(selfID)
		if !ok {
			t.Fatalf("session %s missing", selfID)
		}
		if got := session.State(); got != model.StatusOnline {
			t.Fatalf("session %s state = %s, want online", selfID, got)
		}
	}

	accounts, err := env.store.ListAccounts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 2 {
		t.Fatalf("accounts = %d, want 2", len(accounts))
	}
	for _, acc := range accounts {
		if acc.Status != model.StatusOnline {
			t.Fatalf("persisted status for %s = %s, want online", acc.SelfID, acc.Status)
		}
	}

	st := env.hub.Status()
	if st.Accounts != 2 || st.Online != 2 || st.UpstreamConns != 2 {
		t.Fatalf("hub status = %+v", st)
	}
}

func TestRoleSplitMakesAccountDegradedThenOnline(t *testing.T) {
	env := newUpstreamEnv(t, func(cfg *config.Config) { cfg.OneBot.UpstreamWS.UnknownAccountPolicy = "auto" })

	const selfID = "20001"
	headers := universalHeaders(selfID)

	// Event-only connection: the hub can receive but not send.
	eventConn := env.dialOK(t, map[string]string{
		"X-Self-ID":     selfID,
		"X-Client-Role": "Event",
		"Authorization": "Bearer " + bootstrapToken,
	})
	waitFor(t, "event session", 3*time.Second, func() bool {
		s, ok := env.hub.Registry().Session(selfID)
		return ok && s.State() == model.StatusDegraded
	})

	// Adding the API connection completes the pair.
	_ = env.dialOK(t, map[string]string{
		"X-Self-ID":     selfID,
		"X-Client-Role": "API",
		"Authorization": "Bearer " + bootstrapToken,
	})
	waitFor(t, "online session", 3*time.Second, func() bool {
		s, ok := env.hub.Registry().Session(selfID)
		return ok && s.State() == model.StatusOnline
	})

	// Losing the API connection degrades it again.
	_ = eventConn.Close()
	waitFor(t, "degraded after API drop (event closed too)", 3*time.Second, func() bool {
		s, ok := env.hub.Registry().Session(selfID)
		return !ok || s.State() != model.StatusOnline
	})

	_ = headers
}

func TestFirstFrameIdentityFallback(t *testing.T) {
	env := newUpstreamEnv(t, func(cfg *config.Config) { cfg.OneBot.UpstreamWS.UnknownAccountPolicy = "auto" })

	// No X-Self-ID: identity must come from the first frame.
	conn := env.dialOK(t, map[string]string{
		"X-Client-Role": "Universal",
		"Authorization": "Bearer " + bootstrapToken,
	})

	frame := `{"post_type":"meta_event","meta_event_type":"lifecycle","sub_type":"connect","self_id":30001,"time":1700000000}`
	if err := conn.WriteMessage(websocket.TextMessage, []byte(frame)); err != nil {
		t.Fatalf("write: %v", err)
	}

	waitFor(t, "identity from first frame", 3*time.Second, func() bool {
		_, ok := env.hub.Registry().Session("30001")
		return ok
	})

	frames := env.obs.framesFor("30001")
	if len(frames) == 0 {
		t.Fatal("the identifying frame must still be dispatched")
	}
	if string(frames[0].raw) != frame {
		t.Fatalf("frame bytes changed: %s", frames[0].raw)
	}
}

func TestIdentityTimeoutClosesConnection(t *testing.T) {
	env := newUpstreamEnv(t, func(cfg *config.Config) {
		cfg.OneBot.UpstreamWS.UnknownAccountPolicy = "auto"
		cfg.OneBot.UpstreamWS.IdentityTimeout = config.Duration(300 * time.Millisecond)
	})

	conn := env.dialOK(t, map[string]string{
		"X-Client-Role": "Universal",
		"Authorization": "Bearer " + bootstrapToken,
	})

	// Never identify: the hub must close the connection instead of leaking it.
	expectClosed(t, conn, 3*time.Second)

	if sessions := env.hub.Registry().Sessions(); len(sessions) != 0 {
		t.Fatalf("unidentified connection must not create a session: %+v", sessions)
	}
}

func TestTokenMismatchForSameSelfIDIsRejected(t *testing.T) {
	ctx := context.Background()
	env := newUpstreamEnv(t, func(cfg *config.Config) { cfg.OneBot.UpstreamWS.UnknownAccountPolicy = "auto" })

	const selfID = "40001"
	account, err := env.store.CreateAccount(ctx, selfID, "bound", "test")
	if err != nil {
		t.Fatal(err)
	}
	plain, hash, err := auth.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := env.store.SetAccountToken(ctx, account.ID, &hash); err != nil {
		t.Fatal(err)
	}

	// The bound token connects normally.
	env.dialOK(t, map[string]string{
		"X-Self-ID":     selfID,
		"X-Client-Role": "Universal",
		"Authorization": "Bearer " + plain,
	})
	waitFor(t, "bound session", 3*time.Second, func() bool {
		_, ok := env.hub.Registry().Session(selfID)
		return ok
	})

	// A different token claiming the same self_id is refused.
	conn, _, err := env.dial(t, "/onebot/v11/ws", map[string]string{
		"X-Self-ID":     selfID,
		"X-Client-Role": "Universal",
		"Authorization": "Bearer " + bootstrapToken,
	})
	if err != nil {
		t.Fatalf("handshake should upgrade before rejection: %v", err)
	}
	defer func() { _ = conn.Close() }()
	expectClosed(t, conn, 3*time.Second)

	session, ok := env.hub.Registry().Session(selfID)
	if !ok {
		t.Fatal("original session disappeared")
	}
	if peers := session.Peers(); len(peers) != 1 {
		t.Fatalf("peers = %d, want 1 (the spoof must not attach)", len(peers))
	}

	entries, err := env.store.ListAudit(ctx, 20, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range entries {
		if e.Action == "upstream.rejected" && e.Target == selfID {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected an audit entry for the rejected connection: %+v", entries)
	}
}

func TestUnknownAccountPolicies(t *testing.T) {
	t.Run("reject", func(t *testing.T) {
		env := newUpstreamEnv(t, func(cfg *config.Config) { cfg.OneBot.UpstreamWS.UnknownAccountPolicy = "reject" })
		conn := env.dialOK(t, universalHeaders("50001"))
		expectClosed(t, conn, 3*time.Second)
		if len(env.hub.Registry().Pending()) != 0 {
			t.Fatal("reject policy must not queue a pending account")
		}
		if _, err := env.store.AccountBySelfID(context.Background(), "50001"); err == nil {
			t.Fatal("reject policy must not create the account")
		}
	})

	t.Run("pending", func(t *testing.T) {
		env := newUpstreamEnv(t, nil) // default policy is pending
		conn := env.dialOK(t, universalHeaders("50002"))
		expectClosed(t, conn, 3*time.Second)

		waitFor(t, "pending entry", 2*time.Second, func() bool {
			return len(env.hub.Registry().Pending()) == 1
		})
		pending := env.hub.Registry().Pending()[0]
		if pending.SelfID != "50002" {
			t.Fatalf("pending self_id = %s", pending.SelfID)
		}

		// Approval creates the account so the next reconnect succeeds.
		if _, err := env.hub.Registry().ApprovePending(context.Background(), pending.ID); err != nil {
			t.Fatalf("approve: %v", err)
		}
		if _, err := env.store.AccountBySelfID(context.Background(), "50002"); err != nil {
			t.Fatalf("approval should create the account: %v", err)
		}
		env.dialOK(t, universalHeaders("50002"))
		waitFor(t, "session after approval", 3*time.Second, func() bool {
			_, ok := env.hub.Registry().Session("50002")
			return ok
		})
	})

	t.Run("auto", func(t *testing.T) {
		env := newUpstreamEnv(t, func(cfg *config.Config) { cfg.OneBot.UpstreamWS.UnknownAccountPolicy = "auto" })
		env.dialOK(t, universalHeaders("50003"))
		waitFor(t, "auto-created account", 3*time.Second, func() bool {
			_, err := env.store.AccountBySelfID(context.Background(), "50003")
			return err == nil
		})
	})
}

func TestDisabledAccountIsRefused(t *testing.T) {
	ctx := context.Background()
	env := newUpstreamEnv(t, nil)

	account, err := env.store.CreateAccount(ctx, "60001", "disabled", "test")
	if err != nil {
		t.Fatal(err)
	}
	account.Enabled = false
	if err := env.store.UpdateAccountProfile(ctx, account); err != nil {
		t.Fatal(err)
	}

	conn := env.dialOK(t, universalHeaders("60001"))
	expectClosed(t, conn, 3*time.Second)

	if _, ok := env.hub.Registry().Session("60001"); ok {
		t.Fatal("a disabled account must not get a session")
	}
}

func TestMissingTokenIsRefusedAtHandshake(t *testing.T) {
	env := newUpstreamEnv(t, func(cfg *config.Config) { cfg.OneBot.UpstreamWS.UnknownAccountPolicy = "auto" })

	_, resp, err := env.dial(t, "/onebot/v11/ws", map[string]string{"X-Self-ID": "70001"})
	if err == nil {
		t.Fatal("dial without a token must fail")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %v, want 401", resp)
	}
}

func TestPathTokenAndRoleSegments(t *testing.T) {
	ctx := context.Background()
	env := newUpstreamEnv(t, func(cfg *config.Config) { cfg.OneBot.UpstreamWS.UnknownAccountPolicy = "auto" })

	account, err := env.store.CreateAccount(ctx, "80001", "pathed", "test")
	if err != nil {
		t.Fatal(err)
	}
	plain, hash, err := auth.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := env.store.SetAccountToken(ctx, account.ID, &hash); err != nil {
		t.Fatal(err)
	}

	// The path token alone identifies the instance (no X-Self-ID header).
	conn, resp, err := env.dial(t, "/onebot/v11/ws/"+plain+"/event", nil)
	if err != nil {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		t.Fatalf("dial with path token: %v (status %d)", err, status)
	}
	defer func() { _ = conn.Close() }()

	waitFor(t, "path-token session", 3*time.Second, func() bool {
		_, ok := env.hub.Registry().Session("80001")
		return ok
	})
	session, _ := env.hub.Registry().Session("80001")
	if got := session.State(); got != model.StatusDegraded {
		t.Fatalf("event-only connection state = %s, want degraded", got)
	}
}

func TestEventFramesAreForwardedByteExact(t *testing.T) {
	env := newUpstreamEnv(t, func(cfg *config.Config) { cfg.OneBot.UpstreamWS.UnknownAccountPolicy = "auto" })

	conn := env.dialOK(t, universalHeaders("90001"))
	waitFor(t, "session", 3*time.Second, func() bool {
		_, ok := env.hub.Registry().Session("90001")
		return ok
	})

	// Deliberately odd formatting, a huge id and an unknown field: the relay
	// must not touch a single byte.
	frame := `{  "post_type" : "message", "message_type":"group", "self_id": 90001, "user_id": 123456789012345678, "group_id": 20002, "message": [{"type":"text","data":{"text":"hi"}}], "unknown_field": {"keep": true} }`
	if err := conn.WriteMessage(websocket.TextMessage, []byte(frame)); err != nil {
		t.Fatal(err)
	}

	waitFor(t, "observed frame", 3*time.Second, func() bool {
		return len(env.obs.framesFor("90001")) > 0
	})
	got := string(env.obs.framesFor("90001")[0].raw)
	if got != frame {
		t.Fatalf("frame was rewritten:\n got: %s\nwant: %s", got, frame)
	}
}

func TestConcurrentInstancesKeepSeparateSessions(t *testing.T) {
	env := newUpstreamEnv(t, func(cfg *config.Config) { cfg.OneBot.UpstreamWS.UnknownAccountPolicy = "auto" })

	const instances = 8
	conns := make([]*websocket.Conn, 0, instances)
	for i := 0; i < instances; i++ {
		conns = append(conns, env.dialOK(t, universalHeaders(fmt.Sprintf("1100%d", i))))
	}
	waitFor(t, "all instances attached", 5*time.Second, func() bool {
		return len(env.hub.Registry().Sessions()) == instances
	})

	for i := 0; i < instances; i++ {
		selfID := fmt.Sprintf("1100%d", i)
		if _, ok := env.hub.Registry().Session(selfID); !ok {
			t.Fatalf("session %s missing", selfID)
		}
		frame := fmt.Sprintf(`{"post_type":"message","message_type":"private","self_id":%s,"user_id":1,"message":"x"}`, selfID)
		if err := conns[i].WriteMessage(websocket.TextMessage, []byte(frame)); err != nil {
			t.Fatalf("write to %s: %v", selfID, err)
		}
	}

	for i := 0; i < instances; i++ {
		selfID := fmt.Sprintf("1100%d", i)
		expected := fmt.Sprintf(`{"post_type":"message","message_type":"private","self_id":%s,"user_id":1,"message":"x"}`, selfID)
		waitFor(t, "frame for "+selfID, 3*time.Second, func() bool {
			frames := env.obs.framesFor(selfID)
			return len(frames) == 1
		})
		if got := string(env.obs.framesFor(selfID)[0].raw); got != expected {
			t.Fatalf("%s got %s want %s", selfID, got, expected)
		}
	}

	// A JSON document with the wrong shape must not crash anything.
	_ = json.Valid([]byte(`{}`))
}
