package ui

import "testing"

// The quota comparison, pinned.
//
// This exists because the boundary was reported as broken and is not: with one
// workspace and a limit of two, another is allowed. The tempting "fix" is to
// loosen the comparison to <=, which would let everybody create one workspace
// more than their quota - a bug that looks like a fix and that nothing else
// would catch, since the webhook and the ResourceQuota would then disagree by
// exactly one.
//
// The form and the webhook must answer identically. The webhook admits while
// `count < limit`; this must therefore block on `count >= limit` and nowhere
// else.
func TestWorkspaceLimitMatchesTheWebhookBoundary(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		count   int
		limit   int32
		blocked bool
	}{
		{"none used", 0, 2, false},
		{"one of two is the reported case", 1, 2, false},
		{"at the limit", 2, 2, true},
		{"over the limit, which a lowered quota produces", 3, 2, true},
		{"a limit of one is reached immediately", 1, 1, true},
		// A UserSpace whose quota has never been written reports zero. Blocking
		// would lock out the very people who are still being provisioned; the
		// webhook is what refuses if it truly is zero.
		{"no limit set", 1, 0, false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			data := CreateData{WorkspaceCount: test.count, WorkspaceLimit: test.limit}
			if got := data.AtWorkspaceLimit(); got != test.blocked {
				t.Errorf("AtWorkspaceLimit() with %d of %d = %t, want %t",
					test.count, test.limit, got, test.blocked)
			}
		})
	}
}
