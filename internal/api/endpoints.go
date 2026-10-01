package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/connect"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/transport"
)

// Dialer is the outbound-connection manager the API drives.
type Dialer interface {
	States() []transport.EndpointState
	ForceReconnect(name string) bool
	Reload(ctx context.Context)
}

// connectionRequest is the create/update payload of one connection. It covers
// all four kinds: a dial target uses url, a dedicated listener uses addr.
type connectionRequest struct {
	Name          string  `json:"name"`
	Kind          string  `json:"kind"`
	Addr          string  `json:"addr"`
	BindAddr      string  `json:"bind_addr"`
	URL           string  `json:"url"`
	Path          string  `json:"path"`
	Mode          string  `json:"mode"`
	Token         string  `json:"token"`
	AccountSelfID string  `json:"account_self_id"`
	AccountID     int64   `json:"account_id"`
	BotName       string  `json:"bot_name"`
	BotID         int64   `json:"bot_id"`
	FixedSelfID   string  `json:"fixed_self_id"`
	TLSCert       string  `json:"tls_cert"`
	TLSKey        string  `json:"tls_key"`
	Enabled       *bool   `json:"enabled"`
	Min           string  `json:"min"`
	Max           string  `json:"max"`
	Jitter        float64 `json:"jitter"`
	// Reconnect is the nested form the WebUI sends (and config.yaml used to).
	Reconnect *reconnectRequest `json:"reconnect"`
}

// reconnectRequest is the nested backoff payload.
type reconnectRequest struct {
	Min    string  `json:"min"`
	Max    string  `json:"max"`
	Jitter float64 `json:"jitter"`
}

// validateConnection normalises and checks a connection definition.
func (s *Server) validateConnection(req connectionRequest) (connect.Connection, string) {
	connection := connect.Connection{
		Name: strings.TrimSpace(req.Name),
		Kind: strings.TrimSpace(req.Kind),
		// bind_addr is the older field name; accepting both keeps existing
		// clients working after the two connection kinds were unified.
		Addr:          strings.TrimSpace(firstNonEmpty(req.Addr, req.BindAddr)),
		URL:           strings.TrimSpace(req.URL),
		Path:          strings.TrimSpace(req.Path),
		Mode:          strings.TrimSpace(req.Mode),
		AccountSelfID: strings.TrimSpace(req.AccountSelfID),
		BotName:       strings.TrimSpace(req.BotName),
		FixedSelfID:   strings.TrimSpace(req.FixedSelfID),
		TLSCert:       strings.TrimSpace(req.TLSCert),
		TLSKey:        strings.TrimSpace(req.TLSKey),
		Enabled:       req.Enabled == nil || *req.Enabled,
	}
	if connection.Mode == "" {
		connection.Mode = "universal"
	}
	if connection.Name == "" {
		return connection, "name 不能为空"
	}
	switch connection.Kind {
	case connect.KindUpstreamDial, connect.KindDownstreamDial:
		if !strings.HasPrefix(connection.URL, "ws://") && !strings.HasPrefix(connection.URL, "wss://") {
			return connection, "url 必须以 ws:// 或 wss:// 开头"
		}
		switch connection.Mode {
		case "universal", "split":
		default:
			return connection, "mode 只能是 universal | split"
		}
		if connection.Kind == connect.KindDownstreamDial {
			if connection.BotName == "" {
				return connection, "下游拨号必须指定 bot_name"
			}
			if _, ok := s.opt.Connections.BotByName(connection.BotName); !ok {
				return connection, "Bot " + connection.BotName + " 不存在"
			}
		} else {
			if connection.BotName != "" || req.BotID > 0 {
				return connection, "上游拨号不需要 bot_name"
			}
			if connection.AccountSelfID != "" {
				if _, ok := s.opt.Connections.AccountBySelfID(connection.AccountSelfID); !ok {
					return connection, "账号 " + connection.AccountSelfID + " 不存在"
				}
			}
		}
	case connect.KindUpstreamListen, connect.KindDownstreamListen:
		if connection.Addr == "" {
			return connection, "监听连接必须指定 addr（例如 0.0.0.0:6710）"
		}
		if !strings.Contains(connection.Addr, ":") {
			return connection, "addr 需要包含端口（例如 0.0.0.0:6710）"
		}
		if connection.Path == "" {
			if connection.Kind == connect.KindDownstreamListen {
				connection.Path = "/onebot/v11/bot/ws"
			} else {
				connection.Path = "/onebot/v11/ws"
			}
		}
		if !strings.HasPrefix(connection.Path, "/") {
			return connection, "path 必须以 / 开头"
		}
		if (connection.TLSCert == "") != (connection.TLSKey == "") {
			return connection, "tls_cert 与 tls_key 需要同时提供"
		}
		if connection.Kind == connect.KindUpstreamListen {
			if connection.AccountSelfID == "" {
				return connection, "上游独立监听必须指定 account_self_id"
			}
			if _, ok := s.opt.Connections.AccountBySelfID(connection.AccountSelfID); !ok {
				return connection, "账号 " + connection.AccountSelfID + " 不存在"
			}
		}
		if connection.Kind == connect.KindDownstreamListen {
			if connection.BotName == "" {
				return connection, "下游独立监听必须指定 bot_name"
			}
			if _, ok := s.opt.Connections.BotByName(connection.BotName); !ok {
				return connection, "Bot " + connection.BotName + " 不存在"
			}
		}
		if connection.URL != "" {
			return connection, "监听连接不使用 url，请改用 addr"
		}
	default:
		return connection, "kind 只能是 upstream_listen | upstream_dial | downstream_listen | downstream_dial"
	}

	// The backoff schedule arrives either nested ({"reconnect":{...}}) or flat
	// ({"min":"2s"}); both are accepted so the older WebUI payload keeps working.
	min, max, jitter := req.Min, req.Max, req.Jitter
	if req.Reconnect != nil {
		min, max, jitter = req.Reconnect.Min, req.Reconnect.Max, req.Reconnect.Jitter
	}
	if min != "" || max != "" || jitter != 0 {
		for _, raw := range []string{min, max} {
			if raw == "" {
				continue
			}
			if _, err := time.ParseDuration(raw); err != nil {
				return connection, "reconnect 的时间格式需要形如 1s / 60s"
			}
		}
		if jitter < 0 || jitter > 1 {
			return connection, "reconnect.jitter 取值 0~1"
		}
		connection.Reconnect = &connect.Reconnect{Min: min, Max: max, Jitter: jitter}
	}
	return connection, ""
}

