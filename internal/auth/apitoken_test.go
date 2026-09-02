package auth

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestGenerateAPIToken(t *testing.T) {
	token, err := GenerateAPIToken()
	if err != nil {
		t.Fatalf("GenerateAPIToken() error = %v", err)
	}
	if !strings.HasPrefix(token, apiTokenPrefix) {
		t.Fatalf("GenerateAPIToken() = %q, want prefix %q", token, apiTokenPrefix)
	}
	raw := strings.TrimPrefix(token, apiTokenPrefix)
	if _, err := uuid.Parse(raw); err != nil {
		t.Fatalf("GenerateAPIToken() suffix = %q, not a valid UUID: %v", raw, err)
	}

	second, err := GenerateAPIToken()
	if err != nil {
		t.Fatalf("GenerateAPIToken() second call error = %v", err)
	}
	if token == second {
		t.Fatalf("GenerateAPIToken() returned the same value twice: %q", token)
	}
}

func TestHashAPIToken(t *testing.T) {
	const token = "dwpk_00000000-0000-0000-0000-000000000000"
	got := HashAPIToken(token)

	if len(got) != 64 {
		t.Fatalf("HashAPIToken(%q) = %q, want 64 hex characters, got length %d", token, got, len(got))
	}
	if got != HashAPIToken(token) {
		t.Fatalf("HashAPIToken(%q) is not deterministic", token)
	}
	if got == HashAPIToken(token+"x") {
		t.Fatalf("HashAPIToken produced the same hash for different inputs")
	}
}

func TestLooksLikeAPIToken(t *testing.T) {
	cases := map[string]bool{
		"dwpk_" + uuid.NewString(): true,
		"dwpk_":                    false,
		"":                         false,
		"sha256~abcdef":            false,
		"dwpk":                     false,
	}
	for input, want := range cases {
		if got := LooksLikeAPIToken(input); got != want {
			t.Errorf("LooksLikeAPIToken(%q) = %v, want %v", input, got, want)
		}
	}
}
