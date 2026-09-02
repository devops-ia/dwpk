package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/oauth2"
)

func TestNewOIDCProviderAuthURLAndExchange(t *testing.T) {
	t.Parallel()

	var tokenRequest struct {
		GrantType    string
		Code         string
		CodeVerifier string
		RedirectURI  string
		AuthHeader   string
	}

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			writeJSON(t, w, map[string]string{
				"issuer":                 server.URL,
				"authorization_endpoint": server.URL + "/authorize",
				"token_endpoint":         server.URL + "/token",
				"jwks_uri":               server.URL + "/keys",
			})
		case "/token":
			if err := r.ParseForm(); err != nil {
				t.Fatalf("ParseForm() error = %v", err)
			}
			tokenRequest.GrantType = r.Form.Get("grant_type")
			tokenRequest.Code = r.Form.Get("code")
			tokenRequest.CodeVerifier = r.Form.Get("code_verifier")
			tokenRequest.RedirectURI = r.Form.Get("redirect_uri")
			tokenRequest.AuthHeader = r.Header.Get("Authorization")

			writeJSON(t, w, map[string]any{
				"access_token": "access-token",
				"id_token":     "id-token",
				"token_type":   "Bearer",
			})
		case "/keys":
			writeJSON(t, w, map[string]any{"keys": []any{}})
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer server.Close()

	provider, err := NewOIDCProvider(OIDCConfig{
		IssuerURL:    server.URL,
		ClientID:     "client-id",
		ClientSecret: "client-secret",
		RedirectURL:  "https://dwpk.example/callback",
		HTTPClient:   server.Client(),
	})
	if err != nil {
		t.Fatalf("NewOIDCProvider() error = %v", err)
	}

	if _, ok := provider.(*oidcProvider); !ok {
		t.Fatalf("provider type = %T, want *oidcProvider", provider)
	}

	authURL, err := url.Parse(provider.AuthURL("state-123", "nonce-456", "verifier-789"))
	if err != nil {
		t.Fatalf("Parse(AuthURL()) error = %v", err)
	}

	query := authURL.Query()
	if got := query.Get("state"); got != "state-123" {
		t.Fatalf("state = %q, want %q", got, "state-123")
	}
	if got := query.Get("nonce"); got != "nonce-456" {
		t.Fatalf("nonce = %q, want %q", got, "nonce-456")
	}
	if got := query.Get("code_challenge_method"); got != "S256" {
		t.Fatalf("code_challenge_method = %q, want %q", got, "S256")
	}
	if got := query.Get("scope"); got != "openid email" {
		t.Fatalf("scope = %q, want %q", got, "openid email")
	}
	if got := query.Get("code_challenge"); got != s256Challenge("verifier-789") {
		t.Fatalf("code_challenge = %q, want %q", got, s256Challenge("verifier-789"))
	}

	tok, err := provider.Exchange(context.Background(), "auth-code", "verifier-789")
	if err != nil {
		t.Fatalf("Exchange() error = %v", err)
	}
	if tok.AccessToken != "access-token" {
		t.Fatalf("Exchange() access token = %q, want %q", tok.AccessToken, "access-token")
	}

	if tokenRequest.GrantType != "authorization_code" {
		t.Fatalf("grant_type = %q, want %q", tokenRequest.GrantType, "authorization_code")
	}
	if tokenRequest.Code != "auth-code" {
		t.Fatalf("code = %q, want %q", tokenRequest.Code, "auth-code")
	}
	if tokenRequest.CodeVerifier != "verifier-789" {
		t.Fatalf("code_verifier = %q, want %q", tokenRequest.CodeVerifier, "verifier-789")
	}
	if tokenRequest.RedirectURI != "https://dwpk.example/callback" {
		t.Fatalf("redirect_uri = %q, want %q", tokenRequest.RedirectURI, "https://dwpk.example/callback")
	}
	if tokenRequest.AuthHeader == "" {
		t.Fatal("Authorization header missing on token exchange")
	}
}

func TestOIDCClaims(t *testing.T) {
	t.Parallel()

	provider := &oidcProvider{
		verifyIDToken: func(ctx context.Context, rawIDToken string) (oidcIdentityClaims, error) {
			if rawIDToken != "verified-token" {
				t.Fatalf("verifyIDToken() raw token = %q, want %q", rawIDToken, "verified-token")
			}
			return oidcIdentityClaims{Email: "person@example.com", Groups: []string{"paas-admins", "engineering"}}, nil
		},
	}

	claims, err := provider.Claims(context.Background(), (&oauth2.Token{}).WithExtra(map[string]any{"id_token": "verified-token"}))
	if err != nil {
		t.Fatalf("Claims() error = %v", err)
	}
	if claims.Email != "person@example.com" {
		t.Fatalf("Claims() email = %q, want %q", claims.Email, "person@example.com")
	}
	if len(claims.Groups) != 2 || claims.Groups[0] != "paas-admins" || claims.Groups[1] != "engineering" {
		t.Fatalf("Claims() groups = %v, want [paas-admins engineering]", claims.Groups)
	}
}

