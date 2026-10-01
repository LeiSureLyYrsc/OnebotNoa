package hub

import (
	"encoding/json"
	"sync"
	"time"
)

// testConnStore is the in-memory ConnStore the hub tests drive.
//
// The production store is connect.json; these tests care about routing, not
// file handling, so the double stores the same shapes without a disk. It is
// deliberately simple: a map per entity, guarded by one mutex.
type testConnStore struct {
	mu          sync.Mutex
	accounts    []Account
	bots        []Bot
	bindings    []JBinding
	status      map[string]string
	tokens      map[int64]string
	botTokens   map[int64]string
	nextAcc     int64
	nextBot     int64
	nextBinding int64
}

func newTestConnStore() *testConnStore {
	return &testConnStore{
		status:    map[string]string{},
		tokens:    map[int64]string{},
		botTokens: map[int64]string{},
	}
}

func (s *testConnStore) addAccount(selfID, name string) Account {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextAcc++
	account := Account{ID: s.nextAcc, SelfID: selfID, Name: name, Enabled: true, Status: StatusOffline}
	s.accounts = append(s.accounts, account)
	return account
}

func (s *testConnStore) addBot(name string) Bot {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextBot++
	bot := Bot{ID: s.nextBot, Name: name, Enabled: true, RateLimit: json.RawMessage("{}"), ActionPolicy: json.RawMessage("{}")}
	s.bots = append(s.bots, bot)
	return bot
}

func (s *testConnStore) addBinding(botName, selfID string, isDefault bool, scope string) JBinding {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextBinding++
	binding := JBinding{
		ID: s.nextBinding, BotName: botName, AccountSelfID: selfID,
		Priority: 100, IsDefault: isDefault, Enabled: true, Scope: json.RawMessage(scope),
	}
	s.bindings = append(s.bindings, binding)
	return binding
}

func (s *testConnStore) AccountBySelfID(selfID string) (Account, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, account := range s.accounts {
		if account.SelfID == selfID {
			account.Status = s.statusOfLocked(selfID)
			return account, true
		}
	}
	return Account{}, false
}

func (s *testConnStore) Accounts() []Account {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Account, 0, len(s.accounts))
	for _, account := range s.accounts {
		account.Status = s.statusOfLocked(account.SelfID)
		out = append(out, account)
	}
	return out
}

func (s *testConnStore) statusOfLocked(selfID string) string {
	if status := s.status[selfID]; status != "" {
		return status
	}
	return StatusOffline
}

func (s *testConnStore) BotByID(id int64) (Bot, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, bot := range s.bots {
		if bot.ID == id {
			return bot, true
		}
	}
	return Bot{}, false
}

func (s *testConnStore) BotByName(name string) (Bot, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, bot := range s.bots {
		if bot.Name == name {
			return bot, true
		}
	}
	return Bot{}, false
}

func (s *testConnStore) Bots() []Bot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Bot(nil), s.bots...)
}

func (s *testConnStore) TouchBotSeen(int64) error { return nil }

func (s *testConnStore) SetBotToken(id int64, plain, tokenHash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.bots {
		if s.bots[i].ID == id {
			s.botTokens[id] = plain
			return nil
		}
	}
	return ErrNotFound
}

func (s *testConnStore) Bindings() []JBinding {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]JBinding(nil), s.bindings...)
}

func (s *testConnStore) BindingsByBot(botName string) []JBinding {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []JBinding{}
	for _, binding := range s.bindings {
		if binding.BotName == botName {
			out = append(out, binding)
		}
	}
	return out
}

func (s *testConnStore) BindingsByAccount(selfID string) []JBinding {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []JBinding{}
	for _, binding := range s.bindings {
		if binding.AccountSelfID == selfID {
			out = append(out, binding)
		}
	}
	return out
}

func (s *testConnStore) BindingByPair(botName, selfID string) (JBinding, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, binding := range s.bindings {
		if binding.BotName == botName && binding.AccountSelfID == selfID {
			return binding, true
		}
	}
	return JBinding{}, false
}

func (s *testConnStore) ApplyBinding(ref BindingRef) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.bindings {
		if s.bindings[i].BotName == ref.BotName && s.bindings[i].AccountSelfID == ref.AccountSelfID {
			s.bindings[i].Enabled = true
			return nil
		}
	}
	return ErrNotFound
}

func (s *testConnStore) PersistAccountState(selfID, status string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status[selfID] = status
	return nil
}

func (s *testConnStore) LastSeen(selfID string) (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.status[selfID]
	return time.Now(), ok
}

func (s *testConnStore) EnsureAccount(selfID, source string) (Account, bool, error) {
	if account, ok := s.AccountBySelfID(selfID); ok {
		return account, false, nil
	}
	return s.addAccount(selfID, ""), true, nil
}

func (s *testConnStore) SetAccountToken(id int64, plain, tokenHash, hint string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.accounts {
		if s.accounts[i].ID == id {
			s.tokens[id] = plain
			return nil
		}
	}
	return ErrNotFound
}

func (s *testConnStore) AccountToken(id int64) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tokens[id], nil
}

// SetStatus lets a test place an account in a given live state.
func (s *testConnStore) SetStatus(selfID, status string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status[selfID] = status
}

var _ ConnStore = (*testConnStore)(nil)
