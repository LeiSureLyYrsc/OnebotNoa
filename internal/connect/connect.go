// Package connect owns connect.json: the second of the two files the hub
// generates for itself.
//
//	config.yaml   process-level, static, hand-written   (listen addr, paths, policy)
//	connect.json  connection-level, generated, managed  (QQ instances, Bots,
//	              connections, bindings, grants and their secrets)
//
// The split follows one rule: anything an operator changes while the hub runs
// lives in connect.json, and anything that needs a restart lives in config.yaml.
// Keeping connections out of the SQLite file also means a connection set is a
// reviewable, diffable document that can be copied between deployments - and a
// damaged database cannot take the data plane down with it.
package connect

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// SchemaVersion is the version of the document this build writes.
const SchemaVersion = 1

// Grant is the credential one entity presents.
//
// Token is stored sealed (see internal/cryptobox) because the hub has to
// present it in the clear when it dials; plaintext values written by hand are
// accepted and re-sealed on the next save.
type Grant struct {
	Token     string `json:"token,omitempty"`
	TokenHash string `json:"token_hash,omitempty"`
	TokenHint string `json:"token_hint,omitempty"`
	Bootstrap string `json:"bootstrap_token,omitempty"`
	Sealed    bool   `json:"sealed,omitempty"`
	RotatedAt string `json:"rotated_at,omitempty"`
}

// Account is one QQ instance (an upstream OneBot implementation).
type Account struct {
	ID       int64    `json:"id"`
	SelfID   string   `json:"self_id"`
	Name     string   `json:"name,omitempty"`
	Nickname string   `json:"nickname,omitempty"`
	Avatar   string   `json:"avatar,omitempty"`
	Enabled  bool     `json:"enabled"`
	Tags     []string `json:"tags,omitempty"`
	Source   string   `json:"source,omitempty"`
	Note     string   `json:"note,omitempty"`
	Grant    Grant    `json:"grant,omitempty"`
}

// Bot is one downstream OneBot application.
type Bot struct {
	ID           int64           `json:"id"`
	Name         string          `json:"name"`
	Enabled      bool            `json:"enabled"`
	Note         string          `json:"note,omitempty"`
	Grant        Grant           `json:"grant,omitempty"`
	RateLimit    json.RawMessage `json:"rate_limit,omitempty"`
	ActionPolicy json.RawMessage `json:"action_policy,omitempty"`
}

// Connection is one physical link of the data plane. Kind uses the "who dials"
// naming of the rest of the project.
type Connection struct {
	ID            int64      `json:"id"`
	Name          string     `json:"name"`
	Kind          string     `json:"kind"`
	Enabled       bool       `json:"enabled"`
	Addr          string     `json:"addr,omitempty"`
	URL           string     `json:"url,omitempty"`
	Path          string     `json:"path,omitempty"`
	Mode          string     `json:"mode,omitempty"`
	AccountSelfID string     `json:"account_self_id,omitempty"`
	BotName       string     `json:"bot_name,omitempty"`
	FixedSelfID   string     `json:"fixed_self_id,omitempty"`
	TLSCert       string     `json:"tls_cert,omitempty"`
	TLSKey        string     `json:"tls_key,omitempty"`
	Reconnect     *Reconnect `json:"reconnect,omitempty"`
	Grant         Grant      `json:"grant,omitempty"`
	Note          string     `json:"note,omitempty"`
	CreatedAt     string     `json:"created_at,omitempty"`
	UpdatedAt     string     `json:"updated_at,omitempty"`
}

// Reconnect is the exponential-backoff schedule of a dialed connection.
type Reconnect struct {
	Min    string  `json:"min,omitempty"`
	Max    string  `json:"max,omitempty"`
	Jitter float64 `json:"jitter,omitempty"`
}

// Scope filters which events a binding forwards.
type Scope struct {
	PostTypes     []string `json:"post_types,omitempty"`
	IncludeGroups []string `json:"include_groups,omitempty"`
	ExcludeGroups []string `json:"exclude_groups,omitempty"`
	IncludeUsers  []string `json:"include_users,omitempty"`
	ExcludeUsers  []string `json:"exclude_users,omitempty"`
	ExcludeSelf   *bool    `json:"exclude_self,omitempty"`
	MetaEvents    string   `json:"meta_events,omitempty"`
}

