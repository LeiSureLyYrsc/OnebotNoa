package cryptobox

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLoadOrCreateKeyGeneratesOnceAndReloads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "connect.json.key")
	key, err := LoadOrCreateKey(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if len(key) != keyBytes {
		t.Fatalf("key length = %d, want %d", len(key), keyBytes)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// The key protects the connection file, so it asks for owner-only access.
	// Windows has no POSIX permission bits (it reports 0666 for any created
	// file), so the check is only meaningful where the bits are real.
	if runtime.GOOS != "windows" {
		if mode := info.Mode().Perm(); mode != 0o600 {
			t.Fatalf("key mode = %o, want 600", mode)
		}
	}

	reloaded, err := LoadOrCreateKey(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if string(reloaded) != string(key) {
		t.Fatal("reloading must return the same key, not a fresh one")
	}
}

func TestSealOpenRoundTrip(t *testing.T) {
	key, err := LoadOrCreateKey(filepath.Join(t.TempDir(), "k"))
	if err != nil {
		t.Fatal(err)
	}
	box, err := NewBox(key)
	if err != nil {
		t.Fatal(err)
	}

	for _, secret := range []string{"short", "a-very-long-token-with-中文-and-symbols-!@#$%^&*()", "  spaces  "} {
		sealed, err := box.Seal(secret)
		if err != nil {
			t.Fatalf("seal %q: %v", secret, err)
		}
		if !IsSealed(sealed) {
			t.Fatalf("sealed value lacks the marker: %q", sealed)
		}
		if strings.Contains(sealed, secret) {
			t.Fatalf("the ciphertext exposes the plaintext: %q", sealed)
		}
		plain, err := box.Open(sealed)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		if plain != secret {
			t.Fatalf("round trip = %q, want %q", plain, secret)
		}
	}

	// An empty value stays empty so the document does not grow noise.
	sealed, err := box.Seal("")
	if err != nil || sealed != "" {
		t.Fatalf("sealing an empty value = %q, %v", sealed, err)
	}

	// A hand-written plaintext value is returned as-is, not decrypted.
	plain, err := box.Open("typed-by-hand")
	if err != nil || plain != "typed-by-hand" {
		t.Fatalf("a plaintext value must pass through: %q %v", plain, err)
	}
}

// TestSealingIsNonDeterministic guards against a fixed nonce, which would make
// two identical tokens produce identical ciphertext (and leak that they match).
func TestSealingIsNonDeterministic(t *testing.T) {
	key, _ := LoadOrCreateKey(filepath.Join(t.TempDir(), "k"))
	box, _ := NewBox(key)
	first, _ := box.Seal("same-token")
	second, _ := box.Seal("same-token")
	if first == second {
		t.Fatal("sealing the same value twice must not produce identical ciphertext")
	}
	both, _ := box.Open(first)
	other, _ := box.Open(second)
	if both != "same-token" || other != "same-token" {
		t.Fatal("both ciphertexts must decrypt to the original")
	}
}

// TestWrongKeyFailsLoudly is the important one: a lost or replaced key must be
// reported, never silently treated as "no token".
func TestWrongKeyFailsLoudly(t *testing.T) {
	one, _ := LoadOrCreateKey(filepath.Join(t.TempDir(), "k1"))
	two, _ := LoadOrCreateKey(filepath.Join(t.TempDir(), "k2"))
	box1, _ := NewBox(one)
	box2, _ := NewBox(two)
	sealed, _ := box1.Seal("secret")
	if _, err := box2.Open(sealed); err == nil {
		t.Fatal("opening with the wrong key must fail, not return garbage")
	}

	// Tampering with the ciphertext must be detected (GCM authenticates).
	if _, err := box1.Open(sealed + "AAAA"); err == nil {
		t.Fatal("a tampered ciphertext must be rejected")
	}
}

func TestKeyFileAcceptsComments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "k")
	body := "# a comment\n\n" + "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff" + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	key, err := LoadOrCreateKey(path)
	if err != nil {
		t.Fatalf("a commented key file must load: %v", err)
	}
	if len(key) != keyBytes {
		t.Fatalf("key length = %d", len(key))
	}
}

func TestBrokenKeyFileIsRejected(t *testing.T) {
	for name, body := range map[string]string{
		"not hex":     "zzzz\n",
		"wrong size":  "0011\n",
		"no key line": "# only a comment\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "k")
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadOrCreateKey(path); err == nil {
				t.Fatalf("%s: expected the key file to be rejected", name)
			}
		})
	}
}
