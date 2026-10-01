package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/hub"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/onebot"
)

// consoleRequest is the API debugger payload: it mirrors a Bot frame.
type consoleRequest struct {
	SelfID  string          `json:"self_id"`
	Action  string          `json:"action"`
	Params  json.RawMessage `json:"params"`
	Echo    json.RawMessage `json:"echo"`
	BotID   int64           `json:"bot_id"`
	BotName string          `json:"bot_name"`
	// Wait for the upstream reply (default) or return as soon as it is sent.
	Async bool `json:"async"`
}

// consoleResult is what the debugger shows.
type consoleResult struct {
	Sent      bool            `json:"sent"`
	SelfID    string          `json:"self_id"`
	Action    string          `json:"action"`
	Request   json.RawMessage `json:"request"`
	Response  json.RawMessage `json:"response,omitempty"`
	ElapsedMS int64           `json:"elapsed_ms"`
	Error     string          `json:"error,omitempty"`
}

// handleConsoleInvoke drives one action through the relay exactly like a Bot
// would, then waits for the upstream answer. It is the "does my Bot's call
// actually work" button.
func (s *Server) handleConsoleInvoke(w http.ResponseWriter, r *http.Request) {
	if s.opt.Hub == nil {
		writeError(w, http.StatusServiceUnavailable, "中继尚未就绪")
		return
	}
	var req consoleRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求体不是合法的 JSON")
		return
	}
	req.Action = strings.TrimSpace(req.Action)
	req.SelfID = strings.TrimSpace(req.SelfID)
	if req.Action == "" {
		writeError(w, http.StatusBadRequest, "action 不能为空")
		return
	}
	if problem := jsonKindProblem(req.Params, "object"); problem != "" {
		writeError(w, http.StatusBadRequest, "params "+problem)
		return
	}
	if !json.Valid(req.Echo) && len(req.Echo) > 0 {
		writeError(w, http.StatusBadRequest, "echo 不是合法的 JSON")
		return
	}

	ctx := r.Context()
	if req.SelfID == "" {
		// Fall back to the only account the selected Bot may use.
		resolved, problem := s.resolveConsoleAccount(ctx, req.BotID, req.BotName)
		if problem != "" {
			writeError(w, http.StatusBadRequest, problem)
			return
		}
		req.SelfID = resolved
	}
	if session, ok := s.opt.Hub.Registry().Session(req.SelfID); !ok || !session.CanSendActions() {
		state := "offline"
		if ok {
			state = session.State()
		}
		writeError(w, http.StatusConflict, "账号 "+req.SelfID+" 当前不可用（"+state+"）")
		return
	}

	echo := req.Echo
	if len(echo) == 0 {
		echo = json.RawMessage(`"console-` + strconv.FormatInt(time.Now().UnixNano(), 10) + `"`)
	}
	params := req.Params
	if len(params) == 0 {
		params = json.RawMessage("{}")
	}

	// Rebuild the frame the way a Bot would send it.
	frame := map[string]json.RawMessage{
		"action": json.RawMessage(strconv.Quote(req.Action)),
		"params": params,
		"echo":   echo,
	}
	raw, err := json.Marshal(frame)
	if err != nil {
		s.fail(w, "构造动作帧失败", err)
		return
	}

	waitCtx, cancel := context.WithTimeout(ctx, s.consoleTimeout())
	defer cancel()

	started := time.Now()
	reply, err := s.opt.Hub.Invoke(waitCtx, req.SelfID, raw, echo, req.Async)
	elapsed := time.Since(started).Milliseconds()

	result := consoleResult{
		Sent:      true,
		SelfID:    req.SelfID,
		Action:    req.Action,
		Request:   raw,
		ElapsedMS: elapsed,
	}
	if err != nil {
		result.Error = consoleError(err)
		result.Sent = !errors.Is(err, hub.ErrNotSent)
	} else if len(reply) > 0 {
		result.Response = reply
	}

	s.audit(r, s.actor(r), "console.invoke", req.SelfID,
		req.Action+" -> "+strconv.FormatInt(elapsed, 10)+"ms")
	writeJSON(w, http.StatusOK, result)
}

// resolveConsoleAccount picks the account when the caller did not name one.
func (s *Server) resolveConsoleAccount(ctx context.Context, botID int64, botName string) (string, string) {
	if botID > 0 && botName == "" {
		bot, ok := s.opt.Connections.BotByID(botID)
		if !ok {
			return "", "bot_id 不存在"
		}
		botName = bot.Name
	}
	if botName != "" {
		enabled := []string{}
		for _, binding := range s.opt.Connections.BindingsByBot(botName) {
			if !binding.Enabled {
				continue
			}
			if _, ok := s.opt.Connections.AccountBySelfID(binding.AccountSelfID); ok {
				enabled = append(enabled, binding.AccountSelfID)
			}
		}
		if len(enabled) == 1 {
			return enabled[0], ""
		}
		if len(enabled) == 0 {
			return "", "该 Bot 没有启用中的绑定账号"
		}
		return "", "该 Bot 绑定了多个账号，请显式指定 self_id"
	}
	accounts := s.opt.Connections.ListAccounts()
	if len(accounts) == 1 {
		return accounts[0].SelfID, ""
	}
	return "", "请指定 self_id（当前有多个账号）"
}

// consoleTimeout bounds how long the debugger waits for the upstream answer.
func (s *Server) consoleTimeout() time.Duration {
	if s.opt.Config == nil {
		return 20 * time.Second
	}
	timeout := s.opt.Config.Policy.ActionTimeout.Std() + 5*time.Second
	if timeout < 10*time.Second {
		timeout = 10 * time.Second
	}
	return timeout
}

// consoleError turns a routing error into a readable message.
func consoleError(err error) string {
	switch {
	case errors.Is(err, hub.ErrNoTarget):
		return "无法确定目标账号"
	case errors.Is(err, hub.ErrNotSent):
		return "动作未能发出（上游离线或队列已满）"
	case errors.Is(err, hub.ErrNoReply):
		return "上游未在超时时间内响应"
	case errors.Is(err, context.DeadlineExceeded):
		return "等待上游响应超时"
	default:
		return err.Error()
	}
}

var _ = onebot.BaseAction
