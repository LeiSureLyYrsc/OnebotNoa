package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/auth"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/model"
)

// botView is the API shape of a downstream application.
type botView struct {
	model.Bot
	Connections  int `json:"connections"`
	BindingCount int `json:"binding_count"`
}

func (s *Server) botView(b model.Bot, bindings int) botView {
	return botView{
		Bot:          b,
		Connections:  len(s.opt.Hub.DownstreamsForBot(b.ID)),
		BindingCount: bindings,
	}
}

func (s *Server) handleListBots(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	bots, err := s.opt.Store.ListBots(ctx)
	if err != nil {
		s.fail(w, "读取 Bot 列表失败", err)
		return
	}
	bindings, err := s.opt.Store.ListBindings(ctx)
	if err != nil {
		s.fail(w, "读取绑定关系失败", err)
		return
	}
	counts := map[int64]int{}
	for _, b := range bindings {
		counts[b.BotID]++
	}

	views := make([]botView, 0, len(bots))
	for _, b := range bots {
		views = append(views, s.botView(b, counts[b.ID]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"bots": views})
}

func (s *Server) handleGetBot(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r)
	if !ok {
		return
	}
	bot, err := s.opt.Store.BotByID(r.Context(), id)
	if err != nil {
		s.notFoundOrFail(w, "Bot 不存在", err)
		return
	}
	bindings, _ := s.opt.Store.BindingsByBot(r.Context(), bot.ID)
	writeJSON(w, http.StatusOK, map[string]any{
		"bot":      s.botView(bot, len(bindings)),
		"bindings": bindings,
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
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name 不能为空")
		return
	}

	plain, hash, err := auth.NewToken()
	if err != nil {
		s.fail(w, "生成 token 失败", err)
		return
	}
	bot, err := s.opt.Store.CreateBot(r.Context(), req.Name, hash, req.Note)
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "同名 Bot 已存在")
			return
		}
		s.fail(w, "创建 Bot 失败", err)
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
	if len(req.RateLimit) > 0 || len(req.ActionPolicy) > 0 {
		if len(req.RateLimit) > 0 {
			bot.RateLimit = req.RateLimit
		}
		if len(req.ActionPolicy) > 0 {
			bot.ActionPolicy = req.ActionPolicy
		}
		if err := s.opt.Store.UpdateBot(r.Context(), bot); err != nil {
			s.fail(w, "保存 Bot 策略失败", err)
			return
		}
	}

	s.audit(r, s.actor(r), "bot.create", bot.Name, "")
	writeJSON(w, http.StatusCreated, map[string]any{
		"bot":   s.botView(bot, 0),
		"token": plain,
		"hint":  "该 token 只显示一次；Bot 侧用它连接 /onebot/v11/bot/ws/{token}",
	})
}

func (s *Server) handleUpdateBot(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r)
	if !ok {
		return
	}
	bot, err := s.opt.Store.BotByID(r.Context(), id)
	if err != nil {
		s.notFoundOrFail(w, "Bot 不存在", err)
		return
	}

	var req botRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求体不是合法的 JSON")
		return
	}
	if name := strings.TrimSpace(req.Name); name != "" {
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

	if err := s.opt.Store.UpdateBot(r.Context(), bot); err != nil {
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
	writeJSON(w, http.StatusOK, map[string]any{"bot": s.botView(bot, 0)})
}

func (s *Server) handleDeleteBot(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r)
	if !ok {
		return
	}
	bot, err := s.opt.Store.BotByID(r.Context(), id)
	if err != nil {
		s.notFoundOrFail(w, "Bot 不存在", err)
		return
	}
	for _, conn := range s.opt.Hub.DownstreamsForBot(bot.ID) {
		conn.Close(1008, "bot deleted by an administrator")
	}
	if err := s.opt.Store.DeleteBot(r.Context(), id); err != nil {
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
	bot, err := s.opt.Store.BotByID(r.Context(), id)
	if err != nil {
		s.notFoundOrFail(w, "Bot 不存在", err)
		return
	}
	plain, hash, err := auth.NewToken()
	if err != nil {
		s.fail(w, "生成 token 失败", err)
		return
	}
	if err := s.opt.Store.SetBotToken(r.Context(), bot.ID, hash); err != nil {
		s.fail(w, "保存 token 失败", err)
		return
	}
	s.audit(r, s.actor(r), "bot.token.rotate", bot.Name, "")
	writeJSON(w, http.StatusOK, map[string]any{
		"bot":   s.botView(bot, 0),
		"token": plain,
		"hint":  "旧 token 立即失效；已连接的会话不受影响",
	})
}
