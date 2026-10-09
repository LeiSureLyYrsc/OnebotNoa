package store

import (
	"path/filepath"
	"testing"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestAccountBotBindingRoundTrip(t *testing.T) {
	st := newTestStore(t)

	acc, err := st.CreateAccount("10001", "alice")
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	if _, err := st.GetAccountBySelfID("10001"); err != nil {
		t.Fatalf("get by self_id: %v", err)
	}

	bot, err := st.CreateBot("demo", "hash")
	if err != nil {
		t.Fatalf("create bot: %v", err)
	}
	if _, err := st.GetBotByTokenHash("hash"); err != nil {
		t.Fatalf("get bot by token hash: %v", err)
	}

	b, err := st.CreateBinding(bot.ID, acc.ID, 100, true, "{}")
	if err != nil {
		t.Fatalf("create binding: %v", err)
	}
	list, err := st.ListBindingsByAccount(acc.ID)
	if err != nil || len(list) != 1 {
		t.Fatalf("list bindings: %v %d", err, len(list))
	}

	if err := st.DeleteAccount(acc.ID); err != nil {
		t.Fatalf("delete account: %v", err)
	}
	if list, _ := st.ListBindings(); len(list) != 0 {
		t.Fatalf("bindings should cascade on account delete, got %d", len(list))
	}
	_ = b
}

func TestUserSessionAudit(t *testing.T) {
	st := newTestStore(t)
	u, err := st.CreateUser("admin", "hash", "admin")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if _, err := st.GetUserByUsername("admin"); err != nil {
		t.Fatalf("get user: %v", err)
	}
	if err := st.CreateSession("tok", u.ID, 1<<62); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := st.GetSession("tok"); err != nil {
		t.Fatalf("get session: %v", err)
	}
	if err := st.AddAudit("admin", "test", "x", "", "127.0.0.1"); err != nil {
		t.Fatalf("audit: %v", err)
	}
	if entries, _ := st.ListAudit(10); len(entries) != 1 {
		t.Fatalf("expected 1 audit entry")
	}
}