// Binding grants one Bot access to one account.
type Binding struct {
	ID            int64  `json:"id"`
	BotName       string `json:"bot_name"`
	AccountSelfID string `json:"account_self_id"`
	Priority      int    `json:"priority,omitempty"`
	IsDefault     bool   `json:"is_default,omitempty"`
	Enabled       bool   `json:"enabled"`
	Scope         Scope  `json:"scope,omitempty"`
}

// Document is the whole connect.json file.
type Document struct {
	SchemaVersion int          `json:"schema_version"`
	GeneratedBy   string       `json:"generated_by,omitempty"`
	UpdatedAt     string       `json:"updated_at,omitempty"`
	Server        ServerInfo   `json:"server,omitempty"`
	Accounts      []Account    `json:"accounts"`
	Bots          []Bot        `json:"bots"`
	Connections   []Connection `json:"connections"`
	Bindings      []Binding    `json:"bindings"`
}

// ServerInfo records the shared endpoints so a copied connect.json documents
// which paths its tokens are meant for.
type ServerInfo struct {
	UpstreamPath   string `json:"upstream_path,omitempty"`
	DownstreamPath string `json:"downstream_path,omitempty"`
	HTTPAPIPath    string `json:"http_api_path,omitempty"`
	HTTPReportPath string `json:"http_report_path,omitempty"`
	PublicBase     string `json:"public_base,omitempty"`
}

// Connection kinds.
const (
	KindUpstreamListen   = "upstream_listen"
	KindUpstreamDial     = "upstream_dial"
	KindDownstreamListen = "downstream_listen"
	KindDownstreamDial   = "downstream_dial"
)

// ErrVersionTooNew refuses to read a document written by a newer build.
var ErrVersionTooNew = errors.New("connect: connect.json 的 schema_version 高于本程序支持的版本")

// Sealer is the encryption primitive the store needs (implemented by
// cryptobox.Box). Keeping it an interface keeps the package testable.
type Sealer interface {
	Seal(value string) (string, error)
	Open(value string) (string, error)
}

// emptyDocument is the starting point of a brand new deployment.
func emptyDocument() *Document {
	return &Document{
		SchemaVersion: SchemaVersion,
		GeneratedBy:   "OnebotNoa",
		Accounts:      []Account{},
		Bots:          []Bot{},
		Connections:   []Connection{},
		Bindings:      []Binding{},
	}
}

// readDocument loads path, returning an empty document when it does not exist.
func readDocument(path string) (*Document, bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return emptyDocument(), false, nil
		}
		return nil, false, fmt.Errorf("connect: 读取 %s: %w", path, err)
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return emptyDocument(), true, nil
	}
	var doc Document
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, false, fmt.Errorf("connect: 解析 %s: %w", path, err)
	}
	if doc.SchemaVersion > SchemaVersion {
		return nil, false, fmt.Errorf("%w（文件 %d，本程序 %d）", ErrVersionTooNew, doc.SchemaVersion, SchemaVersion)
	}
	if doc.SchemaVersion == 0 {
		doc.SchemaVersion = SchemaVersion
	}
	doc.Accounts = normaliseAccounts(doc.Accounts)
	doc.Bots = normaliseBots(doc.Bots)
	doc.Connections = normaliseConnections(doc.Connections)
	doc.Bindings = normaliseBindings(doc.Bindings)
	return &doc, true, nil
}

// Store is the in-memory, always-consistent view of connect.json. Every
// mutation is followed by a save, so the file never lags behind the runtime.
type Store struct {
	mu   sync.RWMutex
	path string
	doc  *Document
	box  Sealer
	// readOnly is set when the file exists but could not be understood; the hub
	// then refuses to write rather than replacing data it could not read.
	readOnly bool
	lastErr  error
}

// Open loads (or creates) connect.json at path.
//
// A file that exists but cannot be parsed is NOT overwritten: Open returns the
// error and the caller decides. Silently replacing a connection file would
// destroy the only copy of the operator's tokens.
func Open(path string, box Sealer) (*Store, error) {
	doc, _, err := readDocument(path)
	if err != nil {
		return nil, err
	}
	return &Store{path: path, doc: doc, box: box}, nil
}

