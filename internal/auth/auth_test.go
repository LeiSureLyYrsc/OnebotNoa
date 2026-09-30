package auth

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/model"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/store"
)

func newTestManager(t *testing.T) (*Manager, *store.Store) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	m := NewManager(st, time.Hour, slog.New(slog.DiscardHandler))
	return m, st
}

func TestPasswordHashRoundTrip(t *testing.T) {
	hash, err := HashPassword("s3cret-pw")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if !strings.HasPrefix(hash, "pbkdf2-sha256$") {
		t.Fatalf("unexpected hash format: %q", hash)
	}
	if strings.Contains(hash, "s3cret-pw") {
		t.Fatal("hash must not embed the plaintext")
	}
	if !VerifyPassword(hash, "s3cret-pw") {
		t.Fatal("correct password rejected")
	}
	if VerifyPassword(hash, "wrong-pw") {
		t.Fatal("wrong password accepted")
	}
	if VerifyPassword("garbage", "s3cret-pw") {
		t.Fatal("malformed hash accepted")
	}
	if VerifyPassword("", "") {
		t.Fatal("empty hash accepted")
	}
	if NeedsRehash(hash) {
		t.Fatal("fresh hash must not need rehashing")
	}
	if !NeedsRehash("pbkdf2-sha256$1000$AAAA$AAAA") {
		t.Fatal("outdated iteration count must need rehashing")
	}

	// Same password, different salts -> different hashes.
	other, err := HashPassword("s3cret-pw")
	if err != nil {
		t.Fatal(err)
	}
	if other == hash {
		t.Fatal("hashes must be salted")
	}
}

func TestTokenPrimitives(t *testing.T) {
	plain, hash, err := NewToken()
	if err != nil {
		t.Fatalf("NewToken: %v", err)
	}
	if len(plain) < 32 {
		t.Fatalf("token too short: %q", plain)
	}
	if !TokenMatches(plain, hash) {
		t.Fatal("token must match its hash")
	}
	if TokenMatches(plain+"x", hash) {
		t.Fatal("mutated token accepted")
	}
	if TokenMatches(plain, "") {
		t.Fatal("empty hash accepted")
	}
	plain2, _, _ := NewToken()
	if plain2 == plain {
		t.Fatal("tokens must be unique")
	}
}

func TestLoginAuthenticateLogout(t *testing.T) {
	ctx := context.Background()
	m, st := newTestManager(t)
	hash, err := HashPassword("pw")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateUser(ctx, "admin", hash, "admin"); err != nil {
		t.Fatal(err)
	}

	if _, err := m.Login(ctx, "admin", "bad", "127.0.0.1", "test"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("wrong password error = %v, want ErrInvalidCredentials", err)
	}
	if _, err := m.Login(ctx, "ghost", "pw", "127.0.0.1", "test"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("unknown user error = %v, want ErrInvalidCredentials", err)
	}

	session, err := m.Login(ctx, "admin", "pw", "127.0.0.1", "test-agent")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if session.Token == "" || session.Session.CSRFToken == "" {
		t.Fatalf("incomplete session: %+v", session)
	}
	if session.User.Username != "admin" {
		t.Fatalf("user = %+v", session.User)
	}

	got, err := m.Authenticate(ctx, session.Token)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if got.User.ID != session.User.ID || got.Session.CSRFToken != session.Session.CSRFToken {
		t.Fatalf("authenticated session mismatch: %+v", got)
	}

	if _, err := m.Authenticate(ctx, "not-a-token"); !errors.Is(err, ErrNoSession) {
		t.Fatalf("bad token error = %v, want ErrNoSession", err)
	}
	if _, err := m.Authenticate(ctx, ""); !errors.Is(err, ErrNoSession) {
		t.Fatalf("empty token error = %v, want ErrNoSession", err)
	}

	if err := m.Logout(ctx, session.Token); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if _, err := m.Authenticate(ctx, session.Token); !errors.Is(err, ErrNoSession) {
		t.Fatalf("session survived logout: %v", err)
	}
}

func TestExpiredSessionIsRejectedAndRemoved(t *testing.T) {
	ctx := context.Background()
	m, st := newTestManager(t)
	hash, _ := HashPassword("pw")
	user, err := st.CreateUser(ctx, "admin", hash, "admin")
	if err != nil {
		t.Fatal(err)
	}

	plain, tokenHash, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-2 * time.Hour)
	sess := model.Session{
		TokenHash:  tokenHash,
		UserID:     user.ID,
		CSRFToken:  "csrf",
		CreatedAt:  past,
		ExpiresAt:  past.Add(time.Minute),
		LastSeenAt: past,
	}
	if err := st.CreateSession(ctx, sess); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Authenticate(ctx, plain); !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("expired session error = %v, want ErrSessionExpired", err)
	}
	if _, err := st.SessionByTokenHash(ctx, tokenHash); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expired session must be deleted, got %v", err)
	}
}

func TestEnsureAdmin(t *testing.T) {
	ctx := context.Background()
	_, st := newTestManager(t)
	logger := slog.New(slog.DiscardHandler)

	generated, err := EnsureAdmin(ctx, st, "", logger)
	if err != nil {
		t.Fatalf("EnsureAdmin: %v", err)
	}
	if generated == "" {
		t.Fatal("expected a generated password")
	}
	user, err := st.UserByUsername(ctx, "admin")
	if err != nil {
		t.Fatalf("admin not created: %v", err)
	}
	if !VerifyPassword(user.PasswordHash, generated) {
		t.Fatal("generated password does not verify")
	}

	// Second call is a no-op.
	again, err := EnsureAdmin(ctx, st, "another-password", logger)
	if err != nil {
		t.Fatalf("EnsureAdmin (second): %v", err)
	}
	if again != "" {
		t.Fatalf("second call must be a no-op, got %q", again)
	}
	if !VerifyPassword(user.PasswordHash, generated) {
		t.Fatal("existing password was overwritten")
	}

	// Configured password path.
	st2, err := store.Open(ctx, filepath.Join(t.TempDir(), "auth2.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st2.Close() }()
	if pw, err := EnsureAdmin(ctx, st2, "configured-pw", logger); err != nil || pw != "" {
		t.Fatalf("EnsureAdmin(configured) = %q, %v", pw, err)
	}
	u2, err := st2.UserByUsername(ctx, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword(u2.PasswordHash, "configured-pw") {
		t.Fatal("configured password not applied")
	}
}
