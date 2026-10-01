package transport

import (
	"encoding/json"
	"sort"
	"sync"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/hub"
)

// testConnStore is the in-memory ConnStore the transport tests drive.
//
// Production reads connect.json; these tests exercise authentication and
// routing, so the double keeps the same shapes without a file. It also lets a
// test assert what the data plane asked for, which a real document would hide.
type testConnStore struct {
	mu           sync.Mutex
	accounts     []AccountRef
	accountToken map[string]string
	bots         []BotRef
	botToken     map[string]string
	bindings     []BindingRef
	bindingID    map[string]int64
	connections  []ConnectionRef
	connToken    map[int64]string
	status       map[string]string
	nextBot      int64
	nextBinding  int64
	nextConn     int64
}

func newTestConnStore() *testConnStore {
	return &testConnStore{
		accountToken: map[string]string{},
		botToken:     map[string]string{},
		bindingID:    map[string]int64{},
		connToken:    map[int64]string{},
		status:       map[string]string{},
	}
}

func (s *testConnStore) addAccount(selfID string) AccountRef {
	s.mu.Lock()
	defer s.mu.Unlock()
	account := AccountRef{ID: int64(len(s.accounts)) + 1, SelfID: selfID, Enabled: true}
	s.accounts = append(s.accounts, account)
	return account
}

func (s *testConnStore) addBot(name string) BotRef {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextBot++
	bot := BotRef{ID: s.nextBot, Name: name, Enabled: true,
		RateLimit: json.RawMessage("{}"), ActionPolicy: json.RawMessage("{}")}
	s.bots = append(s.bots, bot)
	return bot
}

func (s *testConnStore) addBinding(botName, selfID string, isDefault bool, scope string) BindingRef {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextBinding++
	s.bindingID[botName+"|"+selfID] = s.nextBinding
	binding := BindingRef{BotName: botName, AccountSelfID: selfID, Enabled: true,
		IsDefault: isDefault, Scope: json.RawMessage(scope)}
	s.bindings = append(s.bindings, binding)
	return binding
}

// bindingRefID returns the numeric id the file would give a grant; the tests use
// it the way the API does.
func (s *testConnStore) bindingRefID(botName, selfID string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bindingID[botName+"|"+selfID]
}

// setAccountToken binds a plaintext token to an account, mirroring what the
// WebUI does when it rotates one.
func (s *testConnStore) setAccountToken(selfID, token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.accountToken[token] = selfID
}

func (s *testConnStore) setBotToken(name, token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.botToken[token] = name
}

func (s *testConnStore) addConnection(ref ConnectionRef) ConnectionRef {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextConn++
	ref.ID = s.nextConn
	s.connections = append(s.connections, ref)
	return ref
}

// setActionPolicy replaces a Bot allow/deny list the way the WebUI does.
func (s *testConnStore) setActionPolicy(name, policy string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.bots {
		if s.bots[i].Name == name {
			s.bots[i].ActionPolicy = json.RawMessage(policy)
		}
	}
}

// disableAccount flips an account off the way the WebUI does.
func (s *testConnStore) disableAccount(selfID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.accounts {
		if s.accounts[i].SelfID == selfID {
			s.accounts[i].Enabled = false
		}
	}
}

// disableBot flips a Bot off the way the WebUI does.
func (s *testConnStore) disableBot(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.bots {
		if s.bots[i].Name == name {
			s.bots[i].Enabled = false
		}
	}
}

func (s *testConnStore) setConnectionToken(id int64, token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.connToken[id] = token
}

func (s *testConnStore) AccountBySelfID(selfID string) (AccountRef, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, account := range s.accounts {
		if account.SelfID == selfID {
			return account, true
		}
	}
	return AccountRef{}, false
}

func (s *testConnStore) AccountByToken(token string) (AccountRef, bool) {
	s.mu.Lock()
	selfID, ok := s.accountToken[token]
	s.mu.Unlock()
	if !ok {
		return AccountRef{}, false
	}
	return s.AccountBySelfID(selfID)
}

