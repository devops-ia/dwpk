package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"sync/atomic"
	"testing"

	"golang.org/x/oauth2"
)

func TestGitHubProviderAuthURLAndExchange(t *testing.T) {
	t.Parallel()

	var tokenCode atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login/oauth/access_token":
			if err := r.ParseForm(); err != nil {
				t.Fatalf("ParseForm() error = %v", err)
			}
			tokenCode.Store(r.Form.Get("code"))
			writeJSON(t, w, map[string]any{
				"access_token": "github-access-token",
				"token_type":   "bearer",
			})
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer server.Close()

	provider, err := NewGitHubProvider(GitHubConfig{
		ClientID:     "client-id",
		ClientSecret: "client-secret",
		RedirectURL:  "https://dwpk.example/callback",
		AuthorizeURL: server.URL + "/login/oauth/authorize",
		TokenURL:     server.URL + "/login/oauth/access_token",
		APIURL:       server.URL,
		HTTPClient:   server.Client(),
	})
	if err != nil {
		t.Fatalf("NewGitHubProvider() error = %v", err)
	}

	authURL, err := url.Parse(provider.AuthURL("state-123", "ignored", "ignored"))
	if err != nil {
		t.Fatalf("Parse(AuthURL()) error = %v", err)
	}
	query := authURL.Query()
	if got := query.Get("state"); got != "state-123" {
		t.Fatalf("state = %q, want %q", got, "state-123")
	}
	if got := query.Get("scope"); got != "user:email" {
		t.Fatalf("scope = %q, want %q", got, "user:email")
	}

	tok, err := provider.Exchange(context.Background(), "auth-code", "verifier")
	if err != nil {
		t.Fatalf("Exchange() error = %v", err)
	}
	if tok.AccessToken != "github-access-token" {
		t.Fatalf("Exchange() access token = %q, want %q", tok.AccessToken, "github-access-token")
	}
	if got, _ := tokenCode.Load().(string); got != "auth-code" {
		t.Fatalf("posted code = %q, want %q", got, "auth-code")
	}
}

func TestGitHubProviderClaims(t *testing.T) {
	t.Parallel()

	var userCalls atomic.Int32
	var emailCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer github-access-token" {
			t.Fatalf("Authorization = %q, want %q", got, "Bearer github-access-token")
		}

		switch r.URL.Path {
		case "/user":
			userCalls.Add(1)
			writeJSON(t, w, githubUser{Email: "public@example.com"})
		case "/user/emails":
			emailCalls.Add(1)
			writeJSON(t, w, []githubEmail{
				{Email: "secondary@example.com", Primary: false, Verified: true},
				{Email: "primary@example.com", Primary: true, Verified: true},
			})
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer server.Close()

	provider, err := NewGitHubProvider(GitHubConfig{
		ClientID:     "client-id",
		ClientSecret: "client-secret",
		RedirectURL:  "https://dwpk.example/callback",
		APIURL:       server.URL,
		HTTPClient:   server.Client(),
	})
	if err != nil {
		t.Fatalf("NewGitHubProvider() error = %v", err)
	}

	claims, err := provider.Claims(context.Background(), &oauth2.Token{AccessToken: "github-access-token"})
	if err != nil {
		t.Fatalf("Claims() error = %v", err)
	}
	if claims.Email != "primary@example.com" {
		t.Fatalf("Claims() email = %q, want %q", claims.Email, "primary@example.com")
	}
	if userCalls.Load() != 1 {
		t.Fatalf("/user calls = %d, want 1", userCalls.Load())
	}
	if emailCalls.Load() != 1 {
		t.Fatalf("/user/emails calls = %d, want 1", emailCalls.Load())
	}
}

func TestPrimaryVerifiedEmail(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		emails  []githubEmail
		want    string
		wantErr bool
	}{
		{
			name:   "primary verified",
			emails: []githubEmail{{Email: "primary@example.com", Primary: true, Verified: true}},
			want:   "primary@example.com",
		},
		{
			name:    "missing primary verified",
			emails:  []githubEmail{{Email: "secondary@example.com", Primary: false, Verified: true}},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := primaryVerifiedEmail(tt.emails)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("primaryVerifiedEmail() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("primaryVerifiedEmail() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("primaryVerifiedEmail() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestGitHubScopes(t *testing.T) {
	t.Parallel()

	custom := githubScopes([]string{"read:user"})
	if !reflect.DeepEqual(custom, []string{"read:user"}) {
		t.Fatalf("githubScopes(custom) = %v", custom)
	}

	defaults := githubScopes(nil)
	if !reflect.DeepEqual(defaults, []string{"user:email"}) {
		t.Fatalf("githubScopes(default) = %v", defaults)
	}
}

func TestFirstNonEmpty(t *testing.T) {
	t.Parallel()

	if got := firstNonEmpty("", "  ", "value"); got != "value" {
		t.Fatalf("firstNonEmpty() = %q, want %q", got, "value")
	}
}
