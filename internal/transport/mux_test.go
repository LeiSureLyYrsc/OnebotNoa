package transport

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHealthz(t *testing.T) {
	mux := NewMux(Options{
		Version:   "test",
		StartedAt: time.Now().Add(-3 * time.Second),
		Logger:    slog.New(slog.DiscardHandler),
	})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var body struct {
		Status     string `json:"status"`
		Version    string `json:"version"`
		UptimeSec  int    `json:"uptime_sec"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	if body.Status != "ok" || body.Version != "test" {
		t.Fatalf("unexpected body: %+v", body)
	}
	if body.UptimeSec < 2 {
		t.Fatalf("uptime_sec = %d, want >= 2", body.UptimeSec)
	}
}

func TestWebUIFallbackPage(t *testing.T) {
	mux := NewMux(Options{Version: "test", StartedAt: time.Now(), Logger: slog.New(slog.DiscardHandler)})

	for _, path := range []string{"/", "/dashboard"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200", path, rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
			t.Fatalf("%s: content-type = %q", path, ct)
		}
	}
}
