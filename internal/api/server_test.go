package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/auth"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/config"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/hub"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/store"
)

const testPassword = "correct-horse-battery"

type testAPI struct {
	ts     *httptest.Server
	http   *http.Client
	st     *store.Store
	hub    *hub.Hub
	events *hub.EventLog
}

func newTestAPI(t *testing.T) *testAPI {
	t.Helper()
	return newTestAPIWith(t, nil)
}

func newTestAPIWith(t *testing.T, mutate func(*config.Config)) *testAPI {
	t.Helper()
	ctx := context.Background()

	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "api.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	hash, err := auth.HashPassword(testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateUser(ctx, "admin", hash, "admin"); err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.DiscardHandler)
	manager := auth.NewManager(st, time.Hour, logger)
	cfg := config.Default()
	if mutate != nil {
		mutate(cfg)
	}
	relay := hub.New(cfg, st, logger)
	events := hub.NewEventLog(64)
	relay.SetObserver(events)
	relay.Actions().SetTrafficObserver(events)
	srv := New(Options{
		Store: st, Auth: manager, Logger: logger, Config: cfg,
		Hub: relay, Events: events,
		Version: "test-version", StartedAt: time.Now().Add(-time.Minute),
	})
	mux := http.NewServeMux()
	srv.Register(mux)

	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &testAPI{
		ts:     ts,
		http:   &http.Client{Jar: jar, Timeout: 5 * time.Second},
		st:     st,
		hub:    relay,
		events: events,
	}
}

func (a *testAPI) do(t *testing.T, method, path, body string, headers map[string]string) (*http.Response, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(method, a.ts.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := a.http.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = res.Body.Close() }()

	payload := map[string]any{}
	if res.StatusCode != http.StatusNoContent {
		_ = json.NewDecoder(res.Body).Decode(&payload)
	}
	return res, payload
}

func TestLoginFlowAndCSRF(t *testing.T) {
	a := newTestAPI(t)

	res, _ := a.do(t, http.MethodGet, "/api/v1/auth/me", "", nil)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("me without session = %d, want 401", res.StatusCode)
	}

	res, _ = a.do(t, http.MethodPost, "/api/v1/auth/login", "{not json", nil)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("malformed login body = %d, want 400", res.StatusCode)
	}

	res, _ = a.do(t, http.MethodPost, "/api/v1/auth/login", `{"username":"admin"}`, nil)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("login without password = %d, want 400", res.StatusCode)
	}

	res, _ = a.do(t, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"nope"}`, nil)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong password = %d, want 401", res.StatusCode)
	}

	res, payload := a.do(t, http.MethodPost, "/api/v1/auth/login",
		fmt.Sprintf(`{"username":"admin","password":%q}`, testPassword), nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("login = %d, want 200 (%v)", res.StatusCode, payload)
	}
	csrf, _ := payload["csrf_token"].(string)
	if csrf == "" {
		t.Fatalf("login response lacks csrf_token: %v", payload)
	}
	user, _ := payload["user"].(map[string]any)
	if user == nil || user["username"] != "admin" {
		t.Fatalf("login response lacks user: %v", payload)
	}
	if _, ok := user["password_hash"]; ok {
		t.Fatal("password hash must never be serialised")
	}

	var cookieFound bool
	for _, c := range res.Cookies() {
		if c.Name == sessionCookie {
			cookieFound = true
			if !c.HttpOnly {
				t.Fatalf("session cookie must be HttpOnly: %+v", c)
			}
			if c.SameSite != http.SameSiteLaxMode {
				t.Fatalf("session cookie must be SameSite=Lax: %+v", c)
			}
		}
	}
	if !cookieFound {
		t.Fatalf("session cookie %q missing from login response", sessionCookie)
	}

	res, payload = a.do(t, http.MethodGet, "/api/v1/auth/me", "", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("me = %d, want 200", res.StatusCode)
	}
	if payload["csrf_token"] != csrf {
		t.Fatalf("me csrf = %v, want %v", payload["csrf_token"], csrf)
	}

	res, _ = a.do(t, http.MethodPost, "/api/v1/auth/logout", "", nil)
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("logout without CSRF = %d, want 403", res.StatusCode)
	}

	res, _ = a.do(t, http.MethodPost, "/api/v1/auth/logout", "", map[string]string{"X-CSRF-Token": csrf})
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("logout = %d, want 204", res.StatusCode)
	}
	res, _ = a.do(t, http.MethodGet, "/api/v1/auth/me", "", nil)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("me after logout = %d, want 401", res.StatusCode)
	}
}

func TestLoginIsAudited(t *testing.T) {
	a := newTestAPI(t)

	a.do(t, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"nope"}`, nil)
	a.do(t, http.MethodPost, "/api/v1/auth/login",
		fmt.Sprintf(`{"username":"admin","password":%q}`, testPassword), nil)

	entries, err := a.st.ListAudit(context.Background(), 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	var failed, ok bool
	for _, e := range entries {
		switch e.Action {
		case "auth.login.failed":
			failed = true
		case "auth.login":
			ok = true
		}
	}
	if !failed || !ok {
		t.Fatalf("expected both login audit entries, got %+v", entries)
	}
}

