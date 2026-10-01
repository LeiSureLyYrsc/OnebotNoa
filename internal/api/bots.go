package api

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/auth"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/connect"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/hub"
)

// stringsTrim is strings.TrimSpace under a short name: this file trims several
// user-supplied names and the indirection keeps the calls readable.
func stringsTrim(value string) string { return strings.TrimSpace(value) }

// botView is the API shape of a downstream application.
type botView struct {
	connect.Bot
	Connections  int      `json:"connections"`
	BindingCount int      `json:"binding_count"`
	HasToken     bool     `json:"has_token"`
	Token        string   `json:"token,omitempty"`
	Endpoints    []string `json:"endpoints"`
	ClientURL    string   `json:"client_url,omitempty"`
}

func (s *Server) botView(bot connect.Bot, bindings int, showToken bool) botView {
	view := botView{
		Bot:          bot,
		Connections:  len(s.opt.Hub.DownstreamsForBot(bot.ID)),
		BindingCount: bindings,
		HasToken:     bot.Grant.Token != "",
		Endpoints:    s.botEndpoints(bot.Name),
	}
	if showToken {
		if token, err := s.opt.Connections.BotToken(bot.ID); err == nil {
			view.Token = token
			if len(view.Endpoints) > 0 {
				view.ClientURL = view.Endpoints[0] + "/" + token
			}
		}
	}
	return view
}

// botEndpoints lists the addresses this Bot can connect to.
func (s *Server) botEndpoints(botName string) []string {
	out := []string{}
	if s.opt.Config == nil {
		return out
	}
	base := "ws://" + publicBase(s.opt.Config.Server.Listen)
	if s.opt.Config.OneBot.DownstreamWS.Enable {
		out = append(out, base+s.opt.Config.OneBot.DownstreamWS.Path)
	}
	if s.opt.Connections == nil {
		return out
	}
	for _, connection := range s.opt.Connections.ListConnections() {
		if connection.Kind != connect.KindDownstreamListen || !connection.Enabled {
			continue
		}
		if connection.BotName != botName {
			continue
		}
		scheme := "ws"
		if connection.TLSCert != "" && connection.TLSKey != "" {
			scheme = "wss"
		}
		path := connection.Path
		if path == "" {
			path = "/onebot/v11/bot/ws"
		}
		out = append(out, scheme+"://"+displayHost(connection.Addr)+path)
	}
	return out
}

func (s *Server) handleListBots(w http.ResponseWriter, r *http.Request) {
	if s.opt.Connections == nil {
		writeError(w, http.StatusServiceUnavailable, "连接配置文件尚未就绪")
		return
	}
	bots := s.opt.Connections.ListBots()
	bindings := s.opt.Connections.ListBindings()
	counts := map[string]int{}
	for _, b := range bindings {
		counts[b.BotName]++
	}

	views := make([]botView, 0, len(bots))
	for _, bot := range bots {
		views = append(views, s.botView(bot, counts[bot.Name], false))
	}
	writeJSON(w, http.StatusOK, map[string]any{"bots": views})
}

func (s *Server) handleGetBot(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r)
	if !ok {
		return
	}
	bot, found := s.opt.Connections.BotByID(id)
	if !found {
		writeError(w, http.StatusNotFound, "Bot 不存在")
		return
	}
	bindings := s.opt.Connections.BindingsByBot(bot.Name)
	writeJSON(w, http.StatusOK, map[string]any{
		"bot":      s.botView(bot, len(bindings), true),
		"bindings": s.bindingViewsForBot(bot.Name),
	})
}

type botRequest struct {
	Name         string          `json:"name"`
	Note         string          `json:"note"`
	Enabled      *bool           `json:"enabled"`
	RateLimit    json.RawMessage `json:"rate_limit"`
	ActionPolicy json.RawMessage `json:"action_policy"`
}

func (s *Server) handleCreateBot(w http.ResponseWriter, r *http.Request) {
	var req botRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求体不是合法的 JSON")
		return
	}
	req.Name = stringsTrim(req.Name)
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name 不能为空")
		return
	}
	if problem := jsonKindProblem(req.RateLimit, "object"); problem != "" {
		writeError(w, http.StatusBadRequest, "rate_limit "+problem)
		return
	}
	if problem := jsonKindProblem(req.ActionPolicy, "object"); problem != "" {
		writeError(w, http.StatusBadRequest, "action_policy "+problem)
		return
	}

	plain, hash, err := auth.NewToken()
	if err != nil {
		s.fail(w, "生成 token 失败", err)
		return
	}
	bot, err := s.opt.Connections.CreateBotWithToken(req.Name, plain, hash, req.Note)
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "同名 Bot 已存在")
			return
		}
		s.fail(w, "创建 Bot 失败", err)
		return
	}
	if len(req.RateLimit) > 0 || len(req.ActionPolicy) > 0 {
		if len(req.RateLimit) > 0 {
			bot.RateLimit = req.RateLimit
		}
		if len(req.ActionPolicy) > 0 {
			bot.ActionPolicy = req.ActionPolicy
		}
		if err := s.opt.Connections.UpdateBot(bot); err != nil {
			s.fail(w, "保存 Bot 策略失败", err)
			return
		}
	}

	s.audit(r, s.actor(r), "bot.create", bot.Name, "")
	writeJSON(w, http.StatusCreated, map[string]any{
		"bot":   s.botView(bot, 0, false),
		"token": plain,
		"hint":  "token 同时写入 connect.json（本地密钥加密），随时可在本页重新查看；Bot 侧用它连接 /onebot/v11/bot/ws/{token}",
	})
}

