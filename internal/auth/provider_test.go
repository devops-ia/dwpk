package auth

import (
	"context"
	"testing"

	"golang.org/x/oauth2"
)

type stubProvider struct{}

func (stubProvider) AuthURL(state, nonce, _ string) string {
	return state + ":" + nonce
}

func (stubProvider) Exchange(ctx context.Context, code, _ string) (*oauth2.Token, error) {
	return &oauth2.Token{AccessToken: code}, nil
}

func (stubProvider) Claims(ctx context.Context, tok *oauth2.Token) (Claims, error) {
	return Claims{Email: tok.AccessToken}, nil
}

func TestNameValid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value Name
		want  bool
	}{
		{name: "Entra ID", value: ProviderEntraID, want: true},
		{name: "Google", value: ProviderGoogle, want: true},
		{name: "GitLab", value: ProviderGitLab, want: true},
		{name: "Keycloak", value: ProviderKeycloak, want: true},
		{name: "GitHub", value: ProviderGitHub, want: true},
		{name: "Unknown", value: Name("unknown"), want: false},
		{name: "Empty", value: Name(""), want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := tt.value.Valid(); got != tt.want {
				t.Fatalf("Name.Valid() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestProviderInterfaceShape(t *testing.T) {
	t.Parallel()

	var provider Provider = stubProvider{}

	if got := provider.AuthURL("state", "nonce", "verifier"); got != "state:nonce" {
		t.Fatalf("AuthURL() = %q, want %q", got, "state:nonce")
	}

	tok, err := provider.Exchange(context.Background(), "code", "verifier")
	if err != nil {
		t.Fatalf("Exchange() error = %v", err)
	}
	if tok.AccessToken != "code" {
		t.Fatalf("Exchange() access token = %q, want %q", tok.AccessToken, "code")
	}

	claims, err := provider.Claims(context.Background(), tok)
	if err != nil {
		t.Fatalf("Claims() error = %v", err)
	}
	if claims.Email != "code" {
		t.Fatalf("Claims() email = %q, want %q", claims.Email, "code")
	}
}
