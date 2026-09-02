package ui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/devops-ia/dwpk/internal/auth"
)

func newTestServerWithBasePath(t *testing.T, basePath string) *Server {
	t.Helper()
	server, err := NewServer(ServerConfig{
		// A configured provider, so the login page actually renders the
		// provider form whose action carries the base path.
		LoginFlow:     fakeSessionAuthenticator{providers: []auth.Name{auth.ProviderGitHub}},
		ClientFactory: fakeAPIClientFactory{api: fakeAPI{}},
		GatewayHost:   "dwpk.example.com",
		SessionTTL:    time.Minute,
		BasePath:      basePath,
	})
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	return server
}

func TestServerWithBasePathServesLoginUnderPrefix(t *testing.T) {
	t.Parallel()
	server := newTestServerWithBasePath(t, "/dwpk")

	// The bare, unprefixed path is not routed once a base path is set.
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/login", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("GET /login status = %d, want 404 when base path is set", recorder.Code)
	}

	// The prefixed path is routed correctly.
	recorder = httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/dwpk/login", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /dwpk/login status = %d, want 200", recorder.Code)
	}
	body := recorder.Body.String()
	// The provider drop-down submits back to /login, so its form action is the
	// link that has to carry the prefix.
	if !strings.Contains(body, `action="/dwpk/login"`) {
		t.Fatalf("login page missing base-path-prefixed provider form: %s", body)
	}
	if !strings.Contains(body, `href="/dwpk/assets/app.css"`) {
		t.Fatalf("login page missing base-path-prefixed stylesheet: %s", body)
	}
}

func TestServerWithBasePathRedirectsToLoginWithPrefix(t *testing.T) {
	t.Parallel()
	server := newTestServerWithBasePath(t, "/dwpk")

	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/dwpk/", nil))
	if recorder.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", recorder.Code)
	}
	if loc := recorder.Header().Get("Location"); !strings.HasPrefix(loc, "/dwpk/login") {
		t.Fatalf("Location = %q, want prefixed with /dwpk/login", loc)
	}
}

func TestServerWithBasePathSetsCookiePathToBasePath(t *testing.T) {
	t.Parallel()
	server := newTestServerWithBasePath(t, "/dwpk")
	server.loginFlow = fakeSessionAuthenticator{identity: SessionIdentity{UserSpaceNamespace: "dwpk-alice"}}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/dwpk/login/github", nil)
	server.Handler().ServeHTTP(recorder, request)

	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Path != "/dwpk" {
			t.Errorf("cookie %q Path = %q, want /dwpk", cookie.Name, cookie.Path)
		}
	}
}

func TestServerWithoutBasePathKeepsRootRouting(t *testing.T) {
	t.Parallel()
	server := newTestServerWithBasePath(t, "")

	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/login", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /login status = %d, want 200", recorder.Code)
	}
	body := recorder.Body.String()
	if strings.Contains(body, `href="/dwpk`) {
		t.Fatalf("unexpected base path leaked into rendered page: %s", body)
	}
}
