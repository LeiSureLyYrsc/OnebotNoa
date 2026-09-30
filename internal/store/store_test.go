package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/model"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	st, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestOpenMigratesAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "test.db")

	// Derive the expectation from the embedded migrations so this test keeps
	// working as the schema grows.
	expected, err := loadMigrations()
	if err != nil {
		t.Fatalf("load migrations: %v", err)
	}
	if len(expected) == 0 {
		t.Fatal("no embedded migrations found")
	}
	wantVersion := expected[len(expected)-1].version
	wantCount := len(expected)

	st, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	var version int
	if err := st.DB().QueryRowContext(ctx,
		"SELECT MAX(version) FROM schema_migrations").Scan(&version); err != nil {
		t.Fatalf("read migration version: %v", err)
	}
	if version != wantVersion {
		t.Fatalf("schema version = %d, want %d", version, wantVersion)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Re-opening must not replay migrations or fail.
	st2, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	defer func() { _ = st2.Close() }()
	var count int
	if err := st2.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations").Scan(&count); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if count != wantCount {
		t.Fatalf("schema_migrations rows = %d, want %d (migrations must not replay)", count, wantCount)
	}
}

func TestUserLifecycle(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)

	n, err := st.CountUsers(ctx)
	if err != nil || n != 0 {
		t.Fatalf("CountUsers = %d, %v; want 0, nil", n, err)
	}

	created, err := st.CreateUser(ctx, "admin", "hash-1", "admin")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if created.ID == 0 || created.Username != "admin" || created.Role != "admin" {
		t.Fatalf("unexpected user: %+v", created)
	}

	byName, err := st.UserByUsername(ctx, "admin")
	if err != nil {
		t.Fatalf("UserByUsername: %v", err)
	}
	if byName.ID != created.ID || byName.PasswordHash != "hash-1" {
		t.Fatalf("round-trip mismatch: %+v", byName)
	}

	if _, err := st.UserByUsername(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing user error = %v, want ErrNotFound", err)
	}

	if err := st.UpdateUserPassword(ctx, created.ID, "hash-2"); err != nil {
		t.Fatalf("UpdateUserPassword: %v", err)
	}
	after, err := st.UserByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("UserByID: %v", err)
	}
	if after.PasswordHash != "hash-2" {
		t.Fatalf("password hash = %q, want hash-2", after.PasswordHash)
	}

	if err := st.UpdateUserPassword(ctx, 999, "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update missing user error = %v, want ErrNotFound", err)
	}

	users, err := st.ListUsers(ctx)
	if err != nil || len(users) != 1 {
		t.Fatalf("ListUsers = %d users, %v; want 1", len(users), err)
	}
}

func TestSessionLifecycle(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	user, err := st.CreateUser(ctx, "admin", "hash", "admin")
	if err != nil {
		t.Fatal(err)
	}

	nowT := time.Now().UTC().Truncate(time.Second)
	sess := model.Session{
		TokenHash:  "abc123",
		UserID:     user.ID,
		CSRFToken:  "csrf-1",
		CreatedAt:  nowT,
		ExpiresAt:  nowT.Add(time.Hour),
		LastSeenAt: nowT,
		UserAgent:  "test-agent",
		IP:         "127.0.0.1",
	}
	if err := st.CreateSession(ctx, sess); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	got, err := st.SessionByTokenHash(ctx, "abc123")
	if err != nil {
		t.Fatalf("SessionByTokenHash: %v", err)
	}
	if got.UserID != user.ID || got.CSRFToken != "csrf-1" || !got.ExpiresAt.Equal(sess.ExpiresAt) {
		t.Fatalf("session mismatch: %+v", got)
	}

	if _, err := st.SessionByTokenHash(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing session error = %v, want ErrNotFound", err)
	}

	if err := st.TouchSession(ctx, "abc123", nowT.Add(time.Minute)); err != nil {
		t.Fatalf("TouchSession: %v", err)
	}
	touched, _ := st.SessionByTokenHash(ctx, "abc123")
	if !touched.LastSeenAt.After(got.LastSeenAt) {
		t.Fatalf("last_seen_at not advanced: %v -> %v", got.LastSeenAt, touched.LastSeenAt)
	}

	// Expired rows are collected; live ones survive.
	expired := sess
	expired.TokenHash = "expired"
	expired.ExpiresAt = nowT.Add(-time.Minute)
	if err := st.CreateSession(ctx, expired); err != nil {
		t.Fatal(err)
	}
	n, err := st.DeleteExpiredSessions(ctx, nowT)
	if err != nil || n != 1 {
		t.Fatalf("DeleteExpiredSessions = %d, %v; want 1", n, err)
	}
	if _, err := st.SessionByTokenHash(ctx, "abc123"); err != nil {
		t.Fatalf("live session must survive cleanup: %v", err)
	}

	if err := st.DeleteSession(ctx, "abc123"); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if _, err := st.SessionByTokenHash(ctx, "abc123"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("session survived delete: %v", err)
	}
}

func TestSettingsAndAudit(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)

	if _, ok, err := st.GetSetting(ctx, "missing"); err != nil || ok {
		t.Fatalf("GetSetting(missing) = %v, %v; want false, nil", ok, err)
	}
	if err := st.SetSetting(ctx, "ui.theme", "xp"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if err := st.SetSetting(ctx, "ui.theme", "xp-dark"); err != nil {
		t.Fatalf("SetSetting upsert: %v", err)
	}
	value, ok, err := st.GetSetting(ctx, "ui.theme")
	if err != nil || !ok || value != "xp-dark" {
		t.Fatalf("GetSetting = %q, %v, %v; want xp-dark, true, nil", value, ok, err)
	}
	all, err := st.AllSettings(ctx)
	if err != nil || len(all) != 1 || all["ui.theme"] != "xp-dark" {
		t.Fatalf("AllSettings = %v, %v", all, err)
	}

	for i := 0; i < 3; i++ {
		if err := st.AppendAudit(ctx, model.AuditEntry{
			Actor: "admin", Action: "binding.create", Target: "bot:1", Detail: "test", IP: "127.0.0.1",
		}); err != nil {
			t.Fatalf("AppendAudit: %v", err)
		}
	}
	entries, err := st.ListAudit(ctx, 2, 0)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("ListAudit limit ignored: got %d entries", len(entries))
	}
	if entries[0].Action != "binding.create" || entries[0].At.IsZero() {
		t.Fatalf("unexpected audit entry: %+v", entries[0])
	}
	n, err := st.CountAudit(ctx)
	if err != nil || n != 3 {
		t.Fatalf("CountAudit = %d, %v; want 3", n, err)
	}
}
