package connect

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Errors surfaced by the store.
var (
	ErrNotFound      = errors.New("connect: 未找到")
	ErrDuplicateName = errors.New("connect: 名称已存在")
)

func hintOf(token string) string {
	if token == "" {
		return ""
	}
	if len(token) <= 6 {
		return "…"
	}
	return token[:4] + "…" + token[len(token)-4:]
}

// isSealedMarker mirrors cryptobox.IsSealed without importing it (the connect
// package only needs the shape of a marker).
func isSealedMarker(value string) bool { return strings.HasPrefix(value, "enc:v1:") }

// openToken reveals a stored credential. A value with no marker is returned
// unchanged: it was typed by hand, and passing it to the cipher would fail.
func openToken(box Sealer, stored string) (string, error) {
	if stored == "" {
		return "", nil
	}
	if !isSealedMarker(stored) {
		return stored, nil
	}
	if box == nil {
		return "", errors.New("connect: 没有可用的解密器")
	}
	return box.Open(stored)
}

func (s *Store) sealLocked(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if s.box == nil {
		return "", errors.New("connect: 没有可用的加密器")
	}
	return s.box.Seal(value)
}

func (s *Store) findAccountLocked(selfID string) (Account, bool) {
	for _, account := range s.doc.Accounts {
		if account.SelfID == selfID {
			return account, true
		}
	}
	return Account{}, false
}

func (s *Store) accountByIDLocked(id int64) (Account, bool) {
	for _, account := range s.doc.Accounts {
		if account.ID == id {
			return account, true
		}
	}
	return Account{}, false
}

func (s *Store) findBotLocked(name string) (Bot, bool) {
	for _, bot := range s.doc.Bots {
		if bot.Name == name {
			return bot, true
		}
	}
	return Bot{}, false
}

func (s *Store) botByIDLocked(id int64) (Bot, bool) {
	for _, bot := range s.doc.Bots {
		if bot.ID == id {
			return bot, true
		}
	}
	return Bot{}, false
}

func (s *Store) findConnectionLocked(name string) (Connection, bool) {
	for _, connection := range s.doc.Connections {
		if connection.Name == name {
			return connection, true
		}
	}
	return Connection{}, false
}

func (s *Store) clearDefaultLocked(botName string, keepID int64) {
	for i := range s.doc.Bindings {
		if s.doc.Bindings[i].BotName == botName && s.doc.Bindings[i].ID != keepID {
			s.doc.Bindings[i].IsDefault = false
		}
	}
}

// Ids are assigned from the current maximum so a hand-edited file that already
// carries ids keeps them, and a fresh file starts at 1.
func (s *Store) nextAccountIDLocked() int64 {
	var max int64
	for _, account := range s.doc.Accounts {
		if account.ID > max {
			max = account.ID
		}
	}
	return max + 1
}

func (s *Store) nextBotIDLocked() int64 {
	var max int64
	for _, bot := range s.doc.Bots {
		if bot.ID > max {
			max = bot.ID
		}
	}
	return max + 1
}

func (s *Store) nextConnectionIDLocked() int64 {
	var max int64
	for _, connection := range s.doc.Connections {
		if connection.ID > max {
			max = connection.ID
		}
	}
	return max + 1
}

func (s *Store) nextBindingIDLocked() int64 {
	var max int64
	for _, binding := range s.doc.Bindings {
		if binding.ID > max {
			max = binding.ID
		}
	}
	return max + 1
}

// saveLocked writes the document atomically: a temporary file in the same
// directory, then a rename. A crash mid-write therefore never truncates the
// only copy of the connection set.
func (s *Store) saveLocked() error {
	if s.path == "" {
		return errors.New("connect: 未配置文件路径")
	}
	s.doc.SchemaVersion = SchemaVersion
	s.doc.GeneratedBy = "OnebotNoa"
	s.doc.UpdatedAt = time.Now().Format(time.RFC3339)

	body, err := json.MarshalIndent(s.doc, "", "  ")
	if err != nil {
		return fmt.Errorf("connect: 序列化: %w", err)
	}
	body = append(body, '\n')

	dir := filepath.Dir(s.path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("connect: 创建目录 %s: %w", dir, err)
		}
	}
	tmp, err := os.CreateTemp(dir, ".connect-*.json")
	if err != nil {
		return fmt.Errorf("connect: 创建临时文件: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("connect: 写入临时文件: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("connect: 设置权限: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("connect: 关闭临时文件: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("connect: 替换 %s: %w", s.path, err)
	}
	s.lastErr = nil
	return nil
}

// normaliseAccounts fills in defaults for values a hand-written file omitted.
func normaliseAccounts(in []Account) []Account {
	out := make([]Account, 0, len(in))
	var next int64 = 1
	seen := map[int64]bool{}
	for _, account := range in {
		account.SelfID = strings.TrimSpace(account.SelfID)
		if account.SelfID == "" {
			continue
		}
		if account.ID <= 0 || seen[account.ID] {
			for seen[next] {
				next++
			}
			account.ID = next
		}
		seen[account.ID] = true
		if account.ID >= next {
			next = account.ID + 1
		}
		account.Tags = append([]string(nil), account.Tags...)
		out = append(out, account)
	}
	return out
}

func normaliseBots(in []Bot) []Bot {
	out := make([]Bot, 0, len(in))
	var next int64 = 1
	seen := map[int64]bool{}
	names := map[string]bool{}
	for _, bot := range in {
		bot.Name = strings.TrimSpace(bot.Name)
		if bot.Name == "" || names[bot.Name] {
			continue
		}
		names[bot.Name] = true
		if bot.ID <= 0 || seen[bot.ID] {
			for seen[next] {
				next++
			}
			bot.ID = next
		}
		seen[bot.ID] = true
		if bot.ID >= next {
			next = bot.ID + 1
		}
		out = append(out, bot)
	}
	return out
}

func normaliseConnections(in []Connection) []Connection {
	out := make([]Connection, 0, len(in))
	var next int64 = 1
	seen := map[int64]bool{}
	names := map[string]bool{}
	for _, connection := range in {
		connection.Name = strings.TrimSpace(connection.Name)
		if connection.Name == "" || names[connection.Name] {
			continue
		}
		names[connection.Name] = true
		if connection.ID <= 0 || seen[connection.ID] {
			for seen[next] {
				next++
			}
			connection.ID = next
		}
		seen[connection.ID] = true
		if connection.ID >= next {
			next = connection.ID + 1
		}
		if connection.Mode == "" {
			connection.Mode = "universal"
		}
		out = append(out, connection)
	}
	return out
}

func normaliseBindings(in []Binding) []Binding {
	out := make([]Binding, 0, len(in))
	var next int64 = 1
	seen := map[int64]bool{}
	pairs := map[string]bool{}
	for _, binding := range in {
		binding.BotName = strings.TrimSpace(binding.BotName)
		binding.AccountSelfID = strings.TrimSpace(binding.AccountSelfID)
		if binding.BotName == "" || binding.AccountSelfID == "" {
			continue
		}
		key := binding.BotName + "|" + binding.AccountSelfID
		if pairs[key] {
			continue
		}
		pairs[key] = true
		if binding.ID <= 0 || seen[binding.ID] {
			for seen[next] {
				next++
			}
			binding.ID = next
		}
		seen[binding.ID] = true
		if binding.ID >= next {
			next = binding.ID + 1
		}
		if binding.Priority == 0 {
			binding.Priority = 100
		}
		out = append(out, binding)
	}
	return out
}
