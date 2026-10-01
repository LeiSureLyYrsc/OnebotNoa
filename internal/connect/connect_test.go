package connect

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testBox is a reversible Sealer for the tests. It really does transform the
// value (a simple rotation), so a test can still tell a sealed token from a
// plaintext one on disk - which is exactly what the file-content assertions check.
type testBox struct{}

// Seal/Open are a genuine inverse pair, so a value that goes through the file
// comes back byte-identical - and its on-disk form is unreadable.
func (testBox) Seal(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	return "enc:v1:" + rotate(value), nil
}

func (testBox) Open(stored string) (string, error) {
	raw, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(stored, "enc:v1:"))
	if err != nil {
		return "", err
	}
	for i := range raw {
		raw[i] ^= 0x5A
	}
	return string(raw), nil
}

// rotate mirrors itself: applying it twice returns the original. Each byte is
// mapped to a printable character, so the on-disk form never looks like the
// plaintext a test is searching for.
// rotate is an exact inverse pair: it XORs each byte with a constant and
// base64-encodes the result, so applying it twice returns the input and the
// intermediate form never contains the plaintext as a substring.
func rotate(value string) string {
	buf := []byte(value)
	for i := range buf {
		buf[i] ^= 0x5A
	}
	return base64.RawStdEncoding.EncodeToString(buf)
}

func openTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "connect.json")
	store, err := Open(path, testBox{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return store, path
}

func TestOpenOnMissingFileStartsEmpty(t *testing.T) {
	store, path := openTestStore(t)
	if len(store.ListAccounts()) != 0 || len(store.ListBots()) != 0 {
		t.Fatal("a fresh store must start empty")
	}
	// Nothing is written until something changes: an untouched file should not
	// appear just because the hub started.
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("opening must not create the file, stat err = %v", err)
	}
}

// TestMutationsWriteTheFileImmediately pins the contract the whole design rests
// on: connect.json is always the current state, so a restart or a hand-inspection
// never shows a stale connection set.
func TestMutationsWriteTheFileImmediately(t *testing.T) {
	store, path := openTestStore(t)

	account, err := store.CreateAccount("10001", "主号", "manual")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateAccount("10001", "dup", "manual"); err != ErrDuplicateName {
		t.Fatalf("duplicate self_id error = %v, want ErrDuplicateName", err)
	}
	bot, err := store.CreateBotWithToken("nonebot", "plain-bot-token", "hash", "主框架")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateBinding(Binding{BotName: bot.Name, AccountSelfID: account.SelfID, Enabled: true}); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the file must exist after a mutation: %v", err)
	}
	// The stored token is sealed, never the plaintext.
	if strings.Contains(string(raw), "plain-bot-token") {
		t.Fatalf("the token must be sealed on disk:\n%s", raw)
	}
	if !strings.Contains(string(raw), "enc:v1:") {
		t.Fatalf("expected a sealed token marker:\n%s", raw)
	}

	// A second store over the same file sees the same world.
	reopened, err := Open(path, testBox{})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if len(reopened.ListAccounts()) != 1 || len(reopened.ListBots()) != 1 || len(reopened.ListBindings()) != 1 {
		t.Fatalf("reopened store lost data: %+v", reopened.Document())
	}
	plain, err := reopened.BotToken(bot.ID)
	if err != nil || plain != "plain-bot-token" {
		t.Fatalf("the token must round-trip through the file: %q %v", plain, err)
	}
}