func TestOIDCValidateNonce(t *testing.T) {
	t.Parallel()

	tok := (&oauth2.Token{}).WithExtra(map[string]any{"id_token": "verified-token"})

	t.Run("matching nonce passes", func(t *testing.T) {
		t.Parallel()

		provider := &oidcProvider{
			verifyNonce: func(ctx context.Context, rawIDToken, expectedNonce string) error {
				if rawIDToken != "verified-token" || expectedNonce != "expected-nonce" {
					t.Fatalf("verifyNonce() got (%q, %q)", rawIDToken, expectedNonce)
				}
				return nil
			},
		}

		if err := provider.ValidateNonce(context.Background(), tok, "expected-nonce"); err != nil {
			t.Fatalf("ValidateNonce() error = %v", err)
		}
	})

	t.Run("mismatched nonce rejected", func(t *testing.T) {
		t.Parallel()

		provider := &oidcProvider{
			verifyNonce: func(ctx context.Context, rawIDToken, expectedNonce string) error {
				return errOIDCNonceMismatch
			},
		}

		err := provider.ValidateNonce(context.Background(), tok, "expected-nonce")
		if err == nil || !strings.Contains(err.Error(), errOIDCNonceMismatch.Error()) {
			t.Fatalf("ValidateNonce() error = %v, want nonce mismatch error", err)
		}
	})

	t.Run("missing id token rejected", func(t *testing.T) {
		t.Parallel()

		provider := &oidcProvider{}
		err := provider.ValidateNonce(context.Background(), &oauth2.Token{AccessToken: "access-token"}, "expected-nonce")
		if err == nil || !strings.Contains(err.Error(), errOIDCIDTokenRequired.Error()) {
			t.Fatalf("ValidateNonce() error = %v, want missing id_token error", err)
		}
	})
}

func TestOIDCClaimsMissingIDToken(t *testing.T) {
	t.Parallel()

	provider := &oidcProvider{}
	_, err := provider.Claims(context.Background(), &oauth2.Token{AccessToken: "access-token"})
	if err == nil || !strings.Contains(err.Error(), errOIDCIDTokenRequired.Error()) {
		t.Fatalf("Claims() error = %v, want missing id_token error", err)
	}
}

func TestEmailFromIDTokenClaims(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		claims  oidcIdentityClaims
		want    string
		wantErr bool
	}{
		{name: "email claim", claims: oidcIdentityClaims{Email: "person@example.com"}, want: "person@example.com"},
		{name: "preferred username fallback", claims: oidcIdentityClaims{PreferredUsername: "person@example.com"}, want: "person@example.com"},
		{name: "missing", claims: oidcIdentityClaims{}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := emailFromIDTokenClaims(tt.claims)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("emailFromIDTokenClaims() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("emailFromIDTokenClaims() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("emailFromIDTokenClaims() = %q, want %q", got, tt.want)
			}
		})
	}
}

// newDiscoveryOnlyOIDCProvider builds a provider against a server that only
// answers OIDC discovery - enough for AuthURL, which never contacts the
// token endpoint.
func newDiscoveryOnlyOIDCProvider(t *testing.T) Provider {
	t.Helper()
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			writeJSON(t, w, map[string]string{
				"issuer":                 server.URL,
				"authorization_endpoint": server.URL + "/authorize",
				"token_endpoint":         server.URL + "/token",
				"jwks_uri":               server.URL + "/keys",
			})
		case "/keys":
			writeJSON(t, w, map[string]any{"keys": []any{}})
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)

	provider, err := NewOIDCProvider(OIDCConfig{
		IssuerURL:    server.URL,
		ClientID:     "client-id",
		ClientSecret: "client-secret",
		RedirectURL:  "https://dwpk.example/callback",
		HTTPClient:   server.Client(),
	})
	if err != nil {
		t.Fatalf("NewOIDCProvider() error = %v", err)
	}
	return provider
}

// codeVerifier used to be generated once in NewOIDCProvider and reused for
// every login the process ever served, reducing PKCE to a constant shared
// across every concurrent user rather than a fresh proof per attempt
// (RFC 7636). Two logins started back to back on the same provider must
// produce independent challenges.
func TestOIDCProviderAuthURLIsIndependentPerLogin(t *testing.T) {
	t.Parallel()

	provider := newDiscoveryOnlyOIDCProvider(t)

	first, err := url.Parse(provider.AuthURL("state-1", "nonce-1", "verifier-aaa"))
	if err != nil {
		t.Fatalf("Parse(AuthURL()) error = %v", err)
	}
	second, err := url.Parse(provider.AuthURL("state-2", "nonce-2", "verifier-bbb"))
	if err != nil {
		t.Fatalf("Parse(AuthURL()) error = %v", err)
	}

	firstChallenge := first.Query().Get("code_challenge")
	secondChallenge := second.Query().Get("code_challenge")

	if firstChallenge != s256Challenge("verifier-aaa") {
		t.Fatalf("first code_challenge = %q, want the challenge for verifier-aaa", firstChallenge)
	}
	if secondChallenge != s256Challenge("verifier-bbb") {
		t.Fatalf("second code_challenge = %q, want the challenge for verifier-bbb", secondChallenge)
	}
	if firstChallenge == secondChallenge {
		t.Fatal("two logins produced the same code_challenge; the verifier is being reused instead of generated per login")
	}
}

func TestOIDCScopes(t *testing.T) {
	t.Parallel()

	custom := oidcScopes([]string{"openid", "profile"})
	if !reflect.DeepEqual(custom, []string{"openid", "profile"}) {
		t.Fatalf("oidcScopes(custom) = %v", custom)
	}

	defaults := oidcScopes(nil)
	if !reflect.DeepEqual(defaults, []string{"openid", "email"}) {
		t.Fatalf("oidcScopes(default) = %v", defaults)
	}
}

func writeJSON(t *testing.T, w http.ResponseWriter, body any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(body); err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
}

func s256Challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return strings.TrimRight(base64.URLEncoding.EncodeToString(sum[:]), "=")
}
