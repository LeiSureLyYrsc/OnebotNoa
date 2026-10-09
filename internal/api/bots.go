package api

import (
	"net/http"
	"strconv"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/auth"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/model"
)

func (s *Server) botTokenKey(id int64) string { return "bot:" + strconv.FormatInt(id, 10) }

func (s *Server) ensureBotToken(bot model.Bot) (string, error) {
	if tok, ok := s.vault.Get(s.botTokenKey(bot.ID)); ok {
		if bot.TokenHash == auth.HashToken(tok) {
			return tok, nil
		}
	}
	tok, hash, err := auth.NewToken()
	if err != nil {
		return "", err
	}
	if err := s.st.SetBotTokenHash(bot.ID, hash); err != nil {
		return "", err
	}
	if err := s.vault.Set(s.botTokenKey(bot.ID), tok); err != nil {
		return "", err
	}
	return tok, nil
}

func (s *Server) handleBotsList(w http.ResponseWriter, r *http.Request) {
	bots, err := s.st.ListBots()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]map[string]any, 0, len(bots))
	for _, b := range bots {
		out = append(out, map[string]any{
			"id": b.ID, "name": b.Name, "enabled": b.Enabled, "note": b.Note,
			"created_at": b.CreatedAt, "online": s.hub.BotOnlineCount(b.ID),
			"rate_limit": b.RateLimit, "action_policy": b.ActionPolicy,
			"accounts": s.hub.BoundAccountIDs(b.ID),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"bots": out})
}

func (s *Server) handleBotGet(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	bot, err := s.st.GetBot(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "bot not found")
		return
	}
	bindings, _ := s.st.ListBindingsByBot(id)
	writeJSON(w, http.StatusOK, map[string]any{"bot": bot, "online": s.hub.BotOnlineCount(id), "bindings": bindings})
}

func (s *Server) handleBotCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
		Note string `json:"note"`
	}
	if err := readJSON(r, &req); err != nil || req.Name == "" {
		writeError(w, http.StatusBadRequest, "name required")
		return
	}
	bot, err := s.st.CreateBot(req.Name, "")
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	token, err := s.ensureBotToken(bot)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = s.hub.Reload()
	s.audit(r, "bot.create", bot.Name, "")
	writeJSON(w, http.StatusOK, map[string]any{"bot": bot, "token": token})
}

func (s *Server) handleBotUpdate(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	bot, err := s.st.GetBot(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "bot not found")
		return
	}
	var req struct {
		Name         *string `json:"name"`
		Enabled      *bool   `json:"enabled"`
		Note         *string `json:"note"`
		RateLimit    *string `json:"rate_limit"`
		ActionPolicy *string `json:"action_policy"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	name, enabled, note := bot.Name, bot.Enabled, bot.Note
	rateLimit, actionPolicy := bot.RateLimit, bot.ActionPolicy
	if req.Name != nil {
		name = *req.Name
	}
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	if req.Note != nil {
		note = *req.Note
	}
	if req.RateLimit != nil {
		rateLimit = *req.RateLimit
	}
	if req.ActionPolicy != nil {
		actionPolicy = *req.ActionPolicy
	}
	if err := s.st.UpdateBot(id, name, enabled, note, rateLimit, actionPolicy); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = s.hub.Reload()
	s.audit(r, "bot.update", name, "")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleBotDelete(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	bot, err := s.st.GetBot(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "bot not found")
		return
	}
	if err := s.st.DeleteBot(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = s.vault.Delete(s.botTokenKey(id))
	_ = s.hub.Reload()
	s.audit(r, "bot.delete", bot.Name, "")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleBotRotate(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	bot, err := s.st.GetBot(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "bot not found")
		return
	}
	tok, hash, err := auth.NewToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.st.SetBotTokenHash(id, hash); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.vault.Set(s.botTokenKey(id), tok); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "bot.rotate", bot.Name, "")
	writeJSON(w, http.StatusOK, map[string]any{"token": tok})
}

func (s *Server) handleBotConnect(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	bot, err := s.st.GetBot(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "bot not found")
		return
	}
	tok, err := s.ensureBotToken(bot)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	base := baseURL(r, s.cfg.Server.TrustProxy)
	down := s.cfg.OneBot.Downstream.Path
	httpPrefix := s.cfg.OneBot.HTTP.Prefix
	writeJSON(w, http.StatusOK, map[string]any{
		"name":         bot.Name,
		"token":        tok,
		"ws_aggregate": base + down + "/" + tok,
		"ws_single":    base + down + "/" + tok + "/{self_id}",
		"http_api":     base + httpPrefix + "/" + tok + "/{action}",
	})
}
