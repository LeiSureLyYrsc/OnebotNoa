package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/auth"
)

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	ip := s.clientIP(r)
	if wait, blocked := s.limiter.blocked(ip); blocked {
		w.Header().Set("Retry-After", fmt.Sprintf("%d", int(wait.Seconds())+1))
		writeError(w, http.StatusTooManyRequests,
			fmt.Sprintf("登录尝试过于频繁，请在 %d 秒后重试", int(wait.Seconds())+1))
		return
	}

	var req loginRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求体不是合法的 JSON")
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if req.Username == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "用户名和密码不能为空")
		return
	}

	sess, err := s.opt.Auth.Login(r.Context(), req.Username, req.Password, ip, r.UserAgent())
	if err != nil {
		if errors.Is(err, auth.ErrInvalidCredentials) {
			s.limiter.record(ip)
			s.audit(r, req.Username, "auth.login.failed", req.Username, "invalid credentials")
			writeError(w, http.StatusUnauthorized, "用户名或密码错误")
			return
		}
		s.logger.Error("login failed", "user", req.Username, "error", err)
		writeError(w, http.StatusInternalServerError, "登录失败，请查看服务端日志")
		return
	}

	s.limiter.clear(ip)
	s.setSessionCookie(w, r, sess.Token, sess.Session.ExpiresAt)
	s.audit(r, sess.User.Username, "auth.login", sess.User.Username, "")
	writeJSON(w, http.StatusOK, map[string]any{
		"user":       sess.User,
		"csrf_token": sess.Session.CSRFToken,
	})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	sess, ok := sessionFrom(r.Context())
	if ok {
		if err := s.opt.Auth.Logout(r.Context(), sess.Token); err != nil {
			s.logger.Warn("logout failed", "error", err)
		}
		s.audit(r, sess.User.Username, "auth.logout", sess.User.Username, "")
	}
	s.clearSessionCookie(w, r)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	sess, ok := sessionFrom(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "未登录")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user":       sess.User,
		"csrf_token": sess.Session.CSRFToken,
		"expires_at": sess.Session.ExpiresAt,
	})
}
