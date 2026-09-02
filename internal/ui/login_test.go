package ui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/devops-ia/dwpk/internal/auth"
)

const testGatewayHost = "dwpk.example.com"

func newLoginTestServer(t *testing.T, flow fakeSessionAuthenticator) *Server {
	t.Helper()
	server, err := NewServer(ServerConfig{
		LoginFlow:     flow,
		ClientFactory: fakeAPIClientFactory{api: fakeAPI{}},
		GatewayHost:   testGatewayHost,
		SessionTTL:    time.Minute,
	})
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	return server
}

func loginBody(t *testing.T, server *Server) string {
	t.Helper()
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/login", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /login status = %d", recorder.Code)
	}
	return recorder.Body.String()
}

// Only configured providers are offered. Listing all five meant a user could
// pick one that was never set up and land on an error page.
func TestLoginPageOffersOnlyConfiguredProviders(t *testing.T) {
	t.Parallel()
	server := newLoginTestServer(t, fakeSessionAuthenticator{
		providers: []auth.Name{auth.ProviderGitHub, auth.ProviderGoogle},
	})

	body := loginBody(t, server)

	for _, want := range []string{`value="github"`, `value="google"`, "GitHub", "Google"} {
		if !strings.Contains(body, want) {
			t.Fatalf("login page missing %q: %s", want, body)
		}
	}
	for _, unwanted := range []string{`value="keycloak"`, `value="gitlab"`, `value="entra-id"`} {
		if strings.Contains(body, unwanted) {
			t.Fatalf("login page offers unconfigured provider %q", unwanted)
		}
	}
	if !strings.Contains(body, `<select id="provider" name="provider">`) {
		t.Fatalf("providers are not rendered as a drop-down: %s", body)
	}
}

func TestLoginPageHidesProviderFormWhenNoneConfigured(t *testing.T) {
	t.Parallel()
	server := newLoginTestServer(t, fakeSessionAuthenticator{localAuth: true})

	body := loginBody(t, server)

	if strings.Contains(body, `name="provider"`) {
		t.Fatalf("provider control rendered with no providers configured: %s", body)
	}
	// The local form is the only way in, and must still be there.
	if !strings.Contains(body, `action="/login/local"`) {
		t.Fatalf("local login form missing: %s", body)
	}
	// The divider only makes sense between two options.
	if strings.Contains(body, "local-login-divider") {
		t.Fatalf("rendered an 'or' divider with only one login method: %s", body)
	}
}

func TestLoginPageReportsWhenNoLoginMethodExists(t *testing.T) {
	t.Parallel()
	server := newLoginTestServer(t, fakeSessionAuthenticator{})

	body := loginBody(t, server)

	if !strings.Contains(body, "No login method is configured") {
		t.Fatalf("login page does not say it is unusable: %s", body)
	}
}

// The drop-down is a plain GET form, so choosing a provider arrives as a query
// parameter and must start the OAuth redirect.
func TestLoginPickerStartsFlowForSelectedProvider(t *testing.T) {
	t.Parallel()
	server := newLoginTestServer(t, fakeSessionAuthenticator{providers: []auth.Name{auth.ProviderGitHub}})

	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/login?provider=github", nil))

	if recorder.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302, body = %s", recorder.Code, recorder.Body.String())
	}
}

// The path route stays reachable whatever the drop-down shows, so it has to
// refuse an unconfigured provider itself.
func TestBeginLoginRejectsUnconfiguredProvider(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		path string
	}{
		{name: "path route", path: "/login/keycloak"},
		{name: "query parameter", path: "/login?provider=keycloak"},
		{name: "not a provider at all", path: "/login?provider=nonsense"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := newLoginTestServer(t, fakeSessionAuthenticator{providers: []auth.Name{auth.ProviderGitHub}})

			recorder := httptest.NewRecorder()
			server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, test.path, nil))

			if recorder.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404, body = %s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestProviderOptionsLabelsConfiguredNames(t *testing.T) {
	t.Parallel()

	options := providerOptions([]auth.Name{auth.ProviderEntraID, auth.ProviderGitHub})
	if len(options) != 2 {
		t.Fatalf("options = %d, want 2", len(options))
	}
	if options[0].Name != "entra-id" || options[0].Label != "Entra ID" {
		t.Fatalf("first option = %+v", options[0])
	}

	if got := providerOptions(nil); len(got) != 0 {
		t.Fatalf("providerOptions(nil) = %v, want empty", got)
	}
}

// The checkbox has to reach the cookie, or "remember me" is decoration.
func TestLocalLoginRememberMeLengthensTheSessionCookie(t *testing.T) {
	t.Parallel()
	server := newLoginTestServer(t, fakeSessionAuthenticator{localAuth: true})

	sessionCookieExpiry := func(form string) time.Time {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, "/login/local", strings.NewReader(form))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, request)
		if recorder.Code != http.StatusFound {
			t.Fatalf("POST /login/local status = %d, want 302", recorder.Code)
		}
		for _, cookie := range recorder.Result().Cookies() {
			if cookie.Name == sessionCookieName {
				return cookie.Expires
			}
		}
		t.Fatalf("no %s cookie was set", sessionCookieName)
		return time.Time{}
	}

	plain := sessionCookieExpiry("username=alice&password=s3cret")
	remembered := sessionCookieExpiry("username=alice&password=s3cret&remember=true")

	if !remembered.After(plain.Add(24 * time.Hour)) {
		t.Fatalf("remembered cookie expires at %v, barely past the default %v", remembered, plain)
	}
}

// An OAuth login loses the form across the provider redirect, so the choice
// travels in a cookie. Without it the checkbox silently does nothing there.
func TestBeginLoginCarriesRememberMeAcrossTheRedirect(t *testing.T) {
	t.Parallel()
	server := newLoginTestServer(t, fakeSessionAuthenticator{providers: []auth.Name{auth.ProviderGitHub}})

	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder,
		httptest.NewRequest(http.MethodGet, "/login?provider=github&remember=true", nil))

	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == loginRememberCookieName {
			if cookie.Value != boolTrue {
				t.Fatalf("%s cookie value = %q, want %q", loginRememberCookieName, cookie.Value, boolTrue)
			}
			return
		}
	}
	t.Fatalf("no %s cookie was set: %v", loginRememberCookieName, recorder.Result().Cookies())
}
