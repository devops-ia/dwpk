// Package auth's local user support lets a demo/dev deployment log in with
// a username and password instead of an OAuth2 provider (§7.8). It is a
// deliberately small addition: unlike OAuth2 tokens or API tokens, a local
// password is user-chosen and low-entropy, so it is hashed with bcrypt
// (an intentionally slow, salted hash) rather than the fast SHA-256 used
// for high-entropy API tokens in apitoken.go.
package auth

import (
	"errors"

	"golang.org/x/crypto/bcrypt"
)

var (
	ErrInvalidCredentials = errors.New("invalid username or password")
	ErrEmptyUsername      = errors.New("username must not be empty")
	ErrEmptyPassword      = errors.New("password must not be empty")
)

// HashPassword returns a bcrypt hash of a plaintext password, for storage in
// a local user Secret. It never returns the plaintext.
func HashPassword(plaintext string) (string, error) {
	if plaintext == "" {
		return "", ErrEmptyPassword
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(plaintext), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

// VerifyPassword reports whether a plaintext password matches a bcrypt hash
// previously produced by HashPassword.
func VerifyPassword(hash, plaintext string) error {
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(plaintext)); err != nil {
		return ErrInvalidCredentials
	}
	return nil
}

// dummyPasswordHash is a bcrypt hash of no password anyone will ever type. It
// exists only to be compared against, at the same DefaultCost as a real
// account's hash, so a login attempt for a username that does not exist
// costs the same wall-clock time as one for a username that does. See
// VerifyAgainstDummyHash.
const dummyPasswordHash = "$2a$10$C6UzMDM.H6dfI/f/IKcEeO0.j.h.aBqm4xQ4Y5f6.1nMbT9Ge/ILW"

// VerifyAgainstDummyHash runs the same bcrypt comparison VerifyPassword does,
// against a fixed hash instead of a real account's, and always fails.
//
// Call this on the not-found path of a login lookup. Returning
// ErrInvalidCredentials immediately when a login does not exist is faster
// than the bcrypt comparison a real, wrong-password attempt takes - and that
// timing difference is itself an oracle for which logins exist, even though
// the error message is identical either way. Paying the same bcrypt cost on
// both paths removes the signal.
func VerifyAgainstDummyHash(plaintext string) error {
	_ = bcrypt.CompareHashAndPassword([]byte(dummyPasswordHash), []byte(plaintext))
	return ErrInvalidCredentials
}
