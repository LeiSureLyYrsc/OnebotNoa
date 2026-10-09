package transport

import (
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/auth"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/onebot"
)

func (s *Server) registerHTTP(mux *http.ServeMux) {
	prefix := s.cfg.OneBot.HTTP.Prefix
	mux.HandleFunc("POST "+prefix+"/{token}", s.handleHTTPReport)
	mux.HandleFunc("POST "+prefix+"/{token}/{action}", s.handleHTTPAction)
}

func (s *Server) handleHTTPReport(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	acc, err := s.st.GetAccountByTokenHash(auth.HashToken(token))
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxMessageSize))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	if onebot.IsEvent(body) {
		s.hub.HandleUpstreamFrame(acc.SelfID, body)
	}
	writeJSON(w, []byte(`{"status":"ok","retcode":0,"data":null}`))
}

func (s *Server) handleHTTPAction(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	bot, err := s.st.GetBotByTokenHash(auth.HashToken(token))
	if err != nil || !bot.Enabled {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	action := r.PathValue("action")
	params, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxMessageSize))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	if len(params) == 0 {
		params = []byte("{}")
	}
	if selfID := r.URL.Query().Get("self_id"); selfID != "" {
		var m map[string]json.RawMessage
		if json.Unmarshal(params, &m) == nil {
			q, _ := json.Marshal(selfID)
			m["self_id"] = q
			params, _ = json.Marshal(m)
		}
	}
	frame, _ := json.Marshal(map[string]json.RawMessage{
		"action": json.RawMessage(strconvQuote(action)),
		"params": params,
	})

	peer := newHTTPPeer()
	s.hub.HandleBotFrame(bot.ID, peer, "", frame)

	timeout := s.cfg.Policy.ActionTimeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	select {
	case resp := <-peer.ch:
		writeJSON(w, resp)
	case <-time.After(timeout + 2*time.Second):
		writeJSON(w, onebot.ErrorResponse(nil, onebot.RetInternal, "upstream timeout"))
	}
}

func writeJSON(w http.ResponseWriter, body []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func strconvQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

type httpPeer struct {
	id   string
	ch   chan []byte
	done chan struct{}
}

func newHTTPPeer() *httpPeer {
	return &httpPeer{id: "http-" + newID(), ch: make(chan []byte, 8), done: make(chan struct{})}
}

func (p *httpPeer) ID() string            { return p.id }
func (p *httpPeer) Role() string          { return "" }
func (p *httpPeer) Done() <-chan struct{} { return p.done }
func (p *httpPeer) Close()                {}

func (p *httpPeer) Send(raw []byte) bool {
	select {
	case p.ch <- raw:
		return true
	default:
		return false
	}
}