func TestLoginRateLimit(t *testing.T) {
	a := newTestAPI(t)

	last := 0
	for i := 0; i < 9; i++ {
		res, _ := a.do(t, http.MethodPost, "/api/v1/auth/login",
			`{"username":"admin","password":"nope"}`, nil)
		last = res.StatusCode
		if i < 8 && res.StatusCode != http.StatusUnauthorized {
			t.Fatalf("attempt %d = %d, want 401", i+1, res.StatusCode)
		}
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("9th attempt = %d, want 429", last)
	}

	// X-Forwarded-For must be ignored unless server.trust_proxy is on, so
	// spoofing it cannot escape the lockout.
	res, _ := a.do(t, http.MethodPost, "/api/v1/auth/login",
		fmt.Sprintf(`{"username":"admin","password":%q}`, testPassword),
		map[string]string{"X-Forwarded-For": "10.1.2.3"})
	if res.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("spoofed X-Forwarded-For escaped the lockout: %d", res.StatusCode)
	}
}

func TestTrustedProxyHeaderSelectsClientIP(t *testing.T) {
	a := newTestAPIWith(t, func(cfg *config.Config) { cfg.Server.TrustProxy = true })

	for i := 0; i < 8; i++ {
		res, _ := a.do(t, http.MethodPost, "/api/v1/auth/login",
			`{"username":"admin","password":"nope"}`, nil)
		if res.StatusCode != http.StatusUnauthorized {
			t.Fatalf("attempt %d = %d, want 401", i+1, res.StatusCode)
		}
	}
	res, _ := a.do(t, http.MethodPost, "/api/v1/auth/login",
		`{"username":"admin","password":"nope"}`, nil)
	if res.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("lockout not applied for 127.0.0.1: %d", res.StatusCode)
	}

	// A different client IP (from the trusted proxy header) is its own bucket.
	res, payload := a.do(t, http.MethodPost, "/api/v1/auth/login",
		fmt.Sprintf(`{"username":"admin","password":%q}`, testPassword),
		map[string]string{"X-Forwarded-For": "10.1.2.3, 10.9.9.9"})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("login from another client IP = %d, want 200 (%v)", res.StatusCode, payload)
	}

	entries, err := a.st.ListAudit(context.Background(), 20, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range entries {
		if e.Action == "auth.login" && e.IP == "10.1.2.3" {
			found = true
		}
	}
	if !found {
		t.Fatalf("audit did not record the forwarded client IP: %+v", entries)
	}
}

func TestSystemStatus(t *testing.T) {
	a := newTestAPI(t)
	a.do(t, http.MethodPost, "/api/v1/auth/login",
		fmt.Sprintf(`{"username":"admin","password":%q}`, testPassword), nil)

	res, payload := a.do(t, http.MethodGet, "/api/v1/system/status", "", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if payload["version"] != "test-version" {
		t.Fatalf("version = %v", payload["version"])
	}
	if payload["users"] != float64(1) {
		t.Fatalf("users = %v, want 1", payload["users"])
	}
	if _, ok := payload["database"]; !ok {
		t.Fatalf("status lacks database path: %v", payload)
	}
}
