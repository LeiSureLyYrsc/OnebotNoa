package cryptobox

import (
	"crypto/sha256"
	"encoding/hex"
)

// hashString is the hex SHA-256 used for fingerprints. It is kept private so
// callers use the semantic Fingerprint helper instead of hashing directly.
func hashString(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