// Path returns the file this store writes.
func (s *Store) Path() string { return s.path }

// LastError returns the most recent save/load problem, for the API to surface.
func (s *Store) LastError() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastErr
}

// ------------------------------------------------------------------ accounts

// ListAccounts returns every QQ instance, sorted by self_id.
func (s *Store) ListAccounts() []Account {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := append([]Account(nil), s.doc.Accounts...)
	sort.Slice(out, func(i, j int) bool { return out[i].SelfID < out[j].SelfID })
	return out
}

// AccountBySelfID looks up one QQ instance.
func (s *Store) AccountBySelfID(selfID string) (Account, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, account := range s.doc.Accounts {
		if account.SelfID == selfID {
			return account, true
		}
	}
	return Account{}, false
}

// AccountByID looks up one QQ instance by its stable id.
func (s *Store) AccountByID(id int64) (Account, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, account := range s.doc.Accounts {
		if account.ID == id {
			return account, true
		}
	}
	return Account{}, false
}

// CreateAccount adds a QQ instance.
func (s *Store) CreateAccount(selfID, name, source string) (Account, error) {
	selfID = strings.TrimSpace(selfID)
	if selfID == "" {
		return Account{}, errors.New("connect: self_id 不能为空")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.findAccountLocked(selfID); exists {
		return Account{}, ErrDuplicateName
	}
	account := Account{
		ID: s.nextAccountIDLocked(), SelfID: selfID, Name: name,
		Enabled: true, Source: source,
	}
	s.doc.Accounts = append(s.doc.Accounts, account)
	if err := s.saveLocked(); err != nil {
		return Account{}, err
	}
	return account, nil
}

// EnsureAccount returns the account for selfID, creating it when missing.
func (s *Store) EnsureAccount(selfID, name, source string) (Account, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if account, ok := s.findAccountLocked(selfID); ok {
		return account, false, nil
	}
	account := Account{ID: s.nextAccountIDLocked(), SelfID: selfID, Name: name, Enabled: true, Source: source}
	s.doc.Accounts = append(s.doc.Accounts, account)
	if err := s.saveLocked(); err != nil {
		return Account{}, false, err
	}
	return account, true, nil
}

// UpdateAccount replaces the mutable fields of one account.
func (s *Store) UpdateAccount(account Account) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, existing := range s.doc.Accounts {
		if existing.ID != account.ID {
			continue
		}
		// self_id is the identity: never let an edit change it silently.
		account.SelfID = existing.SelfID
		account.Grant = existing.Grant
		s.doc.Accounts[i] = account
		return s.saveLocked()
	}
	return ErrNotFound
}

// SetAccountToken binds (or clears, with an empty tokenHash) a per-instance
// token. Both the sealed plaintext and the hash are kept: the relay dials with
// the first and recognises incoming connections with the second.
func (s *Store) SetAccountToken(id int64, plain, tokenHash, hint string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.doc.Accounts {
		if s.doc.Accounts[i].ID != id {
			continue
		}
		if tokenHash == "" {
			s.doc.Accounts[i].Grant = Grant{}
			return s.saveLocked()
		}
		sealed, err := s.sealLocked(plain)
		if err != nil {
			return err
		}
		s.doc.Accounts[i].Grant = Grant{
			Token: sealed, TokenHash: tokenHash, TokenHint: hint,
			Sealed: plain != "", RotatedAt: time.Now().Format(time.RFC3339),
		}
		return s.saveLocked()
	}
	return ErrNotFound
}

