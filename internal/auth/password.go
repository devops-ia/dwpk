package auth

import (
	"crypto/rand"
	"fmt"
	"math/big"
)

// passwordAlphabet omits characters that are easy to confuse when a password
// is read off a terminal and typed into a login form: 0/O, 1/l/I, and the
// symbols shells would make the operator quote.
const passwordAlphabet = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// generatedPasswordLength gives ~116 bits of entropy over the alphabet above,
// which is well past anything bcrypt would need to defend against offline.
const generatedPasswordLength = 20

// GeneratePassword returns a random password for the bootstrap admin account.
//
// Unlike an API token this value is meant to be read by a person and typed
// once, so it trades the token's URL-safe base64 for an unambiguous alphabet.
// It is hashed with bcrypt before storage, exactly like an operator-chosen
// password.
func GeneratePassword() (string, error) {
	limit := big.NewInt(int64(len(passwordAlphabet)))
	out := make([]byte, generatedPasswordLength)
	for i := range out {
		// crypto/rand.Int rather than reducing a random byte modulo the
		// alphabet length: the alphabet does not divide 256 evenly, so the
		// modulo would quietly bias the first few characters.
		n, err := rand.Int(rand.Reader, limit)
		if err != nil {
			return "", fmt.Errorf("generate password: %w", err)
		}
		out[i] = passwordAlphabet[n.Int64()]
	}
	return string(out), nil
}
