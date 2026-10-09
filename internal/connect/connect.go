// Package connect manages the generated connect.json document: a portable vault
// of data-plane tokens encrypted with a sibling key file (AES-256-GCM).
package connect

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// Doc is the on-disk connect document.
type Doc struct {
	Version int               `json:"version"`
	Tokens  map[string]string `json:"tokens"`
}

// Vault stores and retrieves encrypted tokens by key.
type Vault struct {
	mu      sync.Mutex
	path    string
	keyPath string
	key     []byte
	doc     Doc
}

// Open loads (or initialises) the vault at path, using keyPath for the key.
func Open(path, keyPath string) (*Vault, error) {
	v := &Vault{path: path, keyPath: keyPath, doc: Doc{Version: 1, Tokens: map[string]string{}}}
	if err := v.loadKey(); err != nil {
		return nil, err
	}
	if data, err := os.ReadFile(path); err == nil {
		var doc Doc
		if err := json.Unmarshal(data, &doc); err != nil {
			return nil, err
		}
		if doc.Tokens == nil {
			doc.Tokens = map[string]string{}
		}
		v.doc = doc
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	return v, nil
}

func (v *Vault) loadKey() error {
	data, err := os.ReadFile(v.keyPath)
	if err == nil && len(data) == 32 {
		v.key = data
		return nil
	}
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(v.keyPath), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(v.keyPath, key, 0o600); err != nil {
		return err
	}
	v.key = key
	return nil
}

func (v *Vault) seal(plain string) (string, error) {
	block, err := aes.NewCipher(v.key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := gcm.Seal(nonce, nonce, []byte(plain), nil)
	return base64.RawStdEncoding.EncodeToString(out), nil
}

func (v *Vault) open(enc string) (string, error) {
	raw, err := base64.RawStdEncoding.DecodeString(enc)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(v.key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	ns := gcm.NonceSize()
	if len(raw) < ns {
		return "", os.ErrInvalid
	}
	plain, err := gcm.Open(nil, raw[:ns], raw[ns:], nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

// Get returns the plaintext token for key.
func (v *Vault) Get(key string) (string, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	enc, ok := v.doc.Tokens[key]
	if !ok {
		return "", false
	}
	plain, err := v.open(enc)
	if err != nil {
		return "", false
	}
	return plain, true
}

// Set stores (encrypted) the plaintext token for key and persists the document.
func (v *Vault) Set(key, token string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	enc, err := v.seal(token)
	if err != nil {
		return err
	}
	v.doc.Tokens[key] = enc
	return v.persistLocked()
}

// Delete removes a token and persists the document.
func (v *Vault) Delete(key string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	delete(v.doc.Tokens, key)
	return v.persistLocked()
}

// Plaintext returns a copy of every token decrypted, keyed by vault key.
func (v *Vault) Plaintext() map[string]string {
	v.mu.Lock()
	defer v.mu.Unlock()
	out := make(map[string]string, len(v.doc.Tokens))
	for k, enc := range v.doc.Tokens {
		if plain, err := v.open(enc); err == nil {
			out[k] = plain
		}
	}
	return out
}

func (v *Vault) persistLocked() error {
	if err := os.MkdirAll(filepath.Dir(v.path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(v.doc, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(v.path, data, 0o600)
}
