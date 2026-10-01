package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/auth"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/connect"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/hub"
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

// accountView is the API shape of a QQ instance: the connect.json row plus live
// state and the connection detail an operator needs to wire a client up.
type accountView struct {
	connect.Account
	LiveState    string     `json:"live_state"`
	Peers        []peerView `json:"peers"`
	BindingCount int        `json:"binding_count"`
	HasToken     bool       `json:"has_token"`
	Token        string     `json:"token,omitempty"`
	Endpoints    []string   `json:"endpoints"`

	// ClientURL is the ready-to-paste address of this instance, so an operator
	// never has to assemble host + path + token by hand.
	ClientURL string `json:"client_url,omitempty"`
}

func (s *Server) accountView(account connect.Account, bindingCount int, showToken bool) accountView {
	view := accountView{
		Account:      account,
		LiveState:    "offline",
		Peers:        []peerView{},
		BindingCount: bindingCount,
		HasToken:     account.Grant.Token != "",
		Endpoints:    s.accountEndpoints(account.SelfID),
	}
	if session, ok := s.opt.Hub.Registry().Session(account.SelfID); ok {
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
	if showToken {
		if token, err := s.opt.Connections.AccountToken(account.ID); err == nil {
			view.Token = token
			if len(view.Endpoints) > 0 {
				view.ClientURL = view.Endpoints[0] + "/" + token
			}
		}
	}
	return view
}

// accountEndpoints lists the addresses this account answers on: the shared
// endpoint plus any dedicated listener pinned to it.
func (s *Server) accountEndpoints(selfID string) []string {
	out := []string{}
	if s.opt.Config == nil {
		return out
	}
	base := "ws://" + publicBase(s.opt.Config.Server.Listen)
	if s.opt.Config.OneBot.UpstreamWS.Enable {
		out = append(out, base+s.opt.Config.OneBot.UpstreamWS.Path)
	}
	if s.opt.Connections == nil {
		return out
	}
	for _, connection := range s.opt.Connections.ListConnections() {
		if connection.Kind != connect.KindUpstreamListen || !connection.Enabled {
			continue
		}
		if connection.AccountSelfID != selfID {
			continue
		}
		scheme := "ws"
		if connection.TLSCert != "" && connection.TLSKey != "" {
			scheme = "wss"
		}
		path := connection.Path
		if path == "" {
			path = "/onebot/v11/ws"
		}
		out = append(out, scheme+"://"+displayHost(connection.Addr)+path)
	}
	return out
}

// displayHost turns a bind address into something an operator can dial.
func displayHost(addr string) string {
	if addr == "" {
		return "127.0.0.1"
	}
	switch {
	case strings.HasPrefix(addr, "0.0.0.0:"):
		return "127.0.0.1:" + strings.TrimPrefix(addr, "0.0.0.0:")
	case strings.HasPrefix(addr, "[::]:"):
		return "127.0.0.1:" + strings.TrimPrefix(addr, "[::]:")
	}
	return addr
}

func (s *Server) handleListAccounts(w http.ResponseWriter, r *http.Request) {
	if s.opt.Connections == nil {
		writeError(w, http.StatusServiceUnavailable, "连接配置文件尚未就绪")
		return
	}
	accounts := s.opt.Connections.ListAccounts()
	bindings := s.opt.Connections.ListBindings()
	counts := map[string]int{}
	for _, b := range bindings {
		counts[b.AccountSelfID]++
	}

	views := make([]accountView, 0, len(accounts))
	for _, account := range accounts {
		views = append(views, s.accountView(account, counts[account.SelfID], false))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"accounts": views,
		"pending":  s.pendingViews(),
		"file":     s.connectionsFileInfo(),
	})
}

// connectionsFileInfo tells the WebUI where the generated file lives.
func (s *Server) connectionsFileInfo() map[string]any {
	if s.opt.Connections == nil {
		return map[string]any{}
	}
	info := map[string]any{
		"path": s.opt.Connections.Path(),
		"note": "账号 / Bot / 连接 / 绑定 保存在 connect.json；进程级静态配置在 config.yaml。",
	}
	if store, ok := s.opt.Connections.(interface{ LastError() error }); ok {
		if err := store.LastError(); err != nil {
			info["last_error"] = err.Error()
		}
	}
	return info
}

func (s *Server) handleGetAccount(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r)
	if !ok {
		return
	}
	if s.opt.Connections == nil {
		writeError(w, http.StatusServiceUnavailable, "连接配置文件尚未就绪")
		return
	}
	account, found := s.opt.Connections.AccountByID(id)
	if !found {
		writeError(w, http.StatusNotFound, "账号不存在")
		return
	}
	bindings := s.opt.Connections.BindingsByAccount(account.SelfID)
	// A single-account read may reveal the token: the operator opened this row.
	writeJSON(w, http.StatusOK, map[string]any{
		"account":  s.accountView(account, len(bindings), true),
		"bindings": s.bindingViewsFor(account.SelfID),
	})
}

