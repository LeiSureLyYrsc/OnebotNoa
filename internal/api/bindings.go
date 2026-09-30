package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/hub"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/model"
)

// bindingView is a grant plus the names the table needs.
type bindingView struct {
	model.Binding
	BotName       string `json:"bot_name"`
	AccountSelfID string `json:"account_self_id"`
	AccountName   string `json:"account_name"`
}

func (s *Server) bindingViews(r *http.Request) ([]bindingView, error) {
	ctx := r.Context()
	bindings, err := s.opt.Store.ListBindings(ctx)
	if err != nil {
		return nil, err
	}
	bots, err := s.opt.Store.ListBots(ctx)
	if err != nil {
		return nil, err
	}
	accounts, err := s.opt.Store.ListAccounts(ctx)
	if err != nil {
		return nil, err
	}
	botNames := map[int64]string{}
	for _, b := range bots {
		botNames[b.ID] = b.Name
	}
	accountNames := map[int64]struct{ selfID, name string }{}
	for _, a := range accounts {
		accountNames[a.ID] = struct{ selfID, name string }{a.SelfID, a.Name}
	}

	out := make([]bindingView, 0, len(bindings))
	for _, b := range bindings {
		acc := accountNames[b.AccountID]
		out = append(out, bindingView{
			Binding:       b,
			BotName:       botNames[b.BotID],
			AccountSelfID: acc.selfID,
			AccountName:   acc.name,
		})
	}
	return out, nil
}

func (s *Server) handleListBindings(w http.ResponseWriter, r *http.Request) {
	views, err := s.bindingViews(r)
	if err != nil {
		s.fail(w, "读取绑定关系失败", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"bindings": views})
}

type bindingRequest struct {
	BotID     int64           `json:"bot_id"`
	AccountID int64           `json:"account_id"`
	Priority  *int            `json:"priority"`
	IsDefault *bool           `json:"is_default"`
	Enabled   *bool           `json:"enabled"`
	Scope     json.RawMessage `json:"scope"`
}

// validateScope rejects malformed过滤器 so the routing path never has to guess.
func validateScope(raw json.RawMessage) (json.RawMessage, string) {
	if len(raw) == 0 {
		return json.RawMessage("{}"), ""
	}
	if problem := jsonKindProblem(raw, "object"); problem != "" {
		return nil, "scope " + problem
	}
	scope, err := hub.ParseScope(raw)
	if err != nil {
		return nil, "scope 不是合法的 JSON"
	}
	if scope.MetaEvents != "" {
		switch scope.MetaEvents {
		case "synthetic", "passthrough", "drop":
		default:
			return nil, "scope.meta_events 只能是 synthetic | passthrough | drop"
		}
	}
	return raw, ""
}

func (s *Server) handleCreateBinding(w http.ResponseWriter, r *http.Request) {
	var req bindingRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求体不是合法的 JSON")
		return
	}
	ctx := r.Context()
	bot, err := s.opt.Store.BotByID(ctx, req.BotID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bot_id 不存在")
		return
	}
	account, err := s.opt.Store.AccountByID(ctx, req.AccountID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "account_id 不存在")
		return
	}
	scope, problem := validateScope(req.Scope)
	if problem != "" {
		writeError(w, http.StatusBadRequest, problem)
		return
	}

	binding := model.Binding{
		BotID:     bot.ID,
		AccountID: account.ID,
		Priority:  100,
		IsDefault: req.IsDefault != nil && *req.IsDefault,
		Enabled:   req.Enabled == nil || *req.Enabled,
		Scope:     scope,
	}
	if req.Priority != nil {
		binding.Priority = *req.Priority
	}

	created, err := s.opt.Store.CreateBinding(ctx, binding)
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "该 Bot 已绑定此账号")
			return
		}
		s.fail(w, "创建绑定失败", err)
		return
	}
	if created.IsDefault {
		// Promote through UpdateBinding so the previous default is cleared.
		created.Priority = binding.Priority
		created.Scope = scope
		if err := s.opt.Store.UpdateBinding(ctx, created); err != nil {
			s.fail(w, "设置默认账号失败", err)
			return
		}
	}
	s.audit(r, s.actor(r), "binding.create", bot.Name+" -> "+account.SelfID, "")
	s.opt.Hub.RefreshBindings(ctx)

	views, _ := s.bindingViews(r)
	writeJSON(w, http.StatusCreated, map[string]any{"bindings": views})
}

func (s *Server) handleUpdateBinding(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	binding, err := s.opt.Store.BindingByID(ctx, id)
	if err != nil {
		s.notFoundOrFail(w, "绑定不存在", err)
		return
	}

	var req bindingRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求体不是合法的 JSON")
		return
	}
	if req.Priority != nil {
		binding.Priority = *req.Priority
	}
	if req.IsDefault != nil {
		binding.IsDefault = *req.IsDefault
	}
	if req.Enabled != nil {
		binding.Enabled = *req.Enabled
	}
	if len(req.Scope) > 0 {
		scope, problem := validateScope(req.Scope)
		if problem != "" {
			writeError(w, http.StatusBadRequest, problem)
			return
		}
		binding.Scope = scope
	}

	if err := s.opt.Store.UpdateBinding(ctx, binding); err != nil {
		s.fail(w, "更新绑定失败", err)
		return
	}
	s.audit(r, s.actor(r), "binding.update", bindingKey(binding), "")
	s.opt.Hub.RefreshBindings(ctx)

	views, _ := s.bindingViews(r)
	writeJSON(w, http.StatusOK, map[string]any{"bindings": views})
}

func (s *Server) handleDeleteBinding(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	binding, err := s.opt.Store.BindingByID(ctx, id)
	if err != nil {
		s.notFoundOrFail(w, "绑定不存在", err)
		return
	}
	if err := s.opt.Store.DeleteBinding(ctx, id); err != nil {
		s.fail(w, "删除绑定失败", err)
		return
	}
	s.audit(r, s.actor(r), "binding.delete", bindingKey(binding), "")
	s.opt.Hub.RefreshBindings(ctx)

	views, _ := s.bindingViews(r)
	writeJSON(w, http.StatusOK, map[string]any{"bindings": views})
}

func bindingKey(b model.Binding) string {
	return "bot:" + strconv.FormatInt(b.BotID, 10) + " account:" + strconv.FormatInt(b.AccountID, 10)
}
