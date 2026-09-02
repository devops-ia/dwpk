/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// LocalUserSecretPrefix is what every local-user Secret is named after.
const LocalUserSecretPrefix = "dwpk-local-user-"

// maxSecretName is the Kubernetes limit for an object name (RFC 1123 subdomain).
const maxSecretName = 253

// LocalUserSecretName derives a readable Secret name from a username.
//
// The names used to be generated - `dwpk-local-user-jrnhg` - which is unique
// and tells an operator nothing. `dwpk-local-user-alice` says whose credential
// it is without having to read the Secret to find out.
//
// A username is not a Kubernetes name: it may hold `@`, capitals, or anything
// else a person types. Characters outside [a-z0-9-.] become `-`, and a short
// hash of the original is appended whenever sanitising changed anything, so
// `Alice@corp` and `alice-corp` cannot collide onto one Secret and silently
// share a password.
func LocalUserSecretName(username string) string {
	sanitised := sanitiseName(username)
	name := LocalUserSecretPrefix + sanitised

	if sanitised != strings.ToLower(username) || sanitised == "" {
		// The sanitised form is lossy, so it is no longer a unique key on its
		// own. The hash restores that without making the common case ugly.
		sum := sha256.Sum256([]byte(username))
		name += "-" + hex.EncodeToString(sum[:])[:8]
	}
	if len(name) > maxSecretName {
		name = name[:maxSecretName]
	}
	return strings.Trim(name, "-.")
}

// sanitiseName lowercases and replaces anything a Kubernetes name may not hold.
// Leading and trailing separators go too: a name must start and end
// alphanumeric.
func sanitiseName(raw string) string {
	var out strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(raw)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '.':
			out.WriteRune(r)
		default:
			out.WriteRune('-')
		}
	}
	return strings.Trim(out.String(), "-.")
}
