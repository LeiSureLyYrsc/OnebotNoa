package transport

import (
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/config"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/hub"
)

// DataPlane serves the OneBot V11 WebSocket endpoints: the shared upstream
// listener (QQ instances dial in) and, from I3 on, the downstream listener
// (Bot applications dial in).
type DataPlane struct {
	cfg    *config.Config
	conns  ConnStore
	hub    *hub.Hub
	logger *slog.Logger

	upgrader websocket.Upgrader
	connSeq  atomic.Uint64

	mu    sync.Mutex
	peers map[string]hub.Peer
}

// NewDataPlane builds the data plane. conns is connect.json, which is where
// the credentials and the connection set live.
func NewDataPlane(cfg *config.Config, conns ConnStore, h *hub.Hub, logger *slog.Logger) *DataPlane {
	if logger == nil {
		logger = slog.Default()
	}
	return &DataPlane{
		cfg:    cfg,
		conns:  conns,
		hub:    h,
		logger: logger,
		peers:  map[string]hub.Peer{},
		upgrader: websocket.Upgrader{
			HandshakeTimeout: 10 * time.Second,
			ReadBufferSize:   4096,
			WriteBufferSize:  4096,
			// Non-browser OneBot clients send no Origin; browser origins must
			// match the host. Authentication is the real gate (token).
			CheckOrigin: func(r *http.Request) bool {
				origin := r.Header.Get("Origin")
				if origin == "" {
					return true
				}
				parsed, err := url.Parse(origin)
				if err != nil {
					return false
				}
				return strings.EqualFold(parsed.Host, r.Host)
			},
		},
	}
}

// Register mounts the configured OneBot endpoints.
func (d *DataPlane) Register(mux *http.ServeMux) {
	if up := d.cfg.OneBot.UpstreamWS; up.Enable {
		mux.HandleFunc("GET "+up.Path, d.handleUpstreamWS)
		mux.HandleFunc("GET "+up.Path+"/{rest...}", d.handleUpstreamWS)
	}
	if down := d.cfg.OneBot.DownstreamWS; down.Enable {
		mux.HandleFunc("GET "+down.Path, d.handleDownstreamWS)
		mux.HandleFunc("GET "+down.Path+"/{rest...}", d.handleDownstreamWS)
	}
	d.RegisterHTTP(mux)
}

// CloseAll closes every live data-plane connection (graceful shutdown).
func (d *DataPlane) CloseAll(reason string) {
	d.mu.Lock()
	peers := make([]hub.Peer, 0, len(d.peers))
	for _, p := range d.peers {
		peers = append(peers, p)
	}
	d.mu.Unlock()
	for _, p := range peers {
		p.Close(websocket.CloseGoingAway, reason)
	}
}

func (d *DataPlane) track(peer hub.Peer) {
	d.mu.Lock()
	d.peers[peer.ID()] = peer
	d.mu.Unlock()
}

func (d *DataPlane) untrack(peer hub.Peer) {
	d.mu.Lock()
	delete(d.peers, peer.ID())
	d.mu.Unlock()
}

func (d *DataPlane) nextConnID(prefix string) string {
	return fmt.Sprintf("%s-%d-%d", prefix, time.Now().Unix()%100000, d.connSeq.Add(1))
}

// remoteAddr resolves the client address, honouring trusted proxy headers only
// when the operator opted in.
func (d *DataPlane) remoteAddr(r *http.Request) string {
	if d.cfg.Server.TrustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			if idx := strings.IndexByte(xff, ','); idx > 0 {
				return strings.TrimSpace(xff[:idx])
			}
			return strings.TrimSpace(xff)
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
