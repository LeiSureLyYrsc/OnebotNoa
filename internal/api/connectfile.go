package api

import (
	"net/http"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/auth"
)

type exportAccount struct {
	SelfID  string `json:"self_id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	Token   string `json:"token"`
}

type exportBot struct {
	Name         string `json:"name"`
	Enabled      bool   `json:"enabled"`
	Note         string `json:"note"`
	RateLimit    string `json:"rate_limit"`
	ActionPolicy string `json:"action_policy"`
	Token        string `json:"token"`
}

type exportBinding struct {
	Bot       string `json:"bot"`
	Account   string `json:"account"`
	Priority  int    `json:"priority"`
	IsDefault bool   `json:"is_default"`
	Enabled   bool   `json:"enabled"`
	Scope     string `json:"scope"`
}

type connectDoc struct {
	Version  int             `json:"version"`
	Accounts []exportAccount `json:"accounts"`
	Bots     []exportBot     `json:"bots"`
	Bindings []exportBinding `json:"bindings"`
}

func (s *Server) handleConnectExport(w http.ResponseWriter, r *http.Request) {
	doc := connectDoc{Version: 1, Accounts: []exportAccount{}, Bots: []exportBot{}, Bindings: []exportBinding{}}

	accounts, err := s.st.ListAccounts()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	idToSelf := map[int64]string{}
	for _, a := range accounts {
		idToSelf[a.ID] = a.SelfID
		tok, _ := s.ensureAccountToken(a)
		doc.Accounts = append(doc.Accounts, exportAccount{SelfID: a.SelfID, Name: a.Name, Enabled: a.Enabled, Token: tok})
	}
	bots, err := s.st.ListBots()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	idToName := map[int64]string{}
	for _, b := range bots {
		idToName[b.ID] = b.Name
		tok, _ := s.ensureBotToken(b)
		doc.Bots = append(doc.Bots, exportBot{Name: b.Name, Enabled: b.Enabled, Note: b.Note,
			RateLimit: b.RateLimit, ActionPolicy: b.ActionPolicy, Token: tok})
	}
	bindings, err := s.st.ListBindings()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, b := range bindings {
		doc.Bindings = append(doc.Bindings, exportBinding{
			Bot: idToName[b.BotID], Account: idToSelf[b.AccountID],
			Priority: b.Priority, IsDefault: b.IsDefault, Enabled: b.Enabled, Scope: b.Scope,
		})
	}
	writeJSON(w, http.StatusOK, doc)
}

func (s *Server) handleConnectImport(w http.ResponseWriter, r *http.Request) {
	var doc connectDoc
	if err := readJSON(r, &doc); err != nil {
		writeError(w, http.StatusBadRequest, "invalid connect document")
		return
	}
	accountIDs := map[string]int64{}
	for _, a := range doc.Accounts {
		if a.SelfID == "" {
			continue
		}
		acc, err := s.st.UpsertAccountBySelfID(a.SelfID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if a.Name != "" || !a.Enabled {
			_ = s.st.UpdateAccount(acc.ID, firstNonEmpty(a.Name, acc.Name), a.Enabled)
		}
		accountIDs[a.SelfID] = acc.ID
		if a.Token != "" {
			_ = s.st.SetAccountTokenHash(acc.ID, auth.HashToken(a.Token))
			_ = s.vault.Set(s.accountTokenKey(a.SelfID), a.Token)
		}
	}
	botIDs := map[string]int64{}
	for _, b := range doc.Bots {
		if b.Name == "" {
			continue
		}
		bot, err := s.st.GetBotByName(b.Name)
		if err != nil {
			bot, err = s.st.CreateBot(b.Name, "")
			if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
		}
		_ = s.st.UpdateBot(bot.ID, b.Name, b.Enabled, b.Note, b.RateLimit, b.ActionPolicy)
		botIDs[b.Name] = bot.ID
		if b.Token != "" {
			_ = s.st.SetBotTokenHash(bot.ID, auth.HashToken(b.Token))
			_ = s.vault.Set(s.botTokenKey(bot.ID), b.Token)
		}
	}
	imported := 0
	for _, b := range doc.Bindings {
		botID, ok := botIDs[b.Bot]
		if !ok {
			continue
		}
		accountID, ok := accountIDs[b.Account]
		if !ok {
			if acc, err := s.st.GetAccountBySelfID(b.Account); err == nil {
				accountID = acc.ID
			} else {
				continue
			}
		}
		if _, err := s.st.CreateBinding(botID, accountID, b.Priority, b.IsDefault, b.Scope); err == nil {
			imported++
		}
	}
	_ = s.hub.Reload()
	s.audit(r, "connect.import", "", "")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "accounts": len(doc.Accounts), "bots": len(doc.Bots), "bindings": imported})
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
