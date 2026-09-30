package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/auth"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/model"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/store"
)

// peerView describes one live connection of an account.
type peerView struct {
	ID         string `json:"id"`
	Role       string `json:"role"`
	RemoteAddr string `json:"remote_addr"`
	UserAgent  string `json:"user_agent,omitempty"`
	Queued     int64  `json:"queued"`
	Dropped    int64  `json:"dropped"`
}

// accountView is the API shape of a QQ instance: the stored row plus live state.
type accountView struct {
	model.Account
	LiveState    string     `json:"live_state"`
	Peers        []peerView `json:"peers"`
	BindingCount int        `json:"binding_count"`
}

func (s *Server) accountView(a model.Account, bindingCount int) accountView {
	view := accountView{Account: a, LiveState: a.Status, Peers: []peerView{}, BindingCount: bindingCount}
	if session, ok := s.opt.Hub.Registry().Session(a.SelfID); ok {
		view.LiveState = session.State()
		for _, p := range session.Peers() {
			view.Peers = append(view.Peers, peerView{
				ID:         p.ID(),
				Role:       string(p.Role()),
				RemoteAddr: p.RemoteAddr(),
				UserAgent:  p.UserAgent(),
				Queued:     p.Queued(),
				Dropped:    p.Dropped(),
			})
		}
	}
	return view
}

func (s *Server) handleListAccounts(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	accounts, err := s.opt.Store.ListAccounts(ctx)
	if err != nil {
		s.fail(w, "读取账号列表失败", err)
		return
	}
	bindings, err := s.opt.Store.ListBindings(ctx)
	if err != nil {
		s.fail(w, "读取绑定关系失败", err)
		return
	}
	counts := map[int64]int{}
	for _, b := range bindings {
		counts[b.AccountID]++
	}

	views := make([]accountView, 0, len(accounts))
	for _, a := range accounts {
		views = append(views, s.accountView(a, counts[a.ID]))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"accounts": views,
		"pending":  s.pendingViews(),
	})
}

func (s *Server) handleGetAccount(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r)
	if !ok {
		return
	}
	account, err := s.opt.Store.AccountByID(r.Context(), id)
	if err != nil {
		s.notFoundOrFail(w, "账号不存在", err)
		return
	}
	bindings, _ := s.opt.Store.BindingsByAccount(r.Context(), account.ID)
	writeJSON(w, http.StatusOK, map[string]any{
		"account":  s.accountView(account, len(bindings)),
		"bindings": bindings,
	})
}

type accountRequest struct {
	SelfID   string          `json:"self_id"`
	Name     string          `json:"name"`
	Enabled  *bool           `json:"enabled"`
	Tags     json.RawMessage `json:"tags"`
	Nickname string          `json:"nickname"`
}

func (s *Server) handleCreateAccount(w http.ResponseWriter, r *http.Request) {
	var req accountRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求体不是合法的 JSON")
		return
	}
	req.SelfID = strings.TrimSpace(req.SelfID)
	if req.SelfID == "" {
		writeError(w, http.StatusBadRequest, "self_id 不能为空")
		return
	}
	account, err := s.opt.Store.CreateAccount(r.Context(), req.SelfID, strings.TrimSpace(req.Name), "manual")
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "该 self_id 已存在")
			return
		}
		s.fail(w, "创建账号失败", err)
		return
	}
	s.audit(r, s.actor(r), "account.create", account.SelfID, "")
	s.opt.Hub.InvalidateBindings()
	writeJSON(w, http.StatusCreated, map[string]any{"account": s.accountView(account, 0)})
}

func (s *Server) handleUpdateAccount(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r)
	if !ok {
		return
	}
	account, err := s.opt.Store.AccountByID(r.Context(), id)
	if err != nil {
		s.notFoundOrFail(w, "账号不存在", err)
		return
	}

	var req accountRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求体不是合法的 JSON")
		return
	}
	if name := strings.TrimSpace(req.Name); name != "" {
		account.Name = name
	}
	if req.Nickname != "" {
		account.Nickname = req.Nickname
	}
	if req.Enabled != nil {
		account.Enabled = *req.Enabled
	}
	if problem := jsonKindProblem(req.Tags, "array"); problem != "" {
		writeError(w, http.StatusBadRequest, "tags "+problem)
		return
	}
	if len(req.Tags) > 0 {
		account.Tags = req.Tags
	}

	if err := s.opt.Store.UpdateAccountProfile(r.Context(), account); err != nil {
		s.fail(w, "更新账号失败", err)
		return
	}
	s.audit(r, s.actor(r), "account.update", account.SelfID, "")

	if !account.Enabled {
		// Disabling must take effect at once: drop live connections.
		if session, ok := s.opt.Hub.Registry().Session(account.SelfID); ok {
			for _, p := range session.Peers() {
				p.Close(1008, "account disabled by an administrator")
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"account": s.accountView(account, 0)})
}

func (s *Server) handleDeleteAccount(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r)
	if !ok {
		return
	}
	account, err := s.opt.Store.AccountByID(r.Context(), id)
	if err != nil {
		s.notFoundOrFail(w, "账号不存在", err)
		return
	}
	if session, ok := s.opt.Hub.Registry().Session(account.SelfID); ok {
		for _, p := range session.Peers() {
			p.Close(1008, "account deleted by an administrator")
		}
	}
	if err := s.opt.Store.DeleteAccount(r.Context(), id); err != nil {
		s.fail(w, "删除账号失败", err)
		return
	}
	s.audit(r, s.actor(r), "account.delete", account.SelfID, "")
	s.opt.Hub.RefreshBindings(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"deleted": account.SelfID})
}

