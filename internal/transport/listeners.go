package transport

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/model"
)

type dynamicListener struct {
	id  int64
	srv *http.Server
	ln  net.Listener
}

func (s *Server) startDynamic(l model.Listener) (*dynamicListener, error) {
	ln, err := net.Listen("tcp", l.BindAddr)
	if err != nil {
		return nil, err
	}
	path := l.Path
	if path == "" {
		path = s.cfg.OneBot.Upstream.Path
	}
	mux := http.NewServeMux()

	switch l.Role {
	case model.RoleDownstream:
		presetBot := int64(0)
		if l.BotID != nil {
			presetBot = *l.BotID
		}
		mux.HandleFunc("GET "+path, func(w http.ResponseWriter, r *http.Request) {
			s.serveDownstream(w, r, presetBot, "", "")
		})
		mux.HandleFunc("GET "+path+"/{self_id}", func(w http.ResponseWriter, r *http.Request) {
			s.serveDownstream(w, r, presetBot, "", r.PathValue("self_id"))
		})
	default:
		preset := ""
		if l.AccountID != nil {
			if acc, err := s.st.GetAccount(*l.AccountID); err == nil {
				preset = acc.SelfID
			}
		}
		mux.HandleFunc("GET "+path, func(w http.ResponseWriter, r *http.Request) {
			s.serveUpstream(w, r, preset, "", "", preset == "")
		})
	}

	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	dl := &dynamicListener{id: l.ID, srv: srv, ln: ln}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.log.Warn("dynamic listener stopped", "name", l.Name, "err", err)
		}
	}()
	s.log.Info("dynamic listener started", "name", l.Name, "role", l.Role, "addr", l.BindAddr, "path", path)
	return dl, nil
}

func (d *dynamicListener) stop() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = d.srv.Shutdown(ctx)
}
