package api

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/connect"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/hub"
)

// bindingView is a grant plus the names the table needs.
type bindingView struct {
	ID            int64     `json:"id"`
	BotName       string    `json:"bot_name"`
	AccountSelfID string    `json:"account_self_id"`
	AccountName   string    `json:"account_name"`
	Priority      int       `json:"priority"`
	IsDefault     bool      `json:"is_default"`
	Enabled       bool      `json:"enabled"`
	Scope         hub.Scope `json:"scope"`
}

func (s *Server) bindingView(binding connect.Binding) bindingView {
	view := bindingView{
		ID:            binding.ID,
		BotName:       binding.BotName,
		AccountSelfID: binding.AccountSelfID,
		Priority:      binding.Priority,
		IsDefault:     binding.IsDefault,
		Enabled:       binding.Enabled,
		Scope:         scopeFromDocument(binding.Scope),
	}
	if account, ok := s.opt.Connections.AccountBySelfID(binding.AccountSelfID); ok {
		view.AccountName = account.Name
	}
	return view
}

// scopeFromDocument converts the typed scope back into the JSON shape the
// routing path parses, so the API and the hub cannot drift apart.
func scopeFromDocument(scope connect.Scope) hub.Scope {
	encoded, err := json.Marshal(scope)
	if err != nil {
		return hub.Scope{}
	}
	parsed, err := hub.ParseScope(encoded)
	if err != nil {
		return hub.Scope{}
	}
	return parsed
}

func (s *Server) bindingViews(r *http.Request) ([]bindingView, error) {
	rows := s.opt.Connections.ListBindings()
	out := make([]bindingView, 0, len(rows))
	for _, binding := range rows {
		out = append(out, s.bindingView(binding))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].AccountSelfID != out[j].AccountSelfID {
			return out[i].AccountSelfID < out[j].AccountSelfID
		}
		return out[i].BotName < out[j].BotName
	})
	return out, nil
}

func (s *Server) handleListBindings(w http.ResponseWriter, r *http.Request) {
	if s.opt.Connections == nil {
		writeError(w, http.StatusServiceUnavailable, "连接配置文件尚未就绪")
		return
	}
	views, err := s.bindingViews(r)
	if err != nil {
		s.fail(w, "读取绑定关系失败", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"bindings": views})
}

type bindingRequest struct {
	BotID       int64           `json:"bot_id"`
	BotName     string          `json:"bot_name"`
	AccountID   int64           `json:"account_id"`
	AccountSelf string          `json:"account_self_id"`
	Priority    *int            `json:"priority"`
	IsDefault   *bool           `json:"is_default"`
	Enabled     *bool           `json:"enabled"`
	Scope       json.RawMessage `json:"scope"`
}

// resolveBindingRefs accepts either the numeric ids the WebUI has always sent or
// the names the file uses, so old clients keep working after the split.
func (s *Server) resolveBindingRefs(req bindingRequest) (botName, selfID, problem string) {
	botName = strings.TrimSpace(req.BotName)
	if botName == "" && req.BotID > 0 {
		bot, ok := s.opt.Connections.BotByID(req.BotID)
		if !ok {
			return "", "", "bot_id 不存在"
		}
		botName = bot.Name
	}
	if botName == "" {
		return "", "", "需要提供 bot_id 或 bot_name"
	}
	selfID = strings.TrimSpace(req.AccountSelf)
	if selfID == "" && req.AccountID > 0 {
		account, ok := s.opt.Connections.AccountByID(req.AccountID)
		if !ok {
			return "", "", "account_id 不存在"
		}
		selfID = account.SelfID
	}
	if selfID == "" {
		return "", "", "需要提供 account_id 或 account_self_id"
	}
	if _, ok := s.opt.Connections.BotByName(botName); !ok {
		return "", "", "Bot " + botName + " 不存在"
	}
	if _, ok := s.opt.Connections.AccountBySelfID(selfID); !ok {
		return "", "", "账号 " + selfID + " 不存在"
	}
	return botName, selfID, ""
}

