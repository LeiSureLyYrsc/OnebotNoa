package auth

import "testing"

func TestHashVerify(t *testing.T) {
	hash, err := HashPassword("s3cret")
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword(hash, "s3cret") {
		t.Fatal("correct password rejected")
	}
	if VerifyPassword(hash, "wrong") {
		t.Fatal("wrong password accepted")
	}
	if VerifyPassword("garbage", "x") {
		t.Fatal("malformed hash accepted")
	}
}

func TestNewToken(t *testing.T) {
	tok, hash, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if tok == "" || hash == "" || hash != HashToken(tok) {
		t.Fatalf("token/hash mismatch: %q %q", tok, hash)
	}
	tok2, _, _ := NewToken()
	if tok == tok2 {
		t.Fatal("tokens should be unique")
	}
}
