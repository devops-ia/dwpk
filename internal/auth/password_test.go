package auth

import (
	"strings"
	"testing"
)

func TestGeneratePassword(t *testing.T) {
	t.Parallel()

	seen := make(map[string]struct{}, 64)
	for range 64 {
		password, err := GeneratePassword()
		if err != nil {
			t.Fatalf("GeneratePassword() error = %v", err)
		}
		if len(password) != generatedPasswordLength {
			t.Fatalf("length = %d, want %d", len(password), generatedPasswordLength)
		}
		// Ambiguous characters are the whole point of the custom alphabet: a
		// password nobody can read off a terminal is a support ticket.
		if strings.ContainsAny(password, "0O1lI") {
			t.Fatalf("password %q contains an ambiguous character", password)
		}
		for _, r := range password {
			if !strings.ContainsRune(passwordAlphabet, r) {
				t.Fatalf("password %q contains %q, outside the alphabet", password, r)
			}
		}
		if _, duplicate := seen[password]; duplicate {
			t.Fatalf("GeneratePassword() returned %q twice", password)
		}
		seen[password] = struct{}{}
	}
}

// A generated password must survive the same hash/verify round trip an
// operator-chosen one does.
func TestGeneratePasswordHashRoundTrip(t *testing.T) {
	t.Parallel()

	password, err := GeneratePassword()
	if err != nil {
		t.Fatalf("GeneratePassword() error = %v", err)
	}
	hash, err := HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	if err := VerifyPassword(hash, password); err != nil {
		t.Fatalf("VerifyPassword() error = %v", err)
	}
	if err := VerifyPassword(hash, password+"x"); err == nil {
		t.Fatal("VerifyPassword() accepted a wrong password")
	}
}
