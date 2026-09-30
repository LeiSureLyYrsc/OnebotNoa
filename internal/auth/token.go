package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
)

// Tokens (session cookies, Bot tokens, per-account tokens) are high-entropy
// random strings. Only their SHA-256 is stored, which keeps lookup O(1) while a
// database leak still does not expose usable secrets. The plaintext never
// reaches the database.
const tokenBytes = 32

// NewToken returns a fresh random token together with the hash to store.
func NewToken() (plain, hash string, err error) {
	buf := make([]byte, tokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", "", err
	}
	plain = hex.EncodeToString(buf)
	return plain, HashToken(plain), nil
}

// HashToken returns the storable hash of a token.
func HashToken(plain string) string {
	sum := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(sum[:])
}

// TokenMatches reports whether plain hashes to storedHash (constant time).
func TokenMatches(plain, storedHash string) bool {
	if storedHash == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(HashToken(plain)), []byte(storedHash)) == 1
}

// NewSecret returns a short random secret for display-once values such as the
// initial administrator password.
func NewSecret() string {
	return rand.Text()
}

// NewCSRFToken returns a fresh CSRF token.
func NewCSRFToken() string {
	return rand.Text()
}