func (s *Server) handleUpdateBot(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r)
	if !ok {
		return
	}
	bot, found := s.opt.Connections.BotByID(id)
	if !found {
		writeError(w, http.StatusNotFound, "Bot 不存在")
		return
	}

	var req botRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求体不是合法的 JSON")
		return
	}
	if name := stringsTrim(req.Name); name != "" {
		bot.Name = name
	}
	bot.Note = req.Note
	if req.Enabled != nil {
		bot.Enabled = *req.Enabled
	}
	if problem := jsonKindProblem(req.RateLimit, "object"); problem != "" {
		writeError(w, http.StatusBadRequest, "rate_limit "+problem)
		return
	}
	if len(req.RateLimit) > 0 {
		bot.RateLimit = req.RateLimit
	}
	if problem := jsonKindProblem(req.ActionPolicy, "object"); problem != "" {
		writeError(w, http.StatusBadRequest, "action_policy "+problem)
		return
	}
	if len(req.ActionPolicy) > 0 {
		bot.ActionPolicy = req.ActionPolicy
	}

	if err := s.opt.Connections.UpdateBot(bot); err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "同名 Bot 已存在")
			return
		}
		s.fail(w, "更新 Bot 失败", err)
		return
	}
	s.audit(r, s.actor(r), "bot.update", bot.Name, "")

	if !bot.Enabled {
		for _, conn := range s.opt.Hub.DownstreamsForBot(bot.ID) {
			conn.Close(1008, "bot disabled by an administrator")
		}
	}
	s.opt.Hub.RefreshBindings(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"bot": s.botView(bot, 0, false)})
}

func (s *Server) handleDeleteBot(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r)
	if !ok {
		return
	}
	bot, found := s.opt.Connections.BotByID(id)
	if !found {
		writeError(w, http.StatusNotFound, "Bot 不存在")
		return
	}
	for _, conn := range s.opt.Hub.DownstreamsForBot(bot.ID) {
		conn.Close(1008, "bot deleted by an administrator")
	}
	if err := s.opt.Connections.DeleteBot(id); err != nil {
		s.fail(w, "删除 Bot 失败", err)
		return
	}
	s.audit(r, s.actor(r), "bot.delete", bot.Name, "")
	s.opt.Hub.RefreshBindings(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"deleted": bot.Name})
}

// handleRotateBotToken issues a fresh token (invalidating the old one).
func (s *Server) handleRotateBotToken(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r)
	if !ok {
		return
	}
	bot, found := s.opt.Connections.BotByID(id)
	if !found {
		writeError(w, http.StatusNotFound, "Bot 不存在")
		return
	}
	plain, hash, err := auth.NewToken()
	if err != nil {
		s.fail(w, "生成 token 失败", err)
		return
	}
	if err := s.opt.Connections.SetBotToken(bot.ID, plain, hash); err != nil {
		s.fail(w, "保存 token 失败", err)
		return
	}
	s.audit(r, s.actor(r), "bot.token.rotate", bot.Name, "")
	writeJSON(w, http.StatusOK, map[string]any{
		"bot":   s.botView(bot, 0, false),
		"token": plain,
		"hint":  "旧 token 立即失效；已连接的会话不受影响",
	})
}

// bindingViewsForBot lists the grants of one Bot with account names attached.
func (s *Server) bindingViewsForBot(botName string) []bindingView {
	return s.bindingViewsOf(s.opt.Connections.BindingsByBot(botName))
}

// bindingViewsFor lists the grants of one account.
func (s *Server) bindingViewsFor(selfID string) []bindingView {
	return s.bindingViewsOf(s.opt.Connections.BindingsByAccount(selfID))
}

func (s *Server) bindingViewsOf(rows []connect.Binding) []bindingView {
	out := make([]bindingView, 0, len(rows))
	for _, binding := range rows {
		out = append(out, s.bindingView(binding))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AccountSelfID < out[j].AccountSelfID })
	return out
}

var _ = hub.Bot{}
