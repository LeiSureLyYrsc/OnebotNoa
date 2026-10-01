package api

import (
	"encoding/json"
	"net/http"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/connect"
)

// handleGetConnections returns the whole connect.json document.
//
// This is the main reason the connection set is a file instead of a table: an
// operator can diff two deployments, hand-edit it, or seed a fresh instance
// from an existing one. Secrets come out sealed (or plaintext if the operator
// wrote them that way), so the export is exactly the file on disk.
func (s *Server) handleGetConnections(w http.ResponseWriter, r *http.Request) {
	if s.opt.Connections == nil {
		writeError(w, http.StatusServiceUnavailable, "连接配置文件尚未就绪")
		return
	}
	doc := s.opt.Connections.Document()
	writeJSON(w, http.StatusOK, map[string]any{
		"connect": doc,
		"path":    s.opt.Connections.Path(),
		"note": "这是 connect.json 的完整内容。token 已加密（本地密钥在 <path>.key），" +
			"因此可以直接用于备份或在另一台机器上恢复——但需要一起带走密钥文件。",
	})
}

// handlePutConnections replaces the document wholesale (import).
func (s *Server) handlePutConnections(w http.ResponseWriter, r *http.Request) {
	if s.opt.Connections == nil {
		writeError(w, http.StatusServiceUnavailable, "连接配置文件尚未就绪")
		return
	}
	var req struct {
		connect.Document
		Connect *connect.Document `json:"connect"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求体不是合法的 JSON")
		return
	}
	incoming := req.Document
	if req.Connect != nil {
		incoming = *req.Connect
	}
	updated, err := s.opt.Connections.Replace(incoming, s.connectServerInfo())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.audit(r, s.actor(r), "connections.import", s.opt.Connections.Path(), "")
	s.opt.Hub.InvalidateBindings()
	s.opt.Hub.RefreshBindings(r.Context())
	s.reloadRuntime(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"connect": updated,
		"note":    "已导入并保留原文件为 <path>.bak；连接会立即按新内容重建。",
	})
}

// handleDirectConnect returns a ready-to-use address plus credential for one
// account or Bot: the "just tell me what to paste" button.
func (s *Server) handleDirectConnect(w http.ResponseWriter, r *http.Request) {
	if s.opt.Connections == nil {
		writeError(w, http.StatusServiceUnavailable, "连接配置文件尚未就绪")
		return
	}
	kind := r.PathValue("kind")
	id, ok := parseOptionalID(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusBadRequest, "非法的 id")
		return
	}
	switch kind {
	case "account":
		account, found := s.opt.Connections.AccountByID(id)
		if !found {
			writeError(w, http.StatusNotFound, "账号不存在")
			return
		}
		token, err := s.opt.Connections.AccountToken(id)
		if err != nil {
			s.fail(w, "读取 token 失败", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"kind":      "account",
			"self_id":   account.SelfID,
			"token":     token,
			"has_token": token != "",
			"endpoints": s.accountEndpoints(account.SelfID),
			"headers":   map[string]string{"X-Self-ID": account.SelfID, "X-Client-Role": "Universal"},
			"hint": "在 QQ 实现端（NapCat / SnowLuma）填这个反向 WS 地址与 token；" +
				"token 为空时请先「生成 token」。",
		})
	case "bot":
		bot, found := s.opt.Connections.BotByID(id)
		if !found {
			writeError(w, http.StatusNotFound, "Bot 不存在")
			return
		}
		token, err := s.opt.Connections.BotToken(id)
		if err != nil {
			s.fail(w, "读取 token 失败", err)
			return
		}
		endpoints := s.botEndpoints(bot.Name)
		urls := make([]string, 0, len(endpoints))
		for _, endpoint := range endpoints {
			urls = append(urls, endpoint+"/"+token)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"kind":      "bot",
			"bot_name":  bot.Name,
			"token":     token,
			"endpoints": endpoints,
			"urls":      urls,
			"hint": "Bot 侧连接 urls 中的地址即可；" +
				"在地址末尾再加 /<self_id> 可以得到只暴露一个账号的透明视图。",
		})
	default:
		writeError(w, http.StatusBadRequest, "kind 只能是 account | bot")
	}
}

func parseOptionalID(raw string) (int64, bool) {
	if raw == "" {
		return 0, false
	}
	var id int64
	if err := json.Unmarshal([]byte(raw), &id); err != nil {
		return 0, false
	}
	return id, id > 0
}
