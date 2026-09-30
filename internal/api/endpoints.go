package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/config"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/model"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/transport"
)

// Dialer is the outbound-connection manager the API drives.
type Dialer interface {
	States() []transport.EndpointState
	ForceReconnect(name string) bool
	Reload(ctx context.Context)
}

// endpointRequest is the create/update payload for a dial target.
type endpointRequest struct {
	Name        string          `json:"name"`
	Kind        string          `json:"kind"`
	URL         string          `json:"url"`
	Mode        string          `json:"mode"`
	Token       string          `json:"token"`
	AccountHint string          `json:"account_hint"`
	BotID       *int64          `json:"bot_id"`
	FixedSelfID string          `json:"fixed_self_id"`
	Enabled     *bool           `json:"enabled"`
	Reconnect   json.RawMessage `json:"reconnect"`
}

// validateEndpoint normalises and checks a dial target definition.
func validateEndpoint(req endpointRequest) (model.Endpoint, string) {
	ep := model.Endpoint{
		Name:        strings.TrimSpace(req.Name),
		Kind:        strings.TrimSpace(req.Kind),
		URL:         strings.TrimSpace(req.URL),
		Mode:        strings.TrimSpace(req.Mode),
		Token:       strings.TrimSpace(req.Token),
		AccountHint: strings.TrimSpace(req.AccountHint),
		BotID:       req.BotID,
		Reconnect:   json.RawMessage("{}"),
		Enabled:     req.Enabled == nil || *req.Enabled,
	}
	if ep.Mode == "" {
		ep.Mode = "universal"
	}
	if ep.Name == "" {
		return ep, "name 不能为空"
	}
	switch ep.Kind {
	case config.KindUpstreamDial, config.KindDownstreamDial:
	default:
		return ep, "kind 只能是 upstream_dial | downstream_dial"
	}
	if !strings.HasPrefix(ep.URL, "ws://") && !strings.HasPrefix(ep.URL, "wss://") {
		return ep, "url 必须以 ws:// 或 wss:// 开头"
	}
	switch ep.Mode {
	case "universal", "split":
	default:
		return ep, "mode 只能是 universal | split"
	}
	if ep.Kind == config.KindDownstreamDial && (req.BotID == nil || *req.BotID <= 0) {
		return ep, "下游拨号必须指定 bot_id"
	}
	if ep.Kind == config.KindUpstreamDial && req.BotID != nil {
		return ep, "上游拨号不需要 bot_id"
	}
	if problem := jsonKindProblem(req.Reconnect, "object"); problem != "" {
		return ep, "reconnect " + problem
	}
	if len(req.Reconnect) > 0 {
		var wire struct {
			Min    string  `json:"min"`
			Max    string  `json:"max"`
			Jitter float64 `json:"jitter"`
		}
		if err := json.Unmarshal(req.Reconnect, &wire); err != nil {
			return ep, "reconnect 不是合法的 JSON"
		}
		for _, raw := range []string{wire.Min, wire.Max} {
			if raw == "" {
				continue
			}
			if _, err := time.ParseDuration(raw); err != nil {
				return ep, "reconnect 的时间格式需要形如 1s / 60s"
			}
		}
		if wire.Jitter < 0 || wire.Jitter > 1 {
			return ep, "reconnect.jitter 取值 0~1"
		}
		ep.Reconnect = req.Reconnect
	}
	return ep, ""
}

func (s *Server) handleListEndpoints(w http.ResponseWriter, r *http.Request) {
	if s.opt.Dialer == nil {
		writeJSON(w, http.StatusOK, map[string]any{"endpoints": []any{}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"endpoints": s.opt.Dialer.States(),
		"note": "source=config 的条目来自 config.yaml（只读）；source=database 的条目可在此增删改。" +
			"token 保存在本地 SQLite（明文，拨号需要）；也可以只在 config.yaml 里配置。",
	})
}

func (s *Server) handleCreateEndpoint(w http.ResponseWriter, r *http.Request) {
	var req endpointRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求体不是合法的 JSON")
		return
	}
	endpoint, problem := validateEndpoint(req)
	if problem != "" {
		writeError(w, http.StatusBadRequest, problem)
		return
	}
	if _, err := s.opt.Store.EndpointByName(r.Context(), endpoint.Name); err == nil {
		writeError(w, http.StatusConflict, "同名拨号目标已存在")
		return
	}
	created, err := s.opt.Store.CreateEndpoint(r.Context(), endpoint)
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "同名拨号目标已存在")
			return
		}
		s.fail(w, "创建拨号目标失败", err)
		return
	}
	s.audit(r, s.actor(r), "endpoint.create", created.Name, "")
	s.reloadDialer(r.Context())
	writeJSON(w, http.StatusCreated, map[string]any{"endpoints": s.dialerStates()})
}

func (s *Server) handleUpdateEndpoint(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r)
	if !ok {
		return
	}
	existing, err := s.opt.Store.EndpointByID(r.Context(), id)
	if err != nil {
		s.notFoundOrFail(w, "拨号目标不存在", err)
		return
	}
	var req endpointRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求体不是合法的 JSON")
		return
	}
	updated, problem := validateEndpoint(req)
	if problem != "" {
		writeError(w, http.StatusBadRequest, problem)
		return
	}
	updated.ID = existing.ID
	// An empty token keeps the stored one: the API never returns it, so the UI
	// cannot round-trip it.
	if updated.Token == "" {
		updated.Token = existing.Token
	}
	if err := s.opt.Store.UpdateEndpoint(r.Context(), updated); err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "同名拨号目标已存在")
			return
		}
		s.fail(w, "更新拨号目标失败", err)
		return
	}
	s.audit(r, s.actor(r), "endpoint.update", updated.Name, "")
	s.reloadDialer(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"endpoints": s.dialerStates()})
}

func (s *Server) handleDeleteEndpoint(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r)
	if !ok {
		return
	}
	endpoint, err := s.opt.Store.EndpointByID(r.Context(), id)
	if err != nil {
		s.notFoundOrFail(w, "拨号目标不存在", err)
		return
	}
	if err := s.opt.Store.DeleteEndpoint(r.Context(), id); err != nil {
		s.fail(w, "删除拨号目标失败", err)
		return
	}
	s.audit(r, s.actor(r), "endpoint.delete", endpoint.Name, "")
	s.reloadDialer(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"deleted": endpoint.Name, "endpoints": s.dialerStates()})
}

// handleReconnectEndpoint forces an immediate redial.
func (s *Server) handleReconnectEndpoint(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.PathValue("name"))
	if name == "" {
		writeError(w, http.StatusBadRequest, "缺少拨号目标名称")
		return
	}
	if s.opt.Dialer == nil || !s.opt.Dialer.ForceReconnect(name) {
		writeError(w, http.StatusNotFound, "拨号目标不存在或未在运行")
		return
	}
	s.audit(r, s.actor(r), "endpoint.reconnect", name, "")
	writeJSON(w, http.StatusOK, map[string]any{"reconnecting": name, "endpoints": s.dialerStates()})
}

func (s *Server) reloadDialer(ctx context.Context) {
	if s.opt.Dialer != nil {
		s.opt.Dialer.Reload(ctx)
	}
}

func (s *Server) dialerStates() []transport.EndpointState {
	if s.opt.Dialer == nil {
		return []transport.EndpointState{}
	}
	return s.opt.Dialer.States()
}
