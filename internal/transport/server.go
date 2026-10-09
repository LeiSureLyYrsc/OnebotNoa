package transport

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/config"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/hub"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/store"
)

// Server owns the data-plane listeners and outbound dialers.
type Server struct {
	cfg  config.Config
	hub  *hub.Hub
	st   *store.Store
	log  *slog.Logger
	ctx  context.Context
	stop context.CancelFunc

	mu    sync.Mutex
	dyn   map[int64]*dynamicListener
	dials []*dialRunner
}

// New constructs a transport server.
func New(cfg config.Config, h *hub.Hub, st *store.Store, log *slog.Logger) *Server {
	ctx, stop := context.WithCancel(context.Background())
	return &Server{cfg: cfg, hub: h, st: st, log: log, ctx: ctx, stop: stop, dyn: map[int64]*dynamicListener{}}
}

// Register attaches the shared data-plane routes to mux.
func (s *Server) Register(mux *http.ServeMux) {
	up := s.cfg.OneBot.Upstream.Path
	down := s.cfg.OneBot.Downstream.Path
	if s.cfg.OneBot.Upstream.Enable {
		req := s.cfg.OneBot.Upstream.RequireToken
		mux.HandleFunc("GET "+up, func(w http.ResponseWriter, r *http.Request) {
			s.serveUpstream(w, r, "", "", "", req)
		})
		mux.HandleFunc("GET "+up+"/api", func(w http.ResponseWriter, r *http.Request) {
			s.serveUpstream(w, r, "", "", "api", req)
		})
		mux.HandleFunc("GET "+up+"/event", func(w http.ResponseWriter, r *http.Request) {
			s.serveUpstream(w, r, "", "", "event", req)
		})
		mux.HandleFunc("GET "+up+"/{token}", func(w http.ResponseWriter, r *http.Request) {
			s.serveUpstream(w, r, "", r.PathValue("token"), "", req)
		})
	}
	if s.cfg.OneBot.Downstream.Enable {
		mux.HandleFunc("GET "+down, func(w http.ResponseWriter, r *http.Request) {
			s.serveDownstream(w, r, 0, "", "")
		})
		mux.HandleFunc("GET "+down+"/{token}", func(w http.ResponseWriter, r *http.Request) {
			s.serveDownstream(w, r, 0, r.PathValue("token"), "")
		})
		mux.HandleFunc("GET "+down+"/{token}/{self_id}", func(w http.ResponseWriter, r *http.Request) {
			s.serveDownstream(w, r, 0, r.PathValue("token"), r.PathValue("self_id"))
		})
	}
	if s.cfg.OneBot.HTTP.Enable {
		s.registerHTTP(mux)
	}
}

// Start launches outbound dialers and reconciles dynamic listeners.
func (s *Server) Start() {
	for _, d := range s.cfg.OneBot.Dial {
		if d.Enabled != nil && !*d.Enabled {
			continue
		}
		r := newDialRunner(s, d)
		s.dials = append(s.dials, r)
		go r.run(s.ctx)
	}
	if err := s.SyncListeners(); err != nil {
		s.log.Warn("sync listeners", "err", err)
	}
}

// Close stops every listener and dialer.
func (s *Server) Close() {
	s.stop()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, l := range s.dyn {
		l.stop()
	}
	s.dyn = map[int64]*dynamicListener{}
}

// SyncListeners reconciles dedicated listeners with the store.
func (s *Server) SyncListeners() error {
	listeners, err := s.st.ListListeners()
	if err != nil {
		return err
	}
	desired := map[int64]bool{}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, l := range listeners {
		desired[l.ID] = true
		if l.Enabled {
			if _, ok := s.dyn[l.ID]; !ok {
				dl, err := s.startDynamic(l)
				if err != nil {
					s.log.Warn("start dynamic listener", "name", l.Name, "err", err)
					continue
				}
				s.dyn[l.ID] = dl
			}
		} else if dl, ok := s.dyn[l.ID]; ok {
			dl.stop()
			delete(s.dyn, l.ID)
		}
	}
	for id, dl := range s.dyn {
		if !desired[id] {
			dl.stop()
			delete(s.dyn, id)
		}
	}
	return nil
}

func extractToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
	}
	if v := r.URL.Query().Get("access_token"); v != "" {
		return v
	}
	return ""
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
