package transport

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/hub"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/onebot"
)

// RegisterHTTP mounts the OneBot HTTP compatibility layer.
//
// It exists for deployments that cannot use WebSocket:
//
//	POST /onebot/v11/report                  QQ side pushes events (X-Self-ID)
//	POST /onebot/v11/http/{bot-token}        Bot calls the API
//
// The HTTP API deliberately reuses the WebSocket semantics (target resolution,
// policy, rate limits, echo rewriting) so a Bot cannot tell the two apart — with
// one documented exception: there is no request/response correlation to restore,
// because HTTP already carries the reply in its own response.
func (d *DataPlane) RegisterHTTP(mux *http.ServeMux) {
	cfg := d.cfg.OneBot.HTTP
	if !cfg.Enable {
		return
	}
	apiPath := strings.TrimRight(cfg.APIPath, "/")
	mux.HandleFunc("POST "+apiPath+"/{token}", d.handleHTTPAPI)
	mux.HandleFunc("POST "+cfg.ReportPath, d.handleHTTPReport)
	if cfg.QuickOperation {
		// Opt-in: quick-operation means "the implementation asks the Bot what to
		// do with a pending request". With several Bots behind one account the
		// answer is ambiguous, so it is off by default.
		mux.HandleFunc("POST "+apiPath+"/{token}/{self_id}/quick-operation", d.handleQuickOperation)
	}
}

// handleHTTPAPI serves one Bot's API call over HTTP.
func (d *DataPlane) handleHTTPAPI(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(r.PathValue("token"))
	if token == "" {
		http.Error(w, "missing token", http.StatusUnauthorized)
		return
	}
	bot, found := d.conns.BotByToken(token)
	if !found {
		http.Error(w, "unknown token", http.StatusUnauthorized)
		return
	}
	if !bot.Enabled {
		http.Error(w, "bot disabled", http.StatusForbidden)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, d.cfg.OneBot.HTTP.MaxBodyBytes))
	if err != nil {
		http.Error(w, "cannot read body", http.StatusBadRequest)
		return
	}
	defer func() { _ = r.Body.Close() }()

	var frame onebot.ActionFrame
	if err := json.Unmarshal(body, &frame); err != nil || frame.Action == "" {
		writeOneBotFailure(w, nil, onebot.RetBadRequest, "invalid action frame")
		return
	}
	if frame.Echo == nil {
		frame.Echo = json.RawMessage("null")
	}

	selfID, err := d.resolveHTTPTarget(r, bot, frame)
	if err != nil {
		writeOneBotFailure(w, frame.Echo, onebot.RetNotFound, err.Error())
		return
	}

	// A Bot must not be able to reach an account it is not bound to, even over
	// HTTP: mirror the WebSocket rule.
	if !d.botMayUseAccount(bot, selfID) {
		writeOneBotFailure(w, frame.Echo, onebot.RetForbidden, "账号 "+selfID+" 未授权给当前 Bot")
		return
	}

	// Rebuild the frame without the relay-only routing hint.
	envelope := map[string]json.RawMessage{"action": json.RawMessage(mustQuote(frame.Action))}
	if len(frame.Params) > 0 {
		envelope["params"] = frame.Params
	}
	raw, marshalErr := json.Marshal(envelope)
	if marshalErr != nil {
		writeOneBotFailure(w, frame.Echo, onebot.RetBadRequest, "invalid action frame")
		return
	}

	reply, invokeErr := d.hub.Invoke(r.Context(), selfID, raw, frame.Echo, false)
	if invokeErr != nil {
		writeOneBotFailure(w, frame.Echo, onebot.RetImplError, consoleFailureText(invokeErr))
		return
	}
	if len(reply) == 0 {
		writeOneBotFailure(w, frame.Echo, onebot.RetImplError, "上游没有返回响应")
		return
	}

	// Restore the Bot's own echo: the relay rewrites it internally, and an HTTP
	// caller expects its own value back verbatim.
	out := reply
	if len(frame.Echo) > 0 {
		if patched, err := replaceEchoValue(reply, frame.Echo); err == nil {
			out = patched
		}
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out)
}

