package api

import (
	"context"
	"net/http"
	"sort"
	"strings"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/connect"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/transport"
)

// ListenerRuntime is the dedicated-listener manager the API drives.
type ListenerRuntime interface {
	States() []transport.ListenerState
	Reload(ctx context.Context)
}

// listenerView is a stored connection plus its live runtime state. connect.json
// holds the desired configuration; the extra fields say what is actually bound.
type listenerView struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	Kind          string `json:"kind"`
	BindAddr      string `json:"bind_addr"`
	Path          string `json:"path"`
	AccountSelfID string `json:"account_self_id,omitempty"`
	BotName       string `json:"bot_name,omitempty"`
	FixedSelfID   string `json:"fixed_self_id,omitempty"`
	TLSCert       string `json:"tls_cert,omitempty"`
	Enabled       bool   `json:"enabled"`
	Runtime       string `json:"runtime"`
	URL           string `json:"url,omitempty"`
	LastError     string `json:"last_error,omitempty"`
	StartedAt     string `json:"started_at,omitempty"`
	Source        string `json:"source,omitempty"`
}

// runtimeStates indexes the manager live state by connection name.
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

// listenerViews joins connect.json with the runtime view.
func (s *Server) listenerViews(r *http.Request) ([]listenerView, error) {
	states := s.runtimeStates()
	rows := s.opt.Connections.ListConnections()
	out := make([]listenerView, 0, len(rows))
	seen := map[string]bool{}
	for _, connection := range rows {
		if connection.Kind != connect.KindUpstreamListen && connection.Kind != connect.KindDownstreamListen {
			continue
		}
		view := s.viewFor(connection)
		seen[connection.Name] = true
		out = append(out, view)
	}
	// A listener the runtime knows but the file does not (for example one being
	// reconciled away) still has to show up, otherwise the page would lie.
	for name, state := range states {
		if seen[name] {
			continue
		}
		out = append(out, listenerView{
			Name: name, Kind: state.Kind, BindAddr: state.Addr, Path: state.Path,
			FixedSelfID: state.FixedSelfID, Enabled: state.Enabled,
			Runtime: state.State, URL: state.URL, LastError: state.LastError,
			StartedAt: state.StartedAt, Source: state.Source,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// viewFor returns the live view of one configured connection.
func (s *Server) viewFor(connection connect.Connection) listenerView {
	view := listenerView{
		ID: connection.ID, Name: connection.Name, Kind: connection.Kind,
		BindAddr: connection.Addr, Path: connection.Path,
		AccountSelfID: connection.AccountSelfID, BotName: connection.BotName,
		FixedSelfID: connection.FixedSelfID, TLSCert: connection.TLSCert,
		Enabled: connection.Enabled, Runtime: "pending", Source: "connect.json",
	}
	if !connection.Enabled {
		view.Runtime = "disabled"
	}
	if state, ok := s.runtimeStates()[connection.Name]; ok {
		view.Runtime = state.State
		view.URL = state.URL
		view.LastError = state.LastError
		view.StartedAt = state.StartedAt
		view.Source = state.Source
	}
	return view
}

// connectionViews returns every connection (all four kinds) for the UI.
func (s *Server) connectionViews() []connect.Connection {
	if s.opt.Connections == nil {
		return []connect.Connection{}
	}
	rows := s.opt.Connections.ListConnections()
	// The sealed token is never serialised: it is unreadable to the browser and
	// would only leak a fingerprint.
	for i := range rows {
		rows[i].Grant.Token = ""
		rows[i].Grant.TokenHash = ""
		rows[i].TLSKey = maskSecret(rows[i].TLSKey)
	}
	return rows
}

// firstNonEmpty returns the first non-blank value.
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func maskSecret(value string) string {
	if value == "" {
		return ""
	}
	return "***"
}

// dialerListenerViews reloads the listener list after a mutation.
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
	if s.opt.Connections == nil {
		writeError(w, http.StatusServiceUnavailable, "连接配置文件尚未就绪")
		return
	}
	views, err := s.listenerViews(r)
	if err != nil {
		s.fail(w, "读取监听端点失败", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"listeners":   views,
		"connections": s.connectionViews(),
		"shared": map[string]any{
			"upstream_path":   s.opt.Config.OneBot.UpstreamWS.Path,
			"downstream_path": s.opt.Config.OneBot.DownstreamWS.Path,
			"listen":          s.opt.Config.Server.Listen,
		},
		"file": s.connectionsFileInfo(),
		"runtime_note": "Runtime 是实际绑定结果；端口被占用会在 last_error 中报告。" +
			"所有条目都来自 connect.json，改完立即生效、无需重启。",
	})
}

// --------------------------------------------------------------- CRUD

// The listener routes are the create/update/delete entry point the WebUI has
// always used. They now write connect.json rows, so they share the validation of
// the connections API and simply default the kind to a listener.
func (s *Server) handleCreateListener(w http.ResponseWriter, r *http.Request) {
	var req connectionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求体不是合法的 JSON")
		return
	}
	if req.Kind == "" {
		req.Kind = connect.KindUpstreamListen
	}
	if req.Kind != connect.KindUpstreamListen && req.Kind != connect.KindDownstreamListen {
		writeError(w, http.StatusBadRequest, "kind 只能是 upstream_listen | downstream_listen")
		return
	}
	if req.BotID > 0 && req.BotName == "" {
		if bot, ok := s.opt.Connections.BotByID(req.BotID); ok {
			req.BotName = bot.Name
		}
	}
	if req.AccountID > 0 && req.AccountSelfID == "" {
		if account, ok := s.opt.Connections.AccountByID(req.AccountID); ok {
			req.AccountSelfID = account.SelfID
		}
	}
	connection, problem := s.validateConnection(req)
	if problem != "" {
		writeError(w, http.StatusBadRequest, problem)
		return
	}
	if _, exists := s.opt.Connections.ConnectionByName(connection.Name); exists {
		writeError(w, http.StatusConflict, "同名连接已存在")
		return
	}
	created, err := s.opt.Connections.CreateConnection(connection, "")
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "同名连接已存在")
			return
		}
		s.fail(w, "创建监听端点失败", err)
		return
	}
	s.audit(r, s.actor(r), "listener.create", created.Addr+created.Path, "")
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
	existing, found := s.opt.Connections.ConnectionByID(id)
	if !found {
		writeError(w, http.StatusNotFound, "监听端点不存在")
		return
	}
	var req connectionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求体不是合法的 JSON")
		return
	}
	req.Kind = existing.Kind
	if req.BotID > 0 && req.BotName == "" {
		if bot, ok := s.opt.Connections.BotByID(req.BotID); ok {
			req.BotName = bot.Name
		}
	}
	if req.AccountID > 0 && req.AccountSelfID == "" {
		if account, ok := s.opt.Connections.AccountByID(req.AccountID); ok {
			req.AccountSelfID = account.SelfID
		}
	}
	connection, problem := s.validateConnection(req)
	if problem != "" {
		writeError(w, http.StatusBadRequest, problem)
		return
	}
	connection.ID = id
	if err := s.opt.Connections.UpdateConnection(connection, "", false); err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "同名连接已存在")
			return
		}
		s.fail(w, "更新监听端点失败", err)
		return
	}
	s.audit(r, s.actor(r), "listener.update", connection.Addr+connection.Path, "")
	s.reloadListeners(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"listener":  s.viewFor(connection),
		"listeners": s.dialerListenerViews(r),
	})
}

func (s *Server) handleDeleteListener(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r)
	if !ok {
		return
	}
	connection, found := s.opt.Connections.ConnectionByID(id)
	if !found {
		writeError(w, http.StatusNotFound, "监听端点不存在")
		return
	}
	if err := s.opt.Connections.DeleteConnection(id); err != nil {
		s.fail(w, "删除监听端点失败", err)
		return
	}
	s.audit(r, s.actor(r), "listener.delete", connection.Addr+connection.Path, "")
	s.reloadListeners(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"deleted":   connection.Name,
		"listeners": s.dialerListenerViews(r),
	})
}
