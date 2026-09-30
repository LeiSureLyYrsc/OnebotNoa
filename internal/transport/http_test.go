package transport

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/auth"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/config"
)

// postJSON issues a POST with a JSON body and returns the response.
func (e *upstreamEnv) postJSON(t *testing.T, path, body string, headers map[string]string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, e.ts.URL+path, bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	res, err := e.ts.Client().Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer func() { _ = res.Body.Close() }()
	payload, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res, payload
}

func TestHTTPReportIsMulticastLikeWebSocket(t *testing.T) {
	env := newUpstreamEnv(t, func(cfg *config.Config) {
		cfg.OneBot.UpstreamWS.UnknownAccountPolicy = "auto"
	})
	account := env.createAccount(t, "91001")
	bot, token := env.createBot(t, "http-report-bot")
	env.bind(t, bot.ID, account.ID, true, "{}")

	conn := env.dialBot(t, token, "")
	waitFor(t, "downstream connection", 3*time.Second, func() bool { return env.hub.DownstreamCount() == 1 })

	// No WebSocket upstream at all: the event arrives purely over HTTP.
	frame := `{"post_type":"message","message_type":"private","self_id":91001,"user_id":7,"message":"over-http"}`
	res, body := env.postJSON(t, "/onebot/v11/report", frame, map[string]string{
		"X-Self-ID":     "91001",
		"Authorization": "Bearer " + bootstrapToken,
	})
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("report status = %d (%s)", res.StatusCode, body)
	}
	got := string(readBotFrame(t, conn, 3*time.Second))
	if got != frame {
		t.Fatalf("HTTP report mismatch\n got=%q\nwant=%q", got, frame)
	}
}

func TestHTTPReportRequiresIdentityAndToken(t *testing.T) {
	env := newUpstreamEnv(t, func(cfg *config.Config) {
		cfg.OneBot.UpstreamWS.UnknownAccountPolicy = "auto"
	})

	// No X-Self-ID header and no self_id in the payload.
	res, _ := env.postJSON(t, "/onebot/v11/report", `{"post_type":"message"}`, map[string]string{
		"Authorization": "Bearer " + bootstrapToken,
	})
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("report without identity = %d, want 400", res.StatusCode)
	}

	// Identity from the first frame works (same fallback as WebSocket).
	res, _ = env.postJSON(t, "/onebot/v11/report", `{"post_type":"message","self_id":91002}`, map[string]string{
		"Authorization": "Bearer " + bootstrapToken,
	})
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("report with a first-frame identity = %d, want 204", res.StatusCode)
	}

	// A wrong token is rejected.
	res, _ = env.postJSON(t, "/onebot/v11/report", `{"post_type":"message","self_id":91003}`, map[string]string{
		"X-Self-ID":     "91003",
		"Authorization": "Bearer nope",
	})
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("report with a bad token = %d, want 401", res.StatusCode)
	}

	// A per-instance token may only speak for its own account.
	account := env.createAccount(t, "91004")
	plain, hash, err := auth.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := env.store.SetAccountToken(t.Context(), account.ID, &hash); err != nil {
		t.Fatal(err)
	}
	res, _ = env.postJSON(t, "/onebot/v11/report", `{"post_type":"message","self_id":91005}`, map[string]string{
		"X-Self-ID":     "91005",
		"Authorization": "Bearer " + plain,
	})
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("token used for a foreign self_id = %d, want 403", res.StatusCode)
	}
}