func (s *testConnStore) BotByToken(token string) (BotRef, bool) {
	s.mu.Lock()
	name, ok := s.botToken[token]
	s.mu.Unlock()
	if !ok {
		return BotRef{}, false
	}
	return s.BotByName(name)
}

func (s *testConnStore) BotByID(id int64) (BotRef, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, bot := range s.bots {
		if bot.ID == id {
			return bot, true
		}
	}
	return BotRef{}, false
}

func (s *testConnStore) BotByName(name string) (BotRef, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, bot := range s.bots {
		if bot.Name == name {
			return bot, true
		}
	}
	return BotRef{}, false
}

func (s *testConnStore) BindingsByBot(botName string) []BindingRef {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []BindingRef{}
	for _, binding := range s.bindings {
		if binding.BotName == botName {
			out = append(out, binding)
		}
	}
	return out
}

func (s *testConnStore) BindingByPair(botName, selfID string) (BindingRef, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, binding := range s.bindings {
		if binding.BotName == botName && binding.AccountSelfID == selfID {
			return binding, true
		}
	}
	return BindingRef{}, false
}

func (s *testConnStore) Connections() []ConnectionRef {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]ConnectionRef(nil), s.connections...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (s *testConnStore) ConnectionToken(id int64) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.connToken[id], nil
}

var _ ConnStore = (*testConnStore)(nil)

// ------------------------------------------------------------- hub-side view

// testHubStore projects the same in-memory data onto hub.ConnStore, so the tests
// can build a real hub over the same connections the data plane sees.
type testHubStore struct{ inner *testConnStore }

func (s *testConnStore) Hub() *testHubStore { return &testHubStore{inner: s} }

func (h *testHubStore) AccountBySelfID(selfID string) (hub.Account, bool) {
	account, ok := h.inner.AccountBySelfID(selfID)
	if !ok {
		return hub.Account{}, false
	}
	return toHubAccount(account), true
}

func (h *testHubStore) Accounts() []hub.Account {
	rows := h.inner.snapshotAccounts()
	out := make([]hub.Account, 0, len(rows))
	for _, row := range rows {
		out = append(out, toHubAccount(row))
	}
	return out
}

func toHubAccount(account AccountRef) hub.Account {
	return hub.Account{ID: account.ID, SelfID: account.SelfID, Enabled: account.Enabled, Status: hub.StatusOffline}
}

func (h *testHubStore) BotByID(id int64) (hub.Bot, bool) {
	bot, ok := h.inner.BotByID(id)
	if !ok {
		return hub.Bot{}, false
	}
	return toHubBot(bot), true
}

func (h *testHubStore) BotByName(name string) (hub.Bot, bool) {
	bot, ok := h.inner.BotByName(name)
	if !ok {
		return hub.Bot{}, false
	}
	return toHubBot(bot), true
}

func (h *testHubStore) Bots() []hub.Bot {
	rows := h.inner.snapshotBots()
	out := make([]hub.Bot, 0, len(rows))
	for _, row := range rows {
		out = append(out, toHubBot(row))
	}
	return out
}

func toHubBot(bot BotRef) hub.Bot {
	return hub.Bot{ID: bot.ID, Name: bot.Name, Enabled: bot.Enabled,
		RateLimit: bot.RateLimit, ActionPolicy: bot.ActionPolicy}
}

func (h *testHubStore) TouchBotSeen(int64) error { return nil }

func (h *testHubStore) SetBotToken(id int64, plain, tokenHash string) error {
	return h.inner.setBotTokenByID(id, plain)
}

func (h *testHubStore) Bindings() []hub.JBinding {
	return h.inner.hubBindings(h.inner.snapshotBindings())
}

func (h *testHubStore) BindingsByBot(botName string) []hub.JBinding {
	return h.inner.hubBindings(h.inner.BindingsByBot(botName))
}