// handleHTTPReport accepts an event pushed by a QQ-side implementation over
// HTTP (the "HTTP POST 上报" transport in the OneBot spec).
func (d *DataPlane) handleHTTPReport(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, d.cfg.OneBot.HTTP.MaxBodyBytes))
	if err != nil {
		http.Error(w, "cannot read body", http.StatusBadRequest)
		return
	}
	defer func() { _ = r.Body.Close() }()

	selfID := strings.TrimSpace(r.Header.Get("X-Self-ID"))
	if selfID == "" {
		// Fall back to the first frame's self_id, exactly like the WebSocket
		// first-frame fallback.
		selfID = onebot.ExtractSelfID(body)
	}
	if selfID == "" {
		http.Error(w, "missing X-Self-ID", http.StatusBadRequest)
		return
	}

	// Authentication: the same tokens the WebSocket side accepts.
	token, _ := d.presentedToken(r, "")
	if d.cfg.OneBot.UpstreamWS.RequireToken {
		bound := ""
		if token != "" {
			if account, found := d.conns.AccountByToken(token); found {
				bound = account.SelfID
			}
		}
		if !d.authorizedUpstream(token, bound) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if bound != "" && bound != selfID {
			http.Error(w, "token does not belong to that self_id", http.StatusForbidden)
			return
		}
	}

	if !json.Valid(body) {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if err := d.hub.IngestEvent(r.Context(), selfID, body); err != nil {
		d.logger.Warn("http report could not be routed", "self_id", selfID, "error", err)
		http.Error(w, "cannot route event", http.StatusInternalServerError)
		return
	}

	// The OneBot spec treats an empty body with 204 as success.
	w.WriteHeader(http.StatusNoContent)
}

// handleQuickOperation answers quick-operation with a clear 404.
//
// The relay would have to decide which of several Bots answers for one account,
// so the feature is intentionally unimplemented rather than subtly wrong.
func (d *DataPlane) handleQuickOperation(w http.ResponseWriter, r *http.Request) {
	writeOneBotFailure(w, nil, onebot.RetNotFound,
		"quick-operation 在多 Bot 场景下语义不明确，中继未实现；请改用 API 调试台或直接在 Bot 内处理")
}

// resolveHTTPTarget applies the same precedence as the WebSocket path.
func (d *DataPlane) resolveHTTPTarget(r *http.Request, bot BotRef, frame onebot.ActionFrame) (string, error) {
	if selfID := frame.SelfID.String(); selfID != "" {
		return selfID, nil
	}
	var params struct {
		SelfID onebot.ID `json:"self_id"`
	}
	if len(frame.Params) > 0 {
		_ = json.Unmarshal(frame.Params, &params)
	}
	if selfID := params.SelfID.String(); selfID != "" {
		return selfID, nil
	}

	bindings := d.conns.BindingsByBot(bot.Name)
	enabled := []BindingRef{}
	for _, binding := range bindings {
		if binding.Enabled {
			enabled = append(enabled, binding)
		}
	}
	if len(enabled) == 1 {
		if _, ok := d.conns.AccountBySelfID(enabled[0].AccountSelfID); !ok {
			return "", errResource("绑定账号不存在")
		}
		return enabled[0].AccountSelfID, nil
	}
	for _, binding := range enabled {
		if binding.IsDefault {
			if _, ok := d.conns.AccountBySelfID(binding.AccountSelfID); ok {
				return binding.AccountSelfID, nil
			}
		}
	}
	return "", errResource("无法确定目标账号：请在请求中指定 self_id")
}

// botMayUseAccount reports whether a Bot is bound to an account.
func (d *DataPlane) botMayUseAccount(bot BotRef, selfID string) bool {
	if _, ok := d.conns.AccountBySelfID(selfID); !ok {
		return false
	}
	binding, ok := d.conns.BindingByPair(bot.Name, selfID)
	return ok && binding.Enabled
}

type errResource string

func (e errResource) Error() string { return string(e) }

func writeOneBotFailure(w http.ResponseWriter, echo json.RawMessage, retcode int, wording string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(onebot.FailureResponse(echo, retcode, wording))
}

func mustQuote(value string) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return `""`
	}
	return string(encoded)
}

// replaceEchoValue swaps the relay's rewritten echo back to the caller's value.
func replaceEchoValue(raw []byte, echo json.RawMessage) ([]byte, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	if len(echo) > 0 {
		fields["echo"] = echo
	}
	return json.Marshal(fields)
}

// consoleFailureText turns a routing error into a OneBot wording field.
func consoleFailureText(err error) string {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, hub.ErrNoTarget):
		return "账号当前不可用（离线或缺少 API 连接）"
	case errors.Is(err, hub.ErrNotSent):
		return "动作未能发出（上游繁忙或已离线）"
	case errors.Is(err, hub.ErrNoReply):
		return "上游未在超时时间内响应"
	default:
		return err.Error()
	}
}

var _ = hub.ErrNoReply
