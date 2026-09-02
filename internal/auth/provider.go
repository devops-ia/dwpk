package auth

import (
	"context"

	"golang.org/x/oauth2"
)

// Claims stays provider-neutral so each provider can hide its own token shape.
type Claims struct {
	Email string
	// Groups is the identity's group memberships, when the provider's ID
	// token carries a groups claim (Entra ID, Keycloak and GitLab all can).
	// GitHub has no equivalent claim - it maps org/team membership through a
	// separate API instead - so GitHub logins always leave this empty.
	Groups []string
}

// Provider keeps OAuth2/OIDC exchange separate from transport and Kubernetes concerns.
//
// codeVerifier is PKCE's proof that the party exchanging the code is the same
// one that started the login (RFC 7636): the caller generates a fresh one per
// login attempt and carries it through both calls. A provider that does not
// speak PKCE simply ignores it.
type Provider interface {
	AuthURL(state, nonce, codeVerifier string) string
	Exchange(ctx context.Context, code, codeVerifier string) (*oauth2.Token, error)
	Claims(ctx context.Context, tok *oauth2.Token) (Claims, error)
}

// Name gives config and registry code one stable key per supported provider.
type Name string

const (
	ProviderEntraID  Name = "entra-id"
	ProviderGoogle   Name = "google"
	ProviderGitLab   Name = "gitlab"
	ProviderKeycloak Name = "keycloak"
	ProviderGitHub   Name = "github"
)

func (n Name) Valid() bool {
	switch n {
	case ProviderEntraID, ProviderGoogle, ProviderGitLab, ProviderKeycloak, ProviderGitHub:
		return true
	default:
		return false
	}
}
