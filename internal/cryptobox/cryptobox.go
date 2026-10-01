// Package cryptobox seals the secrets that connect.json has to keep.
//
// Background: the relay must present outbound tokens in the clear when it
// dials, so unlike Bot/account tokens (hash-only) those values cannot be
// hashed. They also must not sit in plaintext next to the data plane.
//
// The key lives in a separate file (<path>.key, 0600, generated on first use),
// so copying connect.json alone does not hand over the credentials and a
// password manager can hold just the key. This is obfuscation against casual
// reads of a backup or a support bundle, not protection against an attacker
// who already has the whole data directory - the documentation says so.
package cryptobox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	// markerPrefix identifies a sealed value inside connect.json, so a value can
	// be recognised (and a hand-written plaintext one accepted) unambiguously.
	markerPrefix = "enc:v1:"
	keyBytes     = 32
)

// ErrNoKey reports an encrypted value that cannot be opened because no key file
// is available. Callers turn this into an operator-facing message.
var ErrNoKey = errors.New("cryptobox: 缺少本地密钥文件，无法解密 connect.json 中的密钥")

// Box seals and opens values with one local key.
type Box struct {
	key     []byte
	keyPath string
}

// LoadOrCreateKey reads the key file, generating it when missing.
func LoadOrCreateKey(path string) ([]byte, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("cryptobox: 密钥路径为空")
	}
	raw, err := os.ReadFile(path)
	if err == nil {
		key, decodeErr := decodeKey(string(raw))
		if decodeErr != nil {
			return nil, fmt.Errorf("cryptobox: 密钥文件 %s 无法解析: %w", path, decodeErr)
		}
		return key, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("cryptobox: 读取密钥文件 %s: %w", path, err)
	}

	key := make([]byte, keyBytes)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("cryptobox: 生成密钥: %w", err)
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("cryptobox: 创建密钥目录: %w", err)
		}
	}
	body := "# OnebotNoa 本地密钥：用于加解密 connect.json 中的 token。\n" +
		"# 请与 connect.json 分开备份；丢失后已加密的 token 无法恢复。\n" +
		hex.EncodeToString(key) + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		return nil, fmt.Errorf("cryptobox: 写入密钥文件 %s: %w", path, err)
	}
	return key, nil
}

// decodeKey accepts the key file body: comments and blank lines are ignored and
// the first remaining line must be 64 hex characters.
func decodeKey(body string) ([]byte, error) {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, err := hex.DecodeString(line)
		if err != nil {
			return nil, fmt.Errorf("密钥不是合法的 hex: %w", err)
		}
		if len(key) != keyBytes {
			return nil, fmt.Errorf("密钥长度 %d 字节，需要 %d", len(key), keyBytes)
		}
		return key, nil
	}
	return nil, errors.New("密钥文件里没有密钥行")
}

// NewBox wraps an existing raw key.
func NewBox(key []byte) (*Box, error) {
	if len(key) != keyBytes {
		return nil, fmt.Errorf("cryptobox: 密钥长度 %d 字节，需要 %d", len(key), keyBytes)
	}
	return &Box{key: key}, nil
}

// KeyPath returns the file the key came from (for operator messages).
func (b *Box) KeyPath() string { return b.keyPath }

// SetKeyPath records where the key was loaded from.
func (b *Box) SetKeyPath(path string) { b.keyPath = path }

// Seal encrypts value and returns a self-describing marker string. An empty
// value stays empty so connect.json does not grow noise.
func (b *Box) Seal(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if b == nil || len(b.key) == 0 {
		return "", ErrNoKey
	}
	block, err := aes.NewCipher(b.key)
	if err != nil {
		return "", fmt.Errorf("cryptobox: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("cryptobox: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("cryptobox: 生成 nonce: %w", err)
	}
	sealed := gcm.Seal(nonce, nonce, []byte(value), nil)
	return markerPrefix + base64.RawStdEncoding.EncodeToString(sealed), nil
}

// Open decrypts a value produced by Seal. Values that carry no marker are
// returned unchanged, so a hand-written connect.json keeps working.
func (b *Box) Open(value string) (string, error) {
	if value == "" || !IsSealed(value) {
		return value, nil
	}
	if b == nil || len(b.key) == 0 {
		return "", ErrNoKey
	}
	payload, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(value, markerPrefix))
	if err != nil {
		return "", fmt.Errorf("cryptobox: 密文不是合法的 base64: %w", err)
	}
	block, err := aes.NewCipher(b.key)
	if err != nil {
		return "", fmt.Errorf("cryptobox: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("cryptobox: %w", err)
	}
	if len(payload) < gcm.NonceSize() {
		return "", errors.New("cryptobox: 密文长度不足")
	}
	nonce, ciphertext := payload[:gcm.NonceSize()], payload[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("cryptobox: 解密失败（密钥与写入时不一致？）: %w", err)
	}
	return string(plain), nil
}

// IsSealed reports whether a stored value is an encrypted marker.
func IsSealed(value string) bool { return strings.HasPrefix(value, markerPrefix) }

// Fingerprint returns a stable non-secret hint for a token, used to tell two
// different tokens apart without storing either.
func Fingerprint(value string) string {
	if value == "" {
		return ""
	}
	sum := hashString(value)
	if len(sum) > 12 {
		return sum[:12]
	}
	return sum
}
