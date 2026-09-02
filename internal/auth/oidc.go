package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

var (
	errOIDCIssuerURLRequired = errors.New("issuer URL required")
	errOIDCClientIDRequired  = errors.New("client ID required")
	errOIDCSecretRequired    = errors.New("client secret required")
	errOIDCRedirectRequired  = errors.New("redirect URL required")
	errOIDCIDTokenRequired   = errors.New("id_token required")
	errOIDCEmailRequired     = errors.New("email claim required")
)

var defaultOIDCScopes = []string{"openid", "email"}

type OIDCConfig struct {
	IssuerURL    string
	ClientID     string
	ClientSecret string
	RedirectURL  string
	Scopes       []string
	HTTPClient   *http.Client
}

type oidcProvider struct {
	oauth2Config  *oauth2.Config
	verifyIDToken func(context.Context, string) (oidcIdentityClaims, error)
	verifyNonce   func(context.Context, string, string) error
	httpClient    *http.Client
}

type oidcIdentityClaims struct {
	Email             string   `json:"email"`
	PreferredUsername string   `json:"preferred_username"`
	Groups            []string `json:"groups"`
}

var errOIDCNonceMismatch = errors.New("id token nonce does not match login challenge")

func NewOIDCProvider(cfg OIDCConfig) (Provider, error) {
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("build OIDC provider for issuer %q: %w", cfg.IssuerURL, err)
	}

	ctx := withHTTPClient(context.Background(), cfg.HTTPClient)
	provider, err := oidc.NewProvider(ctx, cfg.IssuerURL)
	if err != nil {
		return nil, fmt.Errorf("discover OIDC provider for issuer %q: %w", cfg.IssuerURL, err)
	}

	verifier := provider.Verifier(&oidc.Config{ClientID: cfg.ClientID})
	config := &oauth2.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		RedirectURL:  cfg.RedirectURL,
		Endpoint:     provider.Endpoint(),
		Scopes:       oidcScopes(cfg.Scopes),
	}

	return &oidcProvider{
		oauth2Config: config,
		httpClient:   cfg.HTTPClient,
		verifyIDToken: func(ctx context.Context, rawIDToken string) (oidcIdentityClaims, error) {
			idToken, err := verifier.Verify(withHTTPClient(ctx, cfg.HTTPClient), rawIDToken)
			if err != nil {
				return oidcIdentityClaims{}, fmt.Errorf("verify id token: %w", err)
			}

			var claims oidcIdentityClaims
			if err := idToken.Claims(&claims); err != nil {
				return oidcIdentityClaims{}, fmt.Errorf("decode id token claims: %w", err)
			}

			return claims, nil
		},
		verifyNonce: func(ctx context.Context, rawIDToken, expectedNonce string) error {
			idToken, err := verifier.Verify(withHTTPClient(ctx, cfg.HTTPClient), rawIDToken)
			if err != nil {
				return fmt.Errorf("verify id token: %w", err)
			}

			if idToken.Nonce != expectedNonce {
				return errOIDCNonceMismatch
			}

			return nil
		},
	}, nil
}

func (p *oidcProvider) AuthURL(state, nonce, codeVerifier string) string {
	return p.oauth2Config.AuthCodeURL(
		state,
		oauth2.S256ChallengeOption(codeVerifier),
		oauth2.SetAuthURLParam("nonce", nonce),
	)
}

func (p *oidcProvider) Exchange(ctx context.Context, code, codeVerifier string) (*oauth2.Token, error) {
	tok, err := p.oauth2Config.Exchange(withHTTPClient(ctx, p.httpClient), code, oauth2.VerifierOption(codeVerifier))
	if err != nil {
		return nil, fmt.Errorf("exchange authorization code: %w", err)
	}

	return tok, nil
}

// ValidateNonce checks that the ID token returned by the provider carries the
// nonce issued at login (§8.5: state and nonce validated), preventing an
// attacker-supplied token from a different login attempt being replayed.
func (p *oidcProvider) ValidateNonce(ctx context.Context, tok *oauth2.Token, expectedNonce string) error {
	rawIDToken, err := rawIDTokenFrom(tok)
	if err != nil {
		return fmt.Errorf("validate nonce: %w", err)
	}

	if err := p.verifyNonce(withHTTPClient(ctx, p.httpClient), rawIDToken, expectedNonce); err != nil {
		return fmt.Errorf("validate nonce: %w", err)
	}

	return nil
}

func rawIDTokenFrom(tok *oauth2.Token) (string, error) {
	if tok == nil {
		return "", errOIDCIDTokenRequired
	}

	rawIDToken, ok := tok.Extra("id_token").(string)
	if !ok || strings.TrimSpace(rawIDToken) == "" {
		return "", errOIDCIDTokenRequired
	}

	return rawIDToken, nil
}

func (p *oidcProvider) Claims(ctx context.Context, tok *oauth2.Token) (Claims, error) {
	rawIDToken, err := rawIDTokenFrom(tok)
	if err != nil {
		return Claims{}, fmt.Errorf("read OIDC claims: %w", err)
	}

	identity, err := p.verifyIDToken(withHTTPClient(ctx, p.httpClient), rawIDToken)
	if err != nil {
		return Claims{}, fmt.Errorf("read OIDC claims: %w", err)
	}

	email, err := emailFromIDTokenClaims(identity)
	if err != nil {
		return Claims{}, fmt.Errorf("read OIDC claims: %w", err)
	}

	return Claims{Email: email, Groups: identity.Groups}, nil
}

func emailFromIDTokenClaims(claims oidcIdentityClaims) (string, error) {
	if email := strings.TrimSpace(claims.Email); email != "" {
		return email, nil
	}

	if email := strings.TrimSpace(claims.PreferredUsername); email != "" {
		return email, nil
	}

	return "", errOIDCEmailRequired
}

func oidcScopes(scopes []string) []string {
	if len(scopes) == 0 {
		return append([]string(nil), defaultOIDCScopes...)
	}

	return append([]string(nil), scopes...)
}

func (c OIDCConfig) validate() error {
	switch {
	case strings.TrimSpace(c.IssuerURL) == "":
		return errOIDCIssuerURLRequired
	case strings.TrimSpace(c.ClientID) == "":
		return errOIDCClientIDRequired
	case strings.TrimSpace(c.ClientSecret) == "":
		return errOIDCSecretRequired
	case strings.TrimSpace(c.RedirectURL) == "":
		return errOIDCRedirectRequired
	default:
		return nil
	}
}

func withHTTPClient(ctx context.Context, client *http.Client) context.Context {
	if client == nil {
		return ctx
	}

	ctx = oidc.ClientContext(ctx, client)
	ctx = context.WithValue(ctx, oauth2.HTTPClient, client)
	return ctx
}