// TestTokenLookupUsesThePlaintext covers the authorisation path: the hub has to
// recognise a presented token, and the sealed value is what it compares against.
func TestTokenLookupUsesThePlaintext(t *testing.T) {
	store, _ := openTestStore(t)
	account, err := store.CreateAccount("20001", "", "manual")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetAccountToken(account.ID, "account-secret", "hash-of-it", "acco…cret"); err != nil {
		t.Fatal(err)
	}

	found, ok := store.AccountByToken("account-secret")
	if !ok || found.SelfID != "20001" {
		t.Fatalf("the presented token must resolve to the account: %+v %v", found, ok)
	}
	// The hash lookup keeps working for files written by an earlier build.
	if byHash, ok := store.AccountByTokenHash("hash-of-it"); !ok || byHash.SelfID != "20001" {
		t.Fatal("a token hash stored by an older build must still resolve")
	}
	if _, ok := store.AccountByToken("wrong"); ok {
		t.Fatal("a wrong token must not resolve")
	}

	// Clearing removes both halves, so a deleted credential cannot linger.
	if err := store.SetAccountToken(account.ID, "", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.AccountByToken("account-secret"); ok {
		t.Fatal("a cleared token must stop working")
	}
}

func TestDeleteCascadesToDependents(t *testing.T) {
	store, _ := openTestStore(t)
	account, _ := store.CreateAccount("30001", "", "manual")
	bot, _ := store.CreateBot("cascade-bot", "hash", "")
	if _, err := store.CreateBinding(Binding{BotName: bot.Name, AccountSelfID: account.SelfID, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateConnection(Connection{
		Name: "cascade-dial", BotName: bot.Name, Kind: KindDownstreamDial, URL: "ws://x/",
	}, ""); err != nil {
		t.Fatal(err)
	}

	// Deleting the Bot takes its grants and its dial target with it: a binding to
	// a Bot that no longer exists would be a dangling permission.
	if err := store.DeleteBot(bot.ID); err != nil {
		t.Fatal(err)
	}
	if len(store.ListBindings()) != 0 {
		t.Fatalf("bindings survived the Bot: %+v", store.ListBindings())
	}
	if len(store.ListConnections()) != 0 {
		t.Fatalf("a downstream dial target survived its Bot: %+v", store.ListConnections())
	}

	// Deleting the account takes its bindings.
	bot2, _ := store.CreateBot("keep-bot", "hash", "")
	account2, _ := store.CreateAccount("30002", "", "manual")
	if _, err := store.CreateBinding(Binding{BotName: bot2.Name, AccountSelfID: account2.SelfID, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteAccount(account2.ID); err != nil {
		t.Fatal(err)
	}
	if len(store.ListBindings()) != 0 {
		t.Fatalf("bindings survived the account: %+v", store.ListBindings())
	}
}

func TestOnlyOneDefaultBindingPerBot(t *testing.T) {
	store, _ := openTestStore(t)
	_, _ = store.CreateAccount("40001", "", "manual")
	_, _ = store.CreateAccount("40002", "", "manual")
	bot, _ := store.CreateBot("default-bot", "hash", "")

	first, err := store.CreateBinding(Binding{BotName: bot.Name, AccountSelfID: "40001", IsDefault: true, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.CreateBinding(Binding{BotName: bot.Name, AccountSelfID: "40002", IsDefault: true, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}

	// Promoting the second must demote the first: the router resolves an unnamed
	// action through "the default", which only has an answer if it is unique.
	after, _ := store.BindingByID(first.ID)
	if after.IsDefault {
		t.Fatal("the previous default was not cleared")
	}
	promoted, _ := store.BindingByID(second.ID)
	if !promoted.IsDefault {
		t.Fatal("the new default was not set")
	}
}

func TestRenameBotFollowsItsReferences(t *testing.T) {
	store, _ := openTestStore(t)
	account, _ := store.CreateAccount("50001", "", "manual")
	bot, _ := store.CreateBot("before", "hash", "")
	if _, err := store.CreateBinding(Binding{BotName: bot.Name, AccountSelfID: account.SelfID, Enabled: true}); err != nil {
		t.Fatal(err)
	}

	bot.Name = "after"
	if err := store.UpdateBot(bot); err != nil {
		t.Fatal(err)
	}
	if len(store.BindingsByBot("after")) != 1 {
		t.Fatalf("the grant did not follow the rename: %+v", store.ListBindings())
	}
	if len(store.BindingsByBot("before")) != 0 {
		t.Fatal("the old name still has grants")
	}
}

// TestImportKeepsABackupAndResealsSecrets covers the restore path: an operator
// pastes a document, and the previous file must survive as .bak.
func TestImportKeepsABackupAndResealsSecrets(t *testing.T) {
	store, path := openTestStore(t)
	if _, err := store.CreateAccount("60001", "original", "manual"); err != nil {
		t.Fatal(err)
	}

	incoming := Document{
		SchemaVersion: SchemaVersion,
		Accounts: []Account{
			{SelfID: "60002", Name: "imported", Enabled: true, Grant: Grant{Token: "plaintext-token"}},
		},
		Bots:        []Bot{{Name: "imported-bot", Enabled: true}},
		Bindings:    []Binding{{BotName: "imported-bot", AccountSelfID: "60002", Enabled: true}},
		Connections: []Connection{},
	}
	if _, err := store.Replace(incoming, ServerInfo{UpstreamPath: "/onebot/v11/ws"}); err != nil {
		t.Fatalf("import: %v", err)
	}

	if _, err := os.Stat(path + ".bak"); err != nil {
		t.Fatalf("the previous file must be kept as .bak: %v", err)
	}
	accounts := store.ListAccounts()
	if len(accounts) != 1 || accounts[0].SelfID != "60002" {
		t.Fatalf("import did not replace the document: %+v", accounts)
	}
	// A plaintext token in the imported document is sealed on the way in.
	if accounts[0].Grant.Token == "plaintext-token" {
		t.Fatal("an imported plaintext token must be sealed")
	}
	if get, _ := store.AccountByToken("plaintext-token"); get.SelfID != "60002" {
		t.Fatal("the imported token must still resolve after re-sealing")
	}
}

func TestImportRejectsADanglingDocument(t *testing.T) {
	store, _ := openTestStore(t)
	// A binding that names entities the document does not declare would authorise
	// nothing but confuse every reader, so it is refused instead of half-applied.
	cases := map[string]Document{
		"binding to unknown bot": {
			Accounts: []Account{{SelfID: "70001", Enabled: true}},
			Bindings: []Binding{{BotName: "ghost", AccountSelfID: "70001", Enabled: true}},
		},
		"duplicate self_id": {
			Accounts: []Account{{SelfID: "70002", Enabled: true}, {SelfID: "70002", Enabled: true}},
		},
		"unknown kind": {
			Connections: []Connection{{Name: "x", Kind: "sideways", URL: "ws://x/"}},
		},
		"downstream dial without a bot": {
			Connections: []Connection{{Name: "x", Kind: KindDownstreamDial, URL: "ws://x/"}},
		},
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := store.Replace(doc, ServerInfo{}); err == nil {
				t.Fatalf("%s: expected the import to be refused", name)
			}
		})
	}
}

func TestSchemaVersionFromTheFutureIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "connect.json")
	body := `{"schema_version": 99, "accounts": [], "bots": [], "connections": [], "bindings": []}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path, testBox{}); err == nil {
		t.Fatal("a document from a newer build must not be read silently")
	}
}

// TestUnreadableFileIsNotOverwritten is the one that matters most: the file holds
// the operator tokens, so a parse error must never be "fixed" by replacing it.
func TestUnreadableFileIsNotOverwritten(t *testing.T) {
	path := filepath.Join(t.TempDir(), "connect.json")
	broken := "{\"schema_version\": 1, \"accounts\": [ oops"
	if err := os.WriteFile(path, []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path, testBox{}); err == nil {
		t.Fatal("a malformed document must be reported, not ignored")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != broken {
		t.Fatal("the unreadable file must be left exactly as it was")
	}
}

func TestHandWrittenPlaintextIsAccepted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "connect.json")
	body := `{
	  "schema_version": 1,
	  "accounts": [{"self_id": "80001", "name": "手工", "enabled": true,
	                "grant": {"token": "written-by-hand"}}],
	  "bots": [], "connections": [], "bindings": []
	}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := Open(path, testBox{})
	if err != nil {
		t.Fatalf("a hand-written document must load: %v", err)
	}
	if account, ok := store.AccountByToken("written-by-hand"); !ok || account.SelfID != "80001" {
		t.Fatal("a plaintext token written by hand must work")
	}
}

func TestSeedFromLegacyImportsOnce(t *testing.T) {
	store, _ := openTestStore(t)
	legacy := LegacyRow{
		Accounts: []LegacyAccount{{SelfID: "90001", Name: "旧号", Enabled: true}},
		Bots:     []LegacyBot{{Name: "旧bot", Enabled: true, ActionPolicy: json.RawMessage(`{"deny":["x"]}`)}},
		Bindings: []LegacyBinding{{BotName: "旧bot", AccountSelfID: "90001", Enabled: true}},
		Endpoints: []LegacyConnection{{
			Name: "旧拨号", Kind: KindUpstreamDial, URL: "ws://old/", Token: "old-token", Enabled: true,
		}},
	}
	imported, err := store.SeedFromLegacy(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if imported != 4 {
		t.Fatalf("imported = %d, want 4", imported)
	}
	if account, ok := store.AccountBySelfID("90001"); !ok || account.Name != "旧号" {
		t.Fatalf("account not imported: %+v", account)
	}
	if plain, err := store.ConnectionToken(store.ListConnections()[0].ID); err != nil || plain != "old-token" {
		t.Fatalf("the legacy token must survive and be sealed: %q %v", plain, err)
	}
	// Running it again must be a no-op: the migration is retried on every start.
	again, err := store.SeedFromLegacy(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if again != 0 {
		t.Fatalf("a second seed imported %d entities, want 0", again)
	}
}
