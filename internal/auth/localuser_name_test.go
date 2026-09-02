package auth

import (
	"strings"
	"testing"
)

// The whole point of the change is that an operator can tell whose credential a
// Secret is without opening it, so the ordinary case must stay readable - and
// the awkward cases must still be unique, because two people sharing a Secret
// means two people sharing a password.
func TestLocalUserSecretName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		username string
		want     string
		// hashed marks names where sanitising was lossy, so a suffix is required
		// rather than merely allowed.
		hashed bool
	}{
		{name: "a plain username is used as-is", username: "alice", want: "dwpk-local-user-alice"},
		{name: "dots are legal in a Kubernetes name", username: "a.moreno", want: "dwpk-local-user-a.moreno"},
		{name: "hyphens are legal too", username: "a-moreno", want: "dwpk-local-user-a-moreno"},
		{name: "capitals are lowered, which is not lossy", username: "Alice", want: "dwpk-local-user-alice"},
		{name: "an email keeps its readable half", username: "alice@corp.com", hashed: true},
		{name: "spaces", username: "alice smith", hashed: true},
		{name: "unicode", username: "álice", hashed: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := LocalUserSecretName(test.username)

			if !strings.HasPrefix(got, LocalUserSecretPrefix) {
				t.Fatalf("%q lost the prefix: %q", test.username, got)
			}
			if test.want != "" && got != test.want {
				t.Fatalf("LocalUserSecretName(%q) = %q, want %q", test.username, got, test.want)
			}
			if test.hashed && got == LocalUserSecretPrefix+sanitiseName(test.username) {
				t.Fatalf("%q was sanitised lossily but got no hash suffix: %q", test.username, got)
			}
			assertValidSecretName(t, got)
		})
	}
}

// Two usernames that sanitise to the same string must not become one Secret.
// Without the hash suffix, "Alice@corp" and "alice-corp" both flatten to
// "alice-corp" and the second create would either fail or overwrite the first.
func TestLocalUserSecretNamesDoNotCollide(t *testing.T) {
	t.Parallel()

	colliding := []string{"alice@corp", "alice-corp", "alice.corp", "ALICE@CORP", "alice corp"}

	seen := map[string]string{}
	for _, username := range colliding {
		name := LocalUserSecretName(username)
		if other, clash := seen[name]; clash {
			t.Fatalf("%q and %q both map to %q", other, username, name)
		}
		seen[name] = username
	}
}

// A name Kubernetes will refuse is worse than a generated one: the account
// simply cannot be created.
func assertValidSecretName(t *testing.T, name string) {
	t.Helper()

	if name == "" || len(name) > maxSecretName {
		t.Fatalf("%q is not a usable length", name)
	}
	for _, r := range name {
		valid := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '.'
		if !valid {
			t.Fatalf("%q holds %q, which a Kubernetes name may not", name, r)
		}
	}
	if strings.HasPrefix(name, "-") || strings.HasPrefix(name, ".") ||
		strings.HasSuffix(name, "-") || strings.HasSuffix(name, ".") {
		t.Fatalf("%q does not start and end alphanumeric", name)
	}
}