func (s *Server) handleListEndpoints(w http.ResponseWriter, r *http.Request) {
	if s.opt.Dialer == nil {
		writeJSON(w, http.StatusOK, map[string]any{"endpoints": []any{}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"endpoints": s.opt.Dialer.States(),
		"note": "拨号目标与独立监听统一存放在 connect.json；这一页只显示该文件中的记录。" +
			"token 保存在 connect.json（本地密钥加密），随时可在页面上重新查看。",
	})
}

func (s *Server) handleCreateEndpoint(w http.ResponseWriter, r *http.Request) {
	var req connectionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求体不是合法的 JSON")
		return
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
	created, err := s.opt.Connections.CreateConnection(connection, strings.TrimSpace(req.Token))
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "同名连接已存在")
			return
		}
		s.fail(w, "创建连接失败", err)
		return
	}
	s.audit(r, s.actor(r), "connection.create", created.Name, created.Kind)
	s.reloadRuntime(r.Context())
	writeJSON(w, http.StatusCreated, map[string]any{"endpoints": s.dialerStates(), "connections": s.connectionViews()})
}

func (s *Server) handleUpdateEndpoint(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r)
	if !ok {
		return
	}
	_, found := s.opt.Connections.ConnectionByID(id)
	if !found {
		writeError(w, http.StatusNotFound, "连接不存在")
		return
	}
	var req connectionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求体不是合法的 JSON")
		return
	}
	connection, problem := s.validateConnection(req)
	if problem != "" {
		writeError(w, http.StatusBadRequest, problem)
		return
	}
	connection.ID = id
	// An empty token keeps the stored one: the API never returns it, so the UI
	// cannot round-trip it.
	if err := s.opt.Connections.UpdateConnection(connection, strings.TrimSpace(req.Token), req.Token != ""); err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "同名连接已存在")
			return
		}
		s.fail(w, "更新连接失败", err)
		return
	}
	s.audit(r, s.actor(r), "connection.update", connection.Name, connection.Kind)
	s.reloadRuntime(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"endpoints": s.dialerStates(), "connections": s.connectionViews()})
}

func (s *Server) handleDeleteEndpoint(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r)
	if !ok {
		return
	}
	connection, found := s.opt.Connections.ConnectionByID(id)
	if !found {
		writeError(w, http.StatusNotFound, "连接不存在")
		return
	}
	if err := s.opt.Connections.DeleteConnection(id); err != nil {
		s.fail(w, "删除连接失败", err)
		return
	}
	s.audit(r, s.actor(r), "connection.delete", connection.Name, connection.Kind)
	s.reloadRuntime(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"deleted":     connection.Name,
		"endpoints":   s.dialerStates(),
		"connections": s.connectionViews(),
	})
}

// handleReconnectEndpoint forces an immediate redial.
func (s *Server) handleReconnectEndpoint(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.PathValue("name"))
	if name == "" {
		writeError(w, http.StatusBadRequest, "缺少连接名称")
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

// reloadRuntime re-applies both halves of the data plane after a connection
// changed: the dial workers and the dedicated listeners.
func (s *Server) reloadRuntime(ctx context.Context) {
	s.reloadDialer(ctx)
	s.reloadListeners(ctx)
}

func (s *Server) dialerStates() []transport.EndpointState {
	if s.opt.Dialer == nil {
		return []transport.EndpointState{}
	}
	return s.opt.Dialer.States()
}