// validateScope rejects a malformed filter so the routing path never guesses.
func validateScope(raw json.RawMessage) (connect.Scope, string) {
	if len(raw) == 0 {
		return connect.Scope{}, ""
	}
	if problem := jsonKindProblem(raw, "object"); problem != "" {
		return connect.Scope{}, "scope " + problem
	}
	scope, err := hub.ParseScope(raw)
	if err != nil {
		return connect.Scope{}, "scope 不是合法的 JSON"
	}
	if scope.MetaEvents != "" {
		switch scope.MetaEvents {
		case "synthetic", "passthrough", "drop":
		default:
			return connect.Scope{}, "scope.meta_events 只能是 synthetic | passthrough | drop"
		}
	}
	return toDocumentScope(scope), ""
}

func toDocumentScope(scope hub.Scope) connect.Scope {
	out := connect.Scope{
		PostTypes:     scope.PostTypes,
		IncludeGroups: scope.IncludeGroups,
		ExcludeGroups: scope.ExcludeGroups,
		IncludeUsers:  scope.IncludeUsers,
		ExcludeUsers:  scope.ExcludeUsers,
		MetaEvents:    scope.MetaEvents,
	}
	if scope.ExcludeSelf {
		value := true
		out.ExcludeSelf = &value
	}
	return out
}

func (s *Server) handleCreateBinding(w http.ResponseWriter, r *http.Request) {
	var req bindingRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求体不是合法的 JSON")
		return
	}
	botName, selfID, problem := s.resolveBindingRefs(req)
	if problem != "" {
		writeError(w, http.StatusBadRequest, problem)
		return
	}
	scope, problem := validateScope(req.Scope)
	if problem != "" {
		writeError(w, http.StatusBadRequest, problem)
		return
	}

	binding := connect.Binding{
		BotName:       botName,
		AccountSelfID: selfID,
		Priority:      100,
		IsDefault:     req.IsDefault != nil && *req.IsDefault,
		Enabled:       req.Enabled == nil || *req.Enabled,
		Scope:         scope,
	}
	if req.Priority != nil {
		binding.Priority = *req.Priority
	}

	if _, err := s.opt.Connections.CreateBinding(binding); err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "该 Bot 已绑定此账号")
			return
		}
		s.fail(w, "创建绑定失败", err)
		return
	}
	s.audit(r, s.actor(r), "binding.create", botName+" -> "+selfID, "")
	s.opt.Hub.RefreshBindings(r.Context())

	views, _ := s.bindingViews(r)
	writeJSON(w, http.StatusCreated, map[string]any{"bindings": views})
}

func (s *Server) handleUpdateBinding(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r)
	if !ok {
		return
	}
	binding, found := s.opt.Connections.BindingByID(id)
	if !found {
		writeError(w, http.StatusNotFound, "绑定不存在")
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

	if err := s.opt.Connections.UpdateBinding(binding); err != nil {
		s.fail(w, "更新绑定失败", err)
		return
	}
	s.audit(r, s.actor(r), "binding.update", binding.BotName+" -> "+binding.AccountSelfID, "")
	s.opt.Hub.RefreshBindings(r.Context())

	views, _ := s.bindingViews(r)
	writeJSON(w, http.StatusOK, map[string]any{"bindings": views})
}

func (s *Server) handleDeleteBinding(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r)
	if !ok {
		return
	}
	binding, found := s.opt.Connections.BindingByID(id)
	if !found {
		writeError(w, http.StatusNotFound, "绑定不存在")
		return
	}
	if err := s.opt.Connections.DeleteBinding(id); err != nil {
		s.fail(w, "删除绑定失败", err)
		return
	}
	s.audit(r, s.actor(r), "binding.delete", binding.BotName+" -> "+binding.AccountSelfID, "")
	s.opt.Hub.RefreshBindings(r.Context())

	views, _ := s.bindingViews(r)
	writeJSON(w, http.StatusOK, map[string]any{"bindings": views})
}
