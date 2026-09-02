package auth

import (
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func TestHashAndVerifyPassword(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	if hash == "" || strings.Contains(hash, "correct horse") {
		t.Fatalf("HashPassword() = %q, must not contain the plaintext", hash)
	}

	if err := VerifyPassword(hash, "correct horse battery staple"); err != nil {
		t.Fatalf("VerifyPassword() with the correct password error = %v, want nil", err)
	}
	if err := VerifyPassword(hash, "wrong password"); err != ErrInvalidCredentials {
		t.Fatalf("VerifyPassword() with the wrong password error = %v, want ErrInvalidCredentials", err)
	}
}

func TestHashPasswordRejectsEmpty(t *testing.T) {
	if _, err := HashPassword(""); err != ErrEmptyPassword {
		t.Fatalf("HashPassword(\"\") error = %v, want ErrEmptyPassword", err)
	}
}

func TestHashPasswordIsSalted(t *testing.T) {
	first, err := HashPassword("same-password")
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	second, err := HashPassword("same-password")
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	if first == second {
		t.Fatal("HashPassword() produced identical hashes for two calls with the same password; bcrypt should salt each call")
	}
}

func TestVerifyAgainstDummyHashAlwaysFails(t *testing.T) {
	if err := VerifyAgainstDummyHash("anything at all"); err != ErrInvalidCredentials {
		t.Fatalf("VerifyAgainstDummyHash() error = %v, want ErrInvalidCredentials", err)
	}
	if err := VerifyAgainstDummyHash(""); err != ErrInvalidCredentials {
		t.Fatalf("VerifyAgainstDummyHash(\"\") error = %v, want ErrInvalidCredentials", err)
	}
}

// The dummy hash exists so a login lookup that fails to find an account costs
// the same as one that finds an account and gets the password wrong. A dummy
// hash at the wrong bcrypt cost - or one bcrypt rejects outright and returns
// from immediately - would make the two paths distinguishable by timing again.
func TestVerifyAgainstDummyHashCostsAsMuchAsARealComparison(t *testing.T) {
	realHash, err := HashPassword("some real password")
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}

	const attempts = 5
	measure := func(f func()) time.Duration {
		start := time.Now()
		for range attempts {
			f()
		}
		return time.Since(start)
	}

	real := measure(func() { _ = VerifyPassword(realHash, "wrong password") })
	dummy := measure(func() { _ = VerifyAgainstDummyHash("wrong password") })

	// Both hashes are bcrypt at the same cost, so this is a sanity check on
	// the constant, not a timing assertion sensitive to machine noise: an
	// order-of-magnitude gap would mean the dummy hash is cheaper to compare
	// against (wrong cost, or malformed and rejected before hashing), which is
	// exactly the difference this function exists to eliminate.
	if dummy < real/10 || dummy > real*10 {
		t.Fatalf("dummy hash comparison took %v, real comparison took %v - costs are not comparable", dummy, real)
	}
}

func TestDummyPasswordHashIsWellFormedAtDefaultCost(t *testing.T) {
	cost, err := bcrypt.Cost([]byte(dummyPasswordHash))
	if err != nil {
		t.Fatalf("dummyPasswordHash is not a valid bcrypt hash: %v", err)
	}
	if cost != bcrypt.DefaultCost {
		t.Fatalf("dummyPasswordHash cost = %d, want bcrypt.DefaultCost (%d)", cost, bcrypt.DefaultCost)
	}
}