type accountRequest struct {
	SelfID   string   `json:"self_id"`
	Name     string   `json:"name"`
	Enabled  *bool    `json:"enabled"`
	Tags     []string `json:"tags"`
	Nickname string   `json:"nickname"`
	Note     string   `json:"note"`
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
	account, err := s.opt.Connections.CreateAccount(req.SelfID, strings.TrimSpace(req.Name), "manual")
	if err != nil {
		if errors.Is(err, connect.ErrDuplicateName) {
			writeError(w, http.StatusConflict, "该 self_id 已存在")
			return
		}
		s.fail(w, "创建账号失败", err)
		return
	}
	s.audit(r, s.actor(r), "account.create", account.SelfID, "")
	s.opt.Hub.InvalidateBindings()
	writeJSON(w, http.StatusCreated, map[string]any{"account": s.accountView(account, 0, false)})
}

func (s *Server) handleUpdateAccount(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r)
	if !ok {
		return
	}
	account, found := s.opt.Connections.AccountByID(id)
	if !found {
		writeError(w, http.StatusNotFound, "账号不存在")
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
	if req.Note != "" {
		account.Note = req.Note
	}
	if req.Enabled != nil {
		account.Enabled = *req.Enabled
	}
	if req.Tags != nil {
		account.Tags = req.Tags
	}

	if err := s.opt.Connections.UpdateAccount(account); err != nil {
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
	writeJSON(w, http.StatusOK, map[string]any{"account": s.accountView(account, 0, false)})
}

func (s *Server) handleDeleteAccount(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r)
	if !ok {
		return
	}
	account, found := s.opt.Connections.AccountByID(id)
	if !found {
		writeError(w, http.StatusNotFound, "账号不存在")
		return
	}
	if session, ok := s.opt.Hub.Registry().Session(account.SelfID); ok {
		for _, p := range session.Peers() {
			p.Close(1008, "account deleted by an administrator")
		}
	}
	if err := s.opt.Connections.DeleteAccount(id); err != nil {
		s.fail(w, "删除账号失败", err)
		return
	}
	s.audit(r, s.actor(r), "account.delete", account.SelfID, "")
	s.opt.Hub.RefreshBindings(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"deleted": account.SelfID})
}

// handleRotateAccountToken issues a per-instance token.
//
// Unlike a Bot token this one is not hash-only: connect.json keeps it sealed so
// the WebUI can show it again and the hub can dial with it.
func (s *Server) handleRotateAccountToken(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r)
	if !ok {
		return
	}
	account, found := s.opt.Connections.AccountByID(id)
	if !found {
		writeError(w, http.StatusNotFound, "账号不存在")
		return
	}
	plain, hash, err := auth.NewToken()
	if err != nil {
		s.fail(w, "生成 token 失败", err)
		return
	}
	if err := s.opt.Connections.SetAccountToken(account.ID, plain, hash, tokenHint(plain)); err != nil {
		s.fail(w, "保存 token 失败", err)
		return
	}
	s.audit(r, s.actor(r), "account.token.rotate", account.SelfID, "")
	account.Grant = connect.Grant{TokenHash: hash}
	writeJSON(w, http.StatusOK, map[string]any{
		"account": s.accountView(account, 0, false),
		"token":   plain,
		"hint":    "token 同时写入 connect.json（本地密钥加密）；随时可在本页重新查看。用于该 QQ 实例连接 /onebot/v11/ws",
	})
}

func (s *Server) handleClearAccountToken(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r)
	if !ok {
		return
	}
	account, found := s.opt.Connections.AccountByID(id)
	if !found {
		writeError(w, http.StatusNotFound, "账号不存在")
		return
	}
	if err := s.opt.Connections.SetAccountToken(account.ID, "", "", ""); err != nil {
		s.fail(w, "清除 token 失败", err)
		return
	}
	s.audit(r, s.actor(r), "account.token.clear", account.SelfID, "")
	account.Grant = connect.Grant{}
	writeJSON(w, http.StatusOK, map[string]any{"account": s.accountView(account, 0, false)})
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
		writeError(w, http.StatusNotFound, "待接入项不存在或已处理")
		return
	}
	s.audit(r, s.actor(r), "account.approve", account.SelfID, req.ID)
	view := accountView{
		Account:   connect.Account{SelfID: account.SelfID, Name: account.Name, Enabled: account.Enabled},
		LiveState: account.Status,
		Peers:     []peerView{},
		Endpoints: s.accountEndpoints(account.SelfID),
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"account": view,
		"hint":    "已在 connect.json 中创建账号；客户端会在下次重连时接入",
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
	if errors.Is(err, connect.ErrNotFound) {
		writeError(w, http.StatusNotFound, message)
		return
	}
	s.fail(w, message, err)
}

// isUniqueViolation detects a duplicate name so the API can answer 409.
func isUniqueViolation(err error) bool {
	return err != nil && (errors.Is(err, connect.ErrDuplicateName) ||
		strings.Contains(strings.ToUpper(err.Error()), "UNIQUE CONSTRAINT"))
}

// tokenHint is the non-secret reminder shown next to a token.
func tokenHint(token string) string {
	if len(token) <= 8 {
		return "…"
	}
	return token[:4] + "…" + token[len(token)-4:]
}

var _ = hub.Account{}
var _ = json.Valid