func TestHTTPAPIRoundTrip(t *testing.T) {
	env := newUpstreamEnv(t, func(cfg *config.Config) {
		cfg.OneBot.UpstreamWS.UnknownAccountPolicy = "auto"
	})
	account := env.createAccount(t, "92001")
	bot, token := env.createBot(t, "http-api-bot")
	env.bind(t, bot.ID, account.ID, true, "{}")

	impl := env.dialImpl(t, "92001")
	apiPath := "/onebot/v11/http/" + token

	// The Bot calls an action over HTTP; the relay forwards it to the
	// implementation and returns the reply in the HTTP response. The responder
	// waits for the forwarded frame while postJSON blocks on the reply.
	answered := make(chan struct{})
	go func() {
		defer close(answered)
		_, key := impl.readAction(5 * time.Second)
		impl.reply(key, `{"message_id":99}`)
	}()

	res, body := env.postJSON(t, apiPath,
		`{"action":"send_msg","params":{"message":"hi"},"echo":"http-1"}`, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("http api status = %d (%s)", res.StatusCode, body)
	}
	var response struct {
		Status  string          `json:"status"`
		Retcode int             `json:"retcode"`
		Data    json.RawMessage `json:"data"`
		Echo    json.RawMessage `json:"echo"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	if response.Status != "ok" {
		t.Fatalf("http api reply = %s", body)
	}
	// The HTTP caller gets its own echo back, not the relay's rewritten one.
	if string(response.Echo) != `"http-1"` {
		t.Fatalf("echo = %s, want \"http-1\"", response.Echo)
	}
	if !strings.Contains(string(response.Data), "99") {
		t.Fatalf("upstream data not returned: %s", response.Data)
	}
}

func TestHTTPAPILocalAnswerAndAuthz(t *testing.T) {
	env := newUpstreamEnv(t, func(cfg *config.Config) {
		cfg.OneBot.UpstreamWS.UnknownAccountPolicy = "auto"
	})
	account := env.createAccount(t, "92002")
	other := env.createAccount(t, "92003")
	bot, token := env.createBot(t, "http-authz")
	env.bind(t, bot.ID, account.ID, true, "{}")

	// Unknown token.
	res, _ := env.postJSON(t, "/onebot/v11/http/not-a-token", `{"action":"get_status"}`, nil)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unknown token = %d, want 401", res.StatusCode)
	}

	// A bound account is resolved automatically and answered by the relay.
	res, body := env.postJSON(t, "/onebot/v11/http/"+token, `{"action":"get_version_info"}`, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("local answer over HTTP = %d (%s)", res.StatusCode, body)
	}
	if !strings.Contains(string(body), "OnebotNoa") {
		t.Fatalf("expected the relay's own version info, got %s", body)
	}

	// An account the Bot is not bound to is refused.
	res, body = env.postJSON(t, "/onebot/v11/http/"+token,
		fmt.Sprintf(`{"action":"get_status","self_id":%s}`, other.SelfID), nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("unbound account = %d (%s)", res.StatusCode, body)
	}
	var failure struct {
		Status  string `json:"status"`
		Retcode int    `json:"retcode"`
	}
	if err := json.Unmarshal(body, &failure); err != nil {
		t.Fatal(err)
	}
	if failure.Status != "failed" || failure.Retcode != 1403 {
		t.Fatalf("unbound account should be 1403, got %s", body)
	}

	// A malformed frame is a 1400.
	res, body = env.postJSON(t, "/onebot/v11/http/"+token, `{"params":{}}`, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("malformed frame = %d", res.StatusCode)
	}
	if err := json.Unmarshal(body, &failure); err != nil {
		t.Fatal(err)
	}
	if failure.Retcode != 1400 {
		t.Fatalf("malformed frame should be 1400, got %s", body)
	}
}

func TestQuickOperationIsExplicitlyUnimplemented(t *testing.T) {
	env := newUpstreamEnv(t, func(cfg *config.Config) {
		cfg.OneBot.HTTP.QuickOperation = true
		cfg.OneBot.UpstreamWS.UnknownAccountPolicy = "auto"
	})
	_, token := env.createBot(t, "quick-op")

	res, body := env.postJSON(t, "/onebot/v11/http/"+token+"/1/quick-operation", `{"flag":"x"}`, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("quick-operation = %d (%s)", res.StatusCode, body)
	}
	var failure struct {
		Retcode int    `json:"retcode"`
		Wording string `json:"wording"`
	}
	if err := json.Unmarshal(body, &failure); err != nil {
		t.Fatal(err)
	}
	if failure.Retcode != 1404 || !strings.Contains(failure.Wording, "语义") {
		t.Fatalf("quick-operation must explain why it is unimplemented: %s", body)
	}

	// When the feature is off, the route does not exist at all.
	plain := newUpstreamEnv(t, nil)
	_, token2 := plain.createBot(t, "quick-op-off")
	res, _ = plain.postJSON(t, "/onebot/v11/http/"+token2+"/1/quick-operation", `{}`, nil)
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("disabled quick-operation = %d, want 404", res.StatusCode)
	}
}

func TestHTTPReportRespectsBindings(t *testing.T) {
	env := newUpstreamEnv(t, func(cfg *config.Config) {
		cfg.OneBot.UpstreamWS.UnknownAccountPolicy = "auto"
	})
	bound := env.createAccount(t, "93001")
	env.createAccount(t, "93002") // exists but is not bound to this Bot
	bot, token := env.createBot(t, "http-bound")
	env.bind(t, bot.ID, bound.ID, true, "{}")

	conn := env.dialBot(t, token, "")
	waitFor(t, "downstream connection", 3*time.Second, func() bool { return env.hub.DownstreamCount() == 1 })

	// An event for an account the Bot is not bound to must never arrive.
	frame := `{"post_type":"message","message_type":"private","self_id":93002,"user_id":1,"message":"secret"}`
	if res, _ := env.postJSON(t, "/onebot/v11/report", frame, map[string]string{
		"X-Self-ID":     "93002",
		"Authorization": "Bearer " + bootstrapToken,
	}); res.StatusCode != http.StatusNoContent {
		t.Fatalf("report status rejected unexpectedly")
	}
	// (No timeout-based read here: it would poison the client connection.)

	// The bound account's event does arrive (byte-exact).
	ok := `{"post_type":"message","message_type":"private","self_id":93001,"user_id":1,"message":"mine"}`
	if res, _ := env.postJSON(t, "/onebot/v11/report", ok, map[string]string{
		"X-Self-ID":     "93001",
		"Authorization": "Bearer " + bootstrapToken,
	}); res.StatusCode != http.StatusNoContent {
		t.Fatalf("report status = %d", res.StatusCode)
	}
	got := string(readBotFrame(t, conn, 3*time.Second))
	if got != ok {
		t.Fatalf("bound event mismatch\n got=%q\nwant=%q", got, ok)
	}
}

var _ = websocket.TextMessage