// handleRotateAccountToken issues a per-instance token. The plaintext is
// returned exactly once; only its hash is stored.
func (s *Server) handleRotateAccountToken(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r)
	if !ok {
		return
	}
	account, err := s.opt.Store.AccountByID(r.Context(), id)
	if err != nil {
		s.notFoundOrFail(w, "账号不存在", err)
		return
	}
	plain, hash, err := auth.NewToken()
	if err != nil {
		s.fail(w, "生成 token 失败", err)
		return
	}
	if err := s.opt.Store.SetAccountToken(r.Context(), account.ID, &hash); err != nil {
		s.fail(w, "保存 token 失败", err)
		return
	}
	s.audit(r, s.actor(r), "account.token.rotate", account.SelfID, "")
	account.HasToken = true
	writeJSON(w, http.StatusOK, map[string]any{
		"account": s.accountView(account, 0),
		"token":   plain,
		"hint":    "该 token 只显示一次，请立刻保存；它用于该 QQ 实例连接 /onebot/v11/ws",
	})
}

func (s *Server) handleClearAccountToken(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r)
	if !ok {
		return
	}
	account, err := s.opt.Store.AccountByID(r.Context(), id)
	if err != nil {
		s.notFoundOrFail(w, "账号不存在", err)
		return
	}
	if err := s.opt.Store.SetAccountToken(r.Context(), account.ID, nil); err != nil {
		s.fail(w, "清除 token 失败", err)
		return
	}
	s.audit(r, s.actor(r), "account.token.clear", account.SelfID, "")
	account.HasToken = false
	writeJSON(w, http.StatusOK, map[string]any{"account": s.accountView(account, 0)})
}

// --------------------------------------------------------- pending approvals

// pendingView is a connection refused because its account is not approved yet.
type pendingView struct {
	ID         string `json:"id"`
	SelfID     string `json:"self_id"`
	Role       string `json:"role"`
	RemoteAddr string `json:"remote_addr"`
	Source     string `json:"source,omitempty"`
	FirstSeen  string `json:"first_seen"`
	LastSeen   string `json:"last_seen"`
	Attempts   int    `json:"attempts"`
}

func (s *Server) pendingViews() []pendingView {
	pending := s.opt.Hub.Registry().Pending()
	out := make([]pendingView, 0, len(pending))
	for _, p := range pending {
		out = append(out, pendingView{
			ID:         p.ID,
			SelfID:     p.SelfID,
			Role:       string(p.Role),
			RemoteAddr: p.RemoteAddr,
			Source:     p.Source,
			FirstSeen:  p.FirstSeen.Format("2006-01-02 15:04:05"),
			LastSeen:   p.LastSeen.Format("2006-01-02 15:04:05"),
			Attempts:   p.Attempts,
		})
	}
	return out
}

func (s *Server) handleListPending(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"pending": s.pendingViews()})
}

type pendingRequest struct {
	ID string `json:"id"`
}

func (s *Server) handleApprovePending(w http.ResponseWriter, r *http.Request) {
	var req pendingRequest
	if err := decodeJSON(r, &req); err != nil || strings.TrimSpace(req.ID) == "" {
		writeError(w, http.StatusBadRequest, "需要提供待接入项的 id")
		return
	}
	account, err := s.opt.Hub.Registry().ApprovePending(r.Context(), req.ID)
	if err != nil {
		s.notFoundOrFail(w, "待接入项不存在或已处理", err)
		return
	}
	s.audit(r, s.actor(r), "account.approve", account.SelfID, req.ID)
	writeJSON(w, http.StatusOK, map[string]any{
		"account": s.accountView(account, 0),
		"hint":    "已创建账号；客户端会在下次重连时接入（反向 WS 默认 3 秒重试）",
	})
}

func (s *Server) handleRejectPending(w http.ResponseWriter, r *http.Request) {
	var req pendingRequest
	if err := decodeJSON(r, &req); err != nil || strings.TrimSpace(req.ID) == "" {
		writeError(w, http.StatusBadRequest, "需要提供待接入项的 id")
		return
	}
	if !s.opt.Hub.Registry().RejectPending(req.ID) {
		writeError(w, http.StatusNotFound, "待接入项不存在")
		return
	}
	s.audit(r, s.actor(r), "account.reject", req.ID, "")
	writeJSON(w, http.StatusOK, map[string]any{"rejected": req.ID})
}

// ------------------------------------------------------------------- helpers

func (s *Server) pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	raw := r.PathValue("id")
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "非法的 id："+raw)
		return 0, false
	}
	return id, true
}

func (s *Server) actor(r *http.Request) string {
	if sess, ok := sessionFrom(r.Context()); ok {
		return sess.User.Username
	}
	return "unknown"
}

func (s *Server) fail(w http.ResponseWriter, message string, err error) {
	s.logger.Error(message, "error", err)
	writeError(w, http.StatusInternalServerError, message)
}

func (s *Server) notFoundOrFail(w http.ResponseWriter, message string, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, message)
		return
	}
	s.fail(w, message, err)
}

// isUniqueViolation detects SQLite's UNIQUE constraint error so the API can
// answer 409 instead of 500.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToUpper(err.Error()), "UNIQUE CONSTRAINT")
}
