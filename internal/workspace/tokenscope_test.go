package workspace

import "testing"

// The scope decides which ServiceAccount a token authenticates as, and that
// account's RBAC is the only thing enforcing it. So this mapping is the whole
// security boundary for scoped tokens: get it wrong and a read token writes.
func TestServiceAccountForScope(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		scope TokenScope
		want  string
	}{
		{name: "read", scope: TokenScopeRead, want: ReadOnlySessionServiceAccountName},
		{name: "full", scope: TokenScopeFull, want: SessionServiceAccountName},
		{
			// An unset or misspelled scope must land on the account that can do
			// less. Failing open here would hand out a full token to anyone who
			// typo'd the field.
			name:  "unset falls back to read-only",
			scope: "",
			want:  ReadOnlySessionServiceAccountName,
		},
		{
			name:  "nonsense falls back to read-only",
			scope: "administrator",
			want:  ReadOnlySessionServiceAccountName,
		},
		{
			// Notably not a special case: "admin" is not a scope. A token cannot
			// exceed its issuer's role, so for an administrator "full" already is
			// an admin token, and for anyone else there is nothing to grant.
			name:  "admin is not a scope",
			scope: "admin",
			want:  ReadOnlySessionServiceAccountName,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := ServiceAccountForScope(test.scope); got != test.want {
				t.Fatalf("ServiceAccountForScope(%q) = %q, want %q", test.scope, got, test.want)
			}
		})
	}
}

// The two accounts must never be the same string, or "read only" silently means
// "full".
func TestSessionAccountsAreDistinct(t *testing.T) {
	t.Parallel()
	if SessionServiceAccountName == ReadOnlySessionServiceAccountName {
		t.Fatal("the read-only session account is the session account")
	}
	if ServiceAccountName == SessionServiceAccountName || ServiceAccountName == ReadOnlySessionServiceAccountName {
		t.Fatal("a workspace pod shares an identity with a browser session or an API token")
	}
}
