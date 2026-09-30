package api

import (
	"net/http"
	"strings"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/config"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/model"
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

// listenerView adds the runtime state of a dedicated listener.
type listenerView struct {
	model.Listener
	Runtime string `json:"runtime"`
}

func (s *Server) listenerViews(r *http.Request) ([]listenerView, error) {
	rows, err := s.opt.Store.ListListeners(r.Context())
	if err != nil {
		return nil, err
	}
	out := make([]listenerView, 0, len(rows))
	for _, l := range rows {
		state := "pending"
		if !l.Enabled {
			state = "disabled"
		}
		out = append(out, listenerView{Listener: l, Runtime: state})
	}
	return out, nil
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
		"runtime_note": "独立监听端口的运行时启停随增量 I9 提供；此处先持久化配置",
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
	writeJSON(w, http.StatusCreated, map[string]any{
		"listener": listenerView{Listener: created, Runtime: "pending"},
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
	state := "pending"
	if !updated.Enabled {
		state = "disabled"
	}
	writeJSON(w, http.StatusOK, map[string]any{"listener": listenerView{Listener: updated, Runtime: state}})
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
	writeJSON(w, http.StatusOK, map[string]any{"deleted": listener.Name})
}
