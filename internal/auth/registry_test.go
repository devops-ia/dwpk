package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewRegistryAndGet(t *testing.T) {
	t.Parallel()

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
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	registry, err := NewRegistry(RegistryConfig{
		Providers: map[Name]ProviderConfig{
			ProviderGoogle: {
				IssuerURL:    server.URL,
				ClientID:     "google-client",
				ClientSecret: "google-secret",
				RedirectURL:  "https://dwpk.example/google/callback",
			},
			ProviderGitHub: {
				ClientID:     "github-client",
				ClientSecret: "github-secret",
				RedirectURL:  "https://dwpk.example/github/callback",
			},
		},
	})
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}

	tests := []struct {
		name     string
		provider Name
		wantErr  bool
	}{
		{name: "configured oidc provider", provider: ProviderGoogle},
		{name: "configured github provider", provider: ProviderGitHub},
		{name: "unknown provider", provider: Name("unknown"), wantErr: true},
		{name: "not configured provider", provider: ProviderEntraID, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider, err := registry.Get(tt.provider)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Get() error = nil, want error")
				}
				if provider != nil {
					t.Fatalf("Get() provider = %T, want nil", provider)
				}
				return
			}

			if err != nil {
				t.Fatalf("Get() error = %v", err)
			}
			if provider == nil {
				t.Fatal("Get() provider = nil, want configured provider")
			}
		})
	}
}
