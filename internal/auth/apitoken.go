package auth

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/google/uuid"
)

// apiTokenPrefix marks a string as a dwpk API token so a Bearer header can be
// told apart from a raw Kubernetes ServiceAccount token at a glance, the same
// way GitHub ("ghp_") and GitLab ("glpat-") prefix their personal access
// tokens.
const apiTokenPrefix = "dwpk_"

// TokenKind distinguishes the one bootstrap admin token from the
// self-service tokens users mint for their own identity.
type TokenKind string

const (
	TokenKindAdmin       TokenKind = "admin"
	TokenKindApplication TokenKind = "application"
)

// GenerateAPIToken returns a new plaintext API token. The caller sees this
// value exactly once; only its hash is ever persisted.
func GenerateAPIToken() (string, error) {
	id, err := uuid.NewRandom()
	if err != nil {
		return "", err
	}
	return apiTokenPrefix + id.String(), nil
}

// HashAPIToken returns the SHA-256 hex digest of a plaintext API token.
//
// SHA-256 rather than a slow password hash (bcrypt/scrypt) is deliberate:
// the token is already a 122-bit random UUIDv4, not a user-chosen secret, so
// there is no offline brute-force risk to slow down, and a fast hash keeps
// every authenticated API call cheap. GitHub and GitLab hash PATs the same
// way for the same reason.
func HashAPIToken(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

// LooksLikeAPIToken reports whether a bearer value has the dwpk API token
// shape, so callers can decide whether to look it up in the TokenStore
// before falling back to another auth mode.
func LooksLikeAPIToken(bearer string) bool {
	return len(bearer) > len(apiTokenPrefix) && bearer[:len(apiTokenPrefix)] == apiTokenPrefix
}
