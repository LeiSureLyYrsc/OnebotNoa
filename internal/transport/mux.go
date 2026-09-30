// Package transport owns the HTTP/WebSocket surface of the hub: the shared
// upstream/downstream listeners, the OneBot HTTP compatibility layer and the
// management API mux.
package transport

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/webui"
)

// Options carries the process-wide facts the mux reports.
type Options struct {
	Version   string
	StartedAt time.Time
	Logger    *slog.Logger
}

// NewMux builds the root HTTP handler.
//
// Route groups (registered by later increments):
//
//	/healthz, /metrics          operational endpoints
//	/api/v1/...                 management REST + SSE
//	/onebot/v11/...             OneBot V11 data plane (WS + HTTP)
//	/                           embedded WebUI
func NewMux(opt Options) *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":     "ok",
			"version":    opt.Version,
			"uptime_sec": int(time.Since(opt.StartedAt).Seconds()),
		})
	})

	mux.Handle("/", webui.Handler())
	return mux
}
