package auth

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Password hashing uses PBKDF2-HMAC-SHA256 from the standard library (Go 1.24+),
// so the hub needs no third-party crypto dependency.
const (
	passwordScheme     = "pbkdf2-sha256"
	passwordIterations = 120_000
	passwordKeyLength  = 32
	passwordSaltLength = 16
)

// ErrInvalidHash reports a stored hash that cannot be parsed.
var ErrInvalidHash = errors.New("auth: invalid password hash")

// HashPassword derives a storable hash of the form
// "pbkdf2-sha256$<iterations>$<salt-b64>$<key-b64>".
func HashPassword(plain string) (string, error) {
	salt := make([]byte, passwordSaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("auth: read salt: %w", err)
	}
	key, err := pbkdf2.Key(sha256.New, plain, salt, passwordIterations, passwordKeyLength)
	if err != nil {
		return "", fmt.Errorf("auth: derive key: %w", err)
	}
	enc := base64.RawStdEncoding
	return strings.Join([]string{
		passwordScheme,
		strconv.Itoa(passwordIterations),
		enc.EncodeToString(salt),
		enc.EncodeToString(key),
	}, "$"), nil
}

// VerifyPassword reports whether plain matches the stored hash. Comparison is
// constant time.
func VerifyPassword(encoded, plain string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != passwordScheme {
		return false
	}
	iterations, err := strconv.Atoi(parts[1])
	if err != nil || iterations <= 0 {
		return false
	}
	enc := base64.RawStdEncoding
	salt, err := enc.DecodeString(parts[2])
	if err != nil {
		return false
	}
	want, err := enc.DecodeString(parts[3])
	if err != nil || len(want) == 0 {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, plain, salt, iterations, len(want))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(got, want) == 1
}

// NeedsRehash reports whether a stored hash uses outdated parameters.
func NeedsRehash(encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != passwordScheme {
		return true
	}
	iterations, err := strconv.Atoi(parts[1])
	return err != nil || iterations != passwordIterations
}