// AccountByToken resolves a pre-bound per-instance token from its plaintext.
//
// Because connect.json holds the sealed plaintext anyway (the relay has to
// present it when it dials), incoming connections are checked against that
// value directly. The stored hash stays for compatibility with files written
// by earlier builds.
func (s *Store) AccountByToken(token string) (Account, bool) {
	if token == "" {
		return Account{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, candidate := range s.doc.Accounts {
		if candidate.Grant.TokenHash != "" && candidate.Grant.TokenHash == token {
			return candidate, true
		}
		if plain, err := openToken(s.box, candidate.Grant.Token); err == nil && plain == token {
			return candidate, true
		}
	}
	return Account{}, false
}

// AccountByTokenHash resolves a pre-bound per-instance token.
func (s *Store) AccountByTokenHash(tokenHash string) (Account, bool) {
	if tokenHash == "" {
		return Account{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, account := range s.doc.Accounts {
		if account.Grant.TokenHash != "" && account.Grant.TokenHash == tokenHash {
			return account, true
		}
	}
	return Account{}, false
}

// AccountToken returns the plaintext token of an account (for display and for
// the direct-connect address the WebUI offers).
func (s *Store) AccountToken(id int64) (string, error) {
	s.mu.RLock()
	account, ok := s.accountByIDLocked(id)
	sealed := ""
	if ok {
		sealed = account.Grant.Token
	}
	box := s.box
	s.mu.RUnlock()
	if !ok {
		return "", ErrNotFound
	}
	return openToken(box, sealed)
}

// DeleteAccount removes a QQ instance together with the bindings that named it.
func (s *Store) DeleteAccount(id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	selfID := ""
	kept := s.doc.Accounts[:0]
	for _, account := range s.doc.Accounts {
		if account.ID == id {
			selfID = account.SelfID
			continue
		}
		kept = append(kept, account)
	}
	if selfID == "" {
		return ErrNotFound
	}
	s.doc.Accounts = kept
	bindings := s.doc.Bindings[:0]
	for _, binding := range s.doc.Bindings {
		if binding.AccountSelfID == selfID {
			continue
		}
		bindings = append(bindings, binding)
	}
	s.doc.Bindings = bindings
	return s.saveLocked()
}

// ---------------------------------------------------------------------- bots

// ListBots returns every downstream application, sorted by name.
func (s *Store) ListBots() []Bot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := append([]Bot(nil), s.doc.Bots...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// BotByID looks up one Bot.
func (s *Store) BotByID(id int64) (Bot, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, bot := range s.doc.Bots {
		if bot.ID == id {
			return bot, true
		}
	}
	return Bot{}, false
}

// BotByName looks up one Bot.
func (s *Store) BotByName(name string) (Bot, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, bot := range s.doc.Bots {
		if bot.Name == name {
			return bot, true
		}
	}
	return Bot{}, false
}

// CreateBot adds a downstream application.
func (s *Store) CreateBot(name, tokenHash, note string) (Bot, error) {
	return s.CreateBotWithToken(name, "", tokenHash, note)
}

// CreateBotWithToken is CreateBot plus the plaintext token, so the WebUI can
// show it again later (the token lives in a file, not in a hashed column).
func (s *Store) CreateBotWithToken(name, plain, tokenHash, note string) (Bot, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Bot{}, errors.New("connect: Bot 名称不能为空")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.findBotLocked(name); exists {
		return Bot{}, ErrDuplicateName
	}
	sealed, err := s.sealLocked(plain)
	if err != nil {
		return Bot{}, err
	}
	// An empty token is a legitimate state (a Bot created before its credential);
	// only a non-empty one is sealed and time-stamped.
	grant := Grant{}
	if plain != "" || tokenHash != "" {
		grant = Grant{
			Token: sealed, TokenHash: tokenHash, TokenHint: hintOf(plain),
			Sealed: plain != "", RotatedAt: time.Now().Format(time.RFC3339),
		}
	}
	bot := Bot{ID: s.nextBotIDLocked(), Name: name, Enabled: true, Note: note, Grant: grant}
	s.doc.Bots = append(s.doc.Bots, bot)
	if err := s.saveLocked(); err != nil {
		return Bot{}, err
	}
	return bot, nil
}

// UpdateBot replaces the mutable fields of one Bot (its name is the identity).
func (s *Store) UpdateBot(bot Bot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, existing := range s.doc.Bots {
		if existing.ID != bot.ID {
			continue
		}
		oldName := existing.Name
		bot.Grant = existing.Grant
		if strings.TrimSpace(bot.Name) == "" {
			bot.Name = oldName
		}
		if bot.Name != oldName {
			if _, taken := s.findBotLocked(bot.Name); taken {
				return ErrDuplicateName
			}
			for j := range s.doc.Bindings {
				if s.doc.Bindings[j].BotName == oldName {
					s.doc.Bindings[j].BotName = bot.Name
				}
			}
			for j := range s.doc.Connections {
				if s.doc.Connections[j].BotName == oldName {
					s.doc.Connections[j].BotName = bot.Name
				}
			}
		}
		s.doc.Bots[i] = bot
		return s.saveLocked()
	}
	return ErrNotFound
}

// SetBotToken replaces a Bot's credential.
func (s *Store) SetBotToken(id int64, plain, tokenHash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.doc.Bots {
		if s.doc.Bots[i].ID != id {
			continue
		}
		sealed, err := s.sealLocked(plain)
		if err != nil {
			return err
		}
		s.doc.Bots[i].Grant = Grant{
			Token: sealed, TokenHash: tokenHash, TokenHint: hintOf(plain),
			Sealed: plain != "", RotatedAt: time.Now().Format(time.RFC3339),
		}
		return s.saveLocked()
	}
	return ErrNotFound
}

// BotByToken resolves the token a Bot presents, from its plaintext.
func (s *Store) BotByToken(token string) (Bot, bool) {
	if token == "" {
		return Bot{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, candidate := range s.doc.Bots {
		if candidate.Grant.TokenHash != "" && candidate.Grant.TokenHash == token {
			return candidate, true
		}
		if plain, err := openToken(s.box, candidate.Grant.Token); err == nil && plain == token {
			return candidate, true
		}
	}
	return Bot{}, false
}

// BotByTokenHash resolves the token a Bot presents.
func (s *Store) BotByTokenHash(tokenHash string) (Bot, bool) {
	if tokenHash == "" {
		return Bot{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, bot := range s.doc.Bots {
		if bot.Grant.TokenHash != "" && bot.Grant.TokenHash == tokenHash {
			return bot, true
		}
	}
	return Bot{}, false
}

// BotToken returns the plaintext token of a Bot.
func (s *Store) BotToken(id int64) (string, error) {
	s.mu.RLock()
	bot, ok := s.botByIDLocked(id)
	box := s.box
	sealed := bot.Grant.Token
	s.mu.RUnlock()
	if !ok {
		return "", ErrNotFound
	}
	return openToken(box, sealed)
}

// DeleteBot removes a Bot and the grants that referenced it.
func (s *Store) DeleteBot(id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	name := ""
	kept := s.doc.Bots[:0]
	for _, bot := range s.doc.Bots {
		if bot.ID == id {
			name = bot.Name
			continue
		}
		kept = append(kept, bot)
	}
	if name == "" {
		return ErrNotFound
	}
	s.doc.Bots = kept
	bindings := s.doc.Bindings[:0]
	for _, binding := range s.doc.Bindings {
		if binding.BotName == name {
			continue
		}
		bindings = append(bindings, binding)
	}
	s.doc.Bindings = bindings
	dropped := s.doc.Connections[:0]
	for _, connection := range s.doc.Connections {
		if connection.BotName == name && connection.Kind == KindDownstreamDial {
			continue
		}
		dropped = append(dropped, connection)
	}
	s.doc.Connections = dropped
	return s.saveLocked()
}

// --------------------------------------------------------------- connections

// ListConnections returns every configured link, sorted by name.
func (s *Store) ListConnections() []Connection {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := append([]Connection(nil), s.doc.Connections...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ConnectionByID looks up one connection.
func (s *Store) ConnectionByID(id int64) (Connection, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, connection := range s.doc.Connections {
		if connection.ID == id {
			return connection, true
		}
	}
	return Connection{}, false
}

// ConnectionByName looks up one connection.
func (s *Store) ConnectionByName(name string) (Connection, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, connection := range s.doc.Connections {
		if connection.Name == name {
			return connection, true
		}
	}
	return Connection{}, false
}

// CreateConnection adds a link. The plaintext token (if any) is sealed.
func (s *Store) CreateConnection(connection Connection, token string) (Connection, error) {
	connection.Name = strings.TrimSpace(connection.Name)
	if connection.Name == "" {
		return Connection{}, errors.New("connect: name 不能为空")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.findConnectionLocked(connection.Name); exists {
		return Connection{}, ErrDuplicateName
	}
	sealed, err := s.sealLocked(token)
	if err != nil {
		return Connection{}, err
	}
	nowT := time.Now().Format(time.RFC3339)
	connection.ID = s.nextConnectionIDLocked()
	connection.CreatedAt = nowT
	connection.UpdatedAt = nowT
	connection.Grant = Grant{Token: sealed, Sealed: token != "", TokenHint: hintOf(token), RotatedAt: nowT}
	s.doc.Connections = append(s.doc.Connections, connection)
	if err := s.saveLocked(); err != nil {
		return Connection{}, err
	}
	return connection, nil
}

// UpdateConnection replaces a link. An empty token keeps the stored one, because
// the API never returns it.
func (s *Store) UpdateConnection(connection Connection, token string, replaceToken bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, existing := range s.doc.Connections {
		if existing.ID != connection.ID {
			continue
		}
		if connection.Name != existing.Name {
			if _, taken := s.findConnectionLocked(connection.Name); taken {
				return ErrDuplicateName
			}
		}
		if replaceToken {
			sealed, err := s.sealLocked(token)
			if err != nil {
				return err
			}
			connection.Grant = Grant{
				Token: sealed, Sealed: token != "", TokenHint: hintOf(token),
				RotatedAt: time.Now().Format(time.RFC3339),
			}
		} else {
			connection.Grant = existing.Grant
		}
		connection.ID = existing.ID
		connection.CreatedAt = existing.CreatedAt
		connection.UpdatedAt = time.Now().Format(time.RFC3339)
		s.doc.Connections[i] = connection
		return s.saveLocked()
	}
	return ErrNotFound
}

// ConnectionToken returns the plaintext credential of a link.
func (s *Store) ConnectionToken(id int64) (string, error) {
	s.mu.RLock()
	var sealed string
	found := false
	for _, connection := range s.doc.Connections {
		if connection.ID == id {
			sealed, found = connection.Grant.Token, true
			break
		}
	}
	box := s.box
	s.mu.RUnlock()
	if !found {
		return "", ErrNotFound
	}
	return openToken(box, sealed)
}

// DeleteConnection removes a link.
func (s *Store) DeleteConnection(id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.doc.Connections[:0]
	removed := false
	for _, connection := range s.doc.Connections {
		if connection.ID == id {
			removed = true
			continue
		}
		kept = append(kept, connection)
	}
	if !removed {
		return ErrNotFound
	}
	s.doc.Connections = kept
	return s.saveLocked()
}

// ------------------------------------------------------------------ bindings

// ListBindings returns every grant, sorted by account then name.
func (s *Store) ListBindings() []Binding {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := append([]Binding(nil), s.doc.Bindings...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].AccountSelfID != out[j].AccountSelfID {
			return out[i].AccountSelfID < out[j].AccountSelfID
		}
		if out[i].Priority != out[j].Priority {
			return out[i].Priority > out[j].Priority
		}
		return out[i].BotName < out[j].BotName
	})
	return out
}

// BindingsByBot returns the accounts granted to one Bot.
func (s *Store) BindingsByBot(botName string) []Binding {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []Binding{}
	for _, binding := range s.doc.Bindings {
		if binding.BotName == botName {
			out = append(out, binding)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Priority > out[j].Priority })
	return out
}

// BindingsByAccount returns the Bots allowed to use one account.
func (s *Store) BindingsByAccount(selfID string) []Binding {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []Binding{}
	for _, binding := range s.doc.Bindings {
		if binding.AccountSelfID == selfID {
			out = append(out, binding)
		}
	}
	return out
}

// BindingByPair returns one grant.
func (s *Store) BindingByPair(botName, selfID string) (Binding, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, binding := range s.doc.Bindings {
		if binding.BotName == botName && binding.AccountSelfID == selfID {
			return binding, true
		}
	}
	return Binding{}, false
}

// BindingByID returns one grant.
func (s *Store) BindingByID(id int64) (Binding, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, binding := range s.doc.Bindings {
		if binding.ID == id {
			return binding, true
		}
	}
	return Binding{}, false
}

// CreateBinding grants an account to a Bot.
func (s *Store) CreateBinding(binding Binding) (Binding, error) {
	if _, ok := s.AccountBySelfID(binding.AccountSelfID); !ok {
		return Binding{}, ErrNotFound
	}
	if _, ok := s.BotByName(binding.BotName); !ok {
		return Binding{}, ErrNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.doc.Bindings {
		if existing.BotName == binding.BotName && existing.AccountSelfID == binding.AccountSelfID {
			return Binding{}, ErrDuplicateName
		}
	}
	if binding.Priority == 0 {
		binding.Priority = 100
	}
	binding.ID = s.nextBindingIDLocked()
	if binding.IsDefault {
		s.clearDefaultLocked(binding.BotName, 0)
	}
	s.doc.Bindings = append(s.doc.Bindings, binding)
	if err := s.saveLocked(); err != nil {
		return Binding{}, err
	}
	return binding, nil
}

// UpdateBinding refreshes a grant; becoming the default clears the flag on the
// Bot's other grants so exactly one default can exist.
func (s *Store) UpdateBinding(binding Binding) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, existing := range s.doc.Bindings {
		if existing.ID != binding.ID {
			continue
		}
		binding.BotName = existing.BotName
		binding.AccountSelfID = existing.AccountSelfID
		if binding.IsDefault {
			s.clearDefaultLocked(binding.BotName, binding.ID)
		}
		s.doc.Bindings[i] = binding
		return s.saveLocked()
	}
	return ErrNotFound
}

// DeleteBinding removes a grant.
func (s *Store) DeleteBinding(id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.doc.Bindings[:0]
	removed := false
	for _, binding := range s.doc.Bindings {
		if binding.ID == id {
			removed = true
			continue
		}
		kept = append(kept, binding)
	}
	if !removed {
		return ErrNotFound
	}
	s.doc.Bindings = kept
	return s.saveLocked()
}

// -------------------------------------------------------------------- export

// Document returns a copy of the document. It is the same value Export returns;
// the name matches what the API calls it.
func (s *Store) Document() Document { return s.Export() }

// Replace is Import under the name the API uses.
func (s *Store) Replace(incoming Document, server ServerInfo) (Document, error) {
	return s.Import(incoming, server)
}

// Export returns a copy of the document for the API and for backups.
func (s *Store) Export() Document {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.copyLocked()
}

// Import replaces the document with a validated copy of incoming, keeping the
// existing file as <path>.bak first.
func (s *Store) Import(incoming Document, server ServerInfo) (Document, error) {
	if incoming.SchemaVersion > SchemaVersion {
		return Document{}, fmt.Errorf("%w（文件 %d，本程序 %d）", ErrVersionTooNew, incoming.SchemaVersion, SchemaVersion)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.backupLocked(); err != nil {
		return Document{}, err
	}
	incoming.SchemaVersion = SchemaVersion
	incoming.GeneratedBy = "OnebotNoa"
	incoming.Server = server
	incoming.Accounts = normaliseAccounts(incoming.Accounts)
	incoming.Bots = normaliseBots(incoming.Bots)
	incoming.Connections = normaliseConnections(incoming.Connections)
	incoming.Bindings = normaliseBindings(incoming.Bindings)
	if err := validateDocument(&incoming); err != nil {
		return Document{}, err
	}
	// Re-seal: an imported file may carry plaintext tokens.
	for i := range incoming.Accounts {
		if incoming.Accounts[i].Grant.Token != "" && !isSealedMarker(incoming.Accounts[i].Grant.Token) {
			sealed, err := s.sealLocked(incoming.Accounts[i].Grant.Token)
			if err != nil {
				return Document{}, err
			}
			incoming.Accounts[i].Grant.Token = sealed
			incoming.Accounts[i].Grant.Sealed = true
		}
	}
	for i := range incoming.Bots {
		if incoming.Bots[i].Grant.Token != "" && !isSealedMarker(incoming.Bots[i].Grant.Token) {
			sealed, err := s.sealLocked(incoming.Bots[i].Grant.Token)
			if err != nil {
				return Document{}, err
			}
			incoming.Bots[i].Grant.Token = sealed
			incoming.Bots[i].Grant.Sealed = true
		}
	}
	for i := range incoming.Connections {
		if incoming.Connections[i].Grant.Token != "" && !isSealedMarker(incoming.Connections[i].Grant.Token) {
			sealed, err := s.sealLocked(incoming.Connections[i].Grant.Token)
			if err != nil {
				return Document{}, err
			}
			incoming.Connections[i].Grant.Token = sealed
			incoming.Connections[i].Grant.Sealed = true
		}
	}
	s.doc = &incoming
	if err := s.saveLocked(); err != nil {
		return Document{}, err
	}
	// Cannot call Export here: it takes the read lock this method already holds.
	// Copy under the same lock instead (a write lock is exclusive, so this is the
	// same guarantee Export would give a caller).
	return s.copyLocked(), nil
}

// copyLocked returns a snapshot of the document. The caller must hold the lock.
func (s *Store) copyLocked() Document {
	out := *s.doc
	out.Accounts = append([]Account(nil), s.doc.Accounts...)
	out.Bots = append([]Bot(nil), s.doc.Bots...)
	out.Connections = append([]Connection(nil), s.doc.Connections...)
	out.Bindings = append([]Binding(nil), s.doc.Bindings...)
	return out
}

// backupLocked copies the current file next to itself before a destructive
// rewrite. A missing file is not an error.
func (s *Store) backupLocked() error {
	if _, err := os.Stat(s.path); err != nil {
		return nil
	}
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return fmt.Errorf("connect: 备份前读取: %w", err)
	}
	if err := os.WriteFile(s.path+".bak", raw, 0o600); err != nil {
		return fmt.Errorf("connect: 写入备份 %s.bak: %w", s.path, err)
	}
	return nil
}

// SetServerInfo records the shared endpoints in the document.
func (s *Store) SetServerInfo(info ServerInfo) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.doc.Server == info {
		return nil
	}
	s.doc.Server = info
	return s.saveLocked()
}

// validateDocument rejects a document that would break the runtime.
func validateDocument(doc *Document) error {
	seenAccounts := map[string]bool{}
	for _, account := range doc.Accounts {
		if strings.TrimSpace(account.SelfID) == "" {
			return errors.New("connect: 存在 self_id 为空的账号")
		}
		if seenAccounts[account.SelfID] {
			return fmt.Errorf("connect: 账号 self_id %q 重复", account.SelfID)
		}
		seenAccounts[account.SelfID] = true
	}
	seenBots := map[string]bool{}
	for _, bot := range doc.Bots {
		if strings.TrimSpace(bot.Name) == "" {
			return errors.New("connect: 存在名称为空的 Bot")
		}
		if seenBots[bot.Name] {
			return fmt.Errorf("connect: Bot 名称 %q 重复", bot.Name)
		}
		seenBots[bot.Name] = true
	}
	seenConnections := map[string]bool{}
	for _, connection := range doc.Connections {
		if connection.Name == "" {
			return errors.New("connect: 存在名称为空的连接")
		}
		if seenConnections[connection.Name] {
			return fmt.Errorf("connect: 连接名称 %q 重复", connection.Name)
		}
		seenConnections[connection.Name] = true
		switch connection.Kind {
		case KindUpstreamListen, KindDownstreamListen:
			if strings.TrimSpace(connection.Addr) == "" {
				return fmt.Errorf("connect: 连接 %q（%s）缺少 addr", connection.Name, connection.Kind)
			}
		case KindUpstreamDial, KindDownstreamDial:
			if strings.TrimSpace(connection.URL) == "" {
				return fmt.Errorf("connect: 连接 %q（%s）缺少 url", connection.Name, connection.Kind)
			}
		default:
			return fmt.Errorf("connect: 连接 %q 的 kind %q 不是 %s|%s|%s|%s", connection.Name, connection.Kind,
				KindUpstreamListen, KindUpstreamDial, KindDownstreamListen, KindDownstreamDial)
		}
		if connection.Kind == KindDownstreamDial && connection.BotName == "" {
			return fmt.Errorf("connect: 下游拨号连接 %q 必须指定 bot_name", connection.Name)
		}
		if connection.Kind == KindUpstreamListen && connection.AccountSelfID == "" {
			return fmt.Errorf("connect: 上游独立监听 %q 必须指定 account_self_id", connection.Name)
		}
	}
	for _, binding := range doc.Bindings {
		if !seenAccounts[binding.AccountSelfID] {
			return fmt.Errorf("connect: 绑定引用了不存在的账号 %q", binding.AccountSelfID)
		}
		if !seenBots[binding.BotName] {
			return fmt.Errorf("connect: 绑定引用了不存在的 Bot %q", binding.BotName)
		}
	}
	return nil
}

// ------------------------------------------------------------------- helpers
