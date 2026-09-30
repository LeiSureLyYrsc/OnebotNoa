package api

import (
	"context"
	"net/http"
	"sort"
	"strings"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/config"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/model"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/transport"
)

// listenerRequest is the create/update payload for a dedicated listener.
type listenerRequest struct {
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	BindAddr    string `json:"bind_addr"`
	Path        string `json:"path"`
	AccountID   *int64 `json:"account_id"`
	BotID       *int64 `json:"bot_id"`
	FixedSelfID string `json:"fixed_self_id"`
	TLSCert     string `json:"tls_cert"`
	TLSKey      string `json:"tls_key"`
	Enabled     *bool  `json:"enabled"`
}

// validateListener normalises and checks a dedicated listener definition.
func validateListener(req listenerRequest) (model.Listener, string) {
	l := model.Listener{
		Name:        strings.TrimSpace(req.Name),
		Kind:        strings.TrimSpace(req.Kind),
		BindAddr:    strings.TrimSpace(req.BindAddr),
		Path:        strings.TrimSpace(req.Path),
		AccountID:   req.AccountID,
		BotID:       req.BotID,
		FixedSelfID: strings.TrimSpace(req.FixedSelfID),
		TLSCert:     strings.TrimSpace(req.TLSCert),
		TLSKey:      strings.TrimSpace(req.TLSKey),
		Enabled:     req.Enabled == nil || *req.Enabled,
	}
	switch l.Kind {
	case config.KindUpstreamListen, config.KindDownstreamListen:
	default:
		return l, "kind 只能是 upstream_listen | downstream_listen"
	}
	if l.Name == "" {
		return l, "name 不能为空"
	}
	if l.BindAddr == "" {
		return l, "bind_addr 不能为空（例如 0.0.0.0:6710）"
	}
	if !strings.Contains(l.BindAddr, ":") {
		return l, "bind_addr 需要包含端口（例如 0.0.0.0:6710）"
	}
	if !strings.HasPrefix(l.Path, "/") {
		return l, "path 必须以 / 开头"
	}
	if (l.TLSCert == "") != (l.TLSKey == "") {
		return l, "tls_cert 与 tls_key 需要同时提供"
	}
	return l, ""
}

// ListenerRuntime is the dedicated-listener manager the API drives.
type ListenerRuntime interface {
	States() []transport.ListenerState
	Reload(ctx context.Context)
}

// listenerView is a stored listener row plus its live runtime state. The row is
// the desired configuration; the extra fields say what is actually bound.
type listenerView struct {
	model.Listener
	Runtime   string `json:"runtime"`
	URL       string `json:"url,omitempty"`
	LastError string `json:"last_error,omitempty"`
	StartedAt string `json:"started_at,omitempty"`
	Source    string `json:"source,omitempty"`
}

// runtimeStates indexes the manager's live state by listener name.
func (s *Server) runtimeStates() map[string]transport.ListenerState {
	index := map[string]transport.ListenerState{}
	if s.opt.Listeners == nil {
		return index
	}
	for _, state := range s.opt.Listeners.States() {
		index[state.Name] = state
	}
	return index
}