func (h *testHubStore) BindingsByAccount(selfID string) []hub.JBinding {
	out := []hub.JBinding{}
	for _, binding := range h.inner.snapshotBindings() {
		if binding.AccountSelfID == selfID {
			out = append(out, h.inner.toHubBinding(binding))
		}
	}
	return out
}

func (h *testHubStore) BindingByPair(botName, selfID string) (hub.JBinding, bool) {
	binding, ok := h.inner.BindingByPair(botName, selfID)
	if !ok {
		return hub.JBinding{}, false
	}
	return h.inner.toHubBinding(binding), true
}

func (h *testHubStore) ApplyBinding(ref hub.BindingRef) error {
	h.inner.mu.Lock()
	defer h.inner.mu.Unlock()
	for i := range h.inner.bindings {
		if h.inner.bindings[i].BotName == ref.BotName && h.inner.bindings[i].AccountSelfID == ref.AccountSelfID {
			h.inner.bindings[i].Enabled = true
			return nil
		}
	}
	return nil
}

func (h *testHubStore) PersistAccountState(selfID, status string) error {
	h.inner.mu.Lock()
	defer h.inner.mu.Unlock()
	h.inner.status[selfID] = status
	return nil
}

func (h *testHubStore) EnsureAccount(selfID, source string) (hub.Account, bool, error) {
	if _, ok := h.inner.AccountBySelfID(selfID); ok {
		account, _ := h.inner.AccountBySelfID(selfID)
		return toHubAccount(account), false, nil
	}
	account := h.inner.addAccount(selfID)
	return toHubAccount(account), true, nil
}

func (h *testHubStore) SetAccountToken(id int64, plain, tokenHash, hint string) error {
	h.inner.mu.Lock()
	defer h.inner.mu.Unlock()
	for _, account := range h.inner.accounts {
		if account.ID == id {
			h.inner.accountToken[plain] = account.SelfID
		}
	}
	return nil
}

func (h *testHubStore) AccountToken(id int64) (string, error) {
	for _, account := range h.inner.snapshotAccounts() {
		if account.ID == id {
			return h.inner.tokenForAccount(account.SelfID), nil
		}
	}
	return "", nil
}

func (s *testConnStore) snapshotAccounts() []AccountRef {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]AccountRef(nil), s.accounts...)
}

func (s *testConnStore) snapshotBots() []BotRef {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]BotRef(nil), s.bots...)
}

func (s *testConnStore) snapshotBindings() []BindingRef {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]BindingRef(nil), s.bindings...)
}

func (s *testConnStore) tokenForAccount(selfID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for token, id := range s.accountToken {
		if id == selfID {
			return token
		}
	}
	return ""
}

func (s *testConnStore) setBotTokenByID(id int64, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, bot := range s.bots {
		if bot.ID == id {
			s.botToken[token] = bot.Name
		}
	}
	return nil
}

func (s *testConnStore) hubBindings(rows []BindingRef) []hub.JBinding {
	out := make([]hub.JBinding, 0, len(rows))
	for _, binding := range rows {
		out = append(out, s.toHubBinding(binding))
	}
	return out
}

func (s *testConnStore) toHubBinding(binding BindingRef) hub.JBinding {
	accountID := int64(0)
	if account, ok := s.AccountBySelfID(binding.AccountSelfID); ok {
		accountID = account.ID
	}
	botID := int64(0)
	if bot, ok := s.BotByName(binding.BotName); ok {
		botID = bot.ID
	}
	return hub.JBinding{
		ID:    s.bindingRefID(binding.BotName, binding.AccountSelfID),
		BotID: botID, BotName: binding.BotName,
		AccountID: accountID, AccountSelfID: binding.AccountSelfID,
		Priority: 100, IsDefault: binding.IsDefault, Enabled: binding.Enabled,
		Scope: binding.Scope,
	}
}

var _ hub.ConnStore = (*testHubStore)(nil)