func (s *Server) listenerViews(r *http.Request) ([]listenerView, error) {
	rows, err := s.opt.Store.ListListeners(r.Context())
	if err != nil {
		return nil, err
	}
	states := s.runtimeStates()
	out := make([]listenerView, 0, len(rows)+len(states))
	seen := map[string]bool{}
	for _, l := range rows {
		view := listenerView{Listener: l, Runtime: "pending"}
		if state, ok := states[l.Name]; ok {
			seen[l.Name] = true
			view.Runtime = state.State
			view.URL = state.URL
			view.LastError = state.LastError
			view.StartedAt = state.StartedAt
			view.Source = state.Source
		} else if !l.Enabled {
			view.Runtime = "disabled"
		}
		out = append(out, view)
	}
	// Listeners that only exist in config.yaml are part of the same picture.
	for name, state := range states {
		if seen[name] {
			continue
		}
		out = append(out, listenerView{
			Listener: model.Listener{
				Name: name, Kind: state.Kind, BindAddr: state.Addr, Path: state.Path,
				FixedSelfID: state.FixedSelfID, Enabled: state.Enabled,
			},
			Runtime: state.State, URL: state.URL, LastError: state.LastError,
			StartedAt: state.StartedAt, Source: state.Source,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// viewFor returns the live view of one stored listener row.
func (s *Server) viewFor(listener model.Listener) listenerView {
	view := listenerView{Listener: listener, Runtime: "pending"}
	if !listener.Enabled {
		view.Runtime = "disabled"
	}
	if state, ok := s.runtimeStates()[listener.Name]; ok {
		view.Runtime = state.State
		view.URL = state.URL
		view.LastError = state.LastError
		view.StartedAt = state.StartedAt
		view.Source = state.Source
	}
	return view
}

// dialerListenerViews reloads the list after a mutation (named for symmetry with
// the endpoints API; the listener runtime is what actually changed).
func (s *Server) dialerListenerViews(r *http.Request) []listenerView {
	views, err := s.listenerViews(r)
	if err != nil {
		return []listenerView{}
	}
	return views
}

// reloadListeners hot-applies the desired listener set.
func (s *Server) reloadListeners(ctx context.Context) {
	if s.opt.Listeners != nil {
		s.opt.Listeners.Reload(ctx)
	}
}

func (s *Server) handleListListeners(w http.ResponseWriter, r *http.Request) {
	views, err := s.listenerViews(r)
	if err != nil {
		s.fail(w, "读取监听端点失败", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"listeners": views,
		"shared": map[string]any{
			"upstream_path":   s.opt.Config.OneBot.UpstreamWS.Path,
			"downstream_path": s.opt.Config.OneBot.DownstreamWS.Path,
			"listen":          s.opt.Config.Server.Listen,
		},
		"runtime_note": "source=config 的条目来自 config.yaml；运行时状态为实际绑定结果，端口被占用会在 last_error 中报告",
	})
}

func (s *Server) handleCreateListener(w http.ResponseWriter, r *http.Request) {
	var req listenerRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求体不是合法的 JSON")
		return
	}
	listener, problem := validateListener(req)
	if problem != "" {
		writeError(w, http.StatusBadRequest, problem)
		return
	}
	created, err := s.opt.Store.CreateListener(r.Context(), listener)
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "该 bind_addr + path 已被占用")
			return
		}
		s.fail(w, "创建监听端点失败", err)
		return
	}
	s.audit(r, s.actor(r), "listener.create", created.BindAddr+created.Path, "")
	s.reloadListeners(r.Context())
	writeJSON(w, http.StatusCreated, map[string]any{
		"listener":  s.viewFor(created),
		"listeners": s.dialerListenerViews(r),
	})
}

func (s *Server) handleUpdateListener(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r)
	if !ok {
		return
	}
	existing, err := s.opt.Store.ListenerByID(r.Context(), id)
	if err != nil {
		s.notFoundOrFail(w, "监听端点不存在", err)
		return
	}
	var req listenerRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求体不是合法的 JSON")
		return
	}
	updated, problem := validateListener(req)
	if problem != "" {
		writeError(w, http.StatusBadRequest, problem)
		return
	}
	updated.ID = existing.ID
	if err := s.opt.Store.UpdateListener(r.Context(), updated); err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "该 bind_addr + path 已被占用")
			return
		}
		s.fail(w, "更新监听端点失败", err)
		return
	}
	s.audit(r, s.actor(r), "listener.update", updated.BindAddr+updated.Path, "")
	s.reloadListeners(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"listener":  s.viewFor(updated),
		"listeners": s.dialerListenerViews(r),
	})
}

func (s *Server) handleDeleteListener(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r)
	if !ok {
		return
	}
	listener, err := s.opt.Store.ListenerByID(r.Context(), id)
	if err != nil {
		s.notFoundOrFail(w, "监听端点不存在", err)
		return
	}
	if err := s.opt.Store.DeleteListener(r.Context(), id); err != nil {
		s.fail(w, "删除监听端点失败", err)
		return
	}
	s.audit(r, s.actor(r), "listener.delete", listener.BindAddr+listener.Path, "")
	s.reloadListeners(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"deleted": listener.Name, "listeners": s.dialerListenerViews(r)})
}
