package ui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestThemeFromRequest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		cookie string
		want   string
	}{
		{name: "no cookie falls back to prefers-color-scheme", cookie: "", want: ""},
		{name: "dark", cookie: themeDark, want: themeDark},
		{name: "light", cookie: themeLight, want: themeLight},
		// A hand-edited cookie must not become an attribute value in the page.
		{name: "unknown value is ignored", cookie: `"><script>alert(1)</script>`, want: ""},
		{name: "empty value is ignored", cookie: "  ", want: ""},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			if test.cookie != "" {
				request.AddCookie(&http.Cookie{Name: themeCookieName, Value: test.cookie})
			}
			if got := themeFromRequest(request); got != test.want {
				t.Fatalf("themeFromRequest() = %q, want %q", got, test.want)
			}
		})
	}
}

// The server stamps the theme on <html> so the page never paints the wrong one.
// The CSP forbids inline script, so this is the only place it can happen.
func TestLoginPageStampsThemeFromCookie(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)

	request := httptest.NewRequest(http.MethodGet, "/login", nil)
	request.AddCookie(&http.Cookie{Name: themeCookieName, Value: themeDark})
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)

	body := recorder.Body.String()
	if !strings.Contains(body, `data-theme="dark"`) {
		t.Fatalf("missing stamped theme attribute: %s", body)
	}
	if !strings.Contains(body, "data-theme-toggle") {
		t.Fatal("theme toggle button is missing")
	}
}

// Signed-out pages centre their card; signed-in pages use the normal document
// layout. This is what makes the login panel sit in the middle of the viewport.
func TestLoginPageUsesCentredShell(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)

	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/login", nil))

	if !strings.Contains(recorder.Body.String(), `class="page auth-shell"`) {
		t.Fatalf("login page is not using the centred shell: %s", recorder.Body.String())
	}
}

func TestAuthenticatedPageDoesNotCentre(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	server.loginFlow = fakeSessionAuthenticator{identity: SessionIdentity{UserSpaceNamespace: testOwnerNS}, token: testToken}
	csrf, err := server.csrfStore.Ensure(testSession)
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}

	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, authedRequest(http.MethodGet, "/", csrf))

	if strings.Contains(recorder.Body.String(), "auth-shell") {
		t.Fatal("a signed-in page should not use the centred auth shell")
	}
}

// The polled status fragment used to render from a context with no base path,
// which emitted unprefixed links once a base path was configured.
func TestWorkspaceStatusFragmentKeepsBasePath(t *testing.T) {
	t.Parallel()
	server := newTestServerWithBasePath(t, "/dwpk")
	server.loginFlow = fakeSessionAuthenticator{identity: SessionIdentity{UserSpaceNamespace: testOwnerNS}, token: testToken}
	csrf, err := server.csrfStore.Ensure(testSession)
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}

	request := httptest.NewRequest(http.MethodGet, "/dwpk/w/dev/status", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: testSession})
	request.Header.Set(csrfHeaderName, csrf)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if strings.Contains(body, `hx-post="/w/`) || strings.Contains(body, `hx-get="/w/`) {
		t.Fatalf("fragment emitted unprefixed links under a base path: %s", body)
	}
}

// The sidebar has to say which section you are in, or seven equal links leave
// you guessing.
func TestSidebarMarksTheCurrentPage(t *testing.T) {
	t.Parallel()
	server, csrf := newAdminScreenServer(t, fakeAPI{})

	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, authedRequest(http.MethodGet, "/admin/quota", csrf))
	body := recorder.Body.String()

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, body)
	}
	if !strings.Contains(body, `class="nav-link nav-link-current" aria-current="page"`) {
		t.Fatalf("no nav link is marked current: %s", body)
	}
	// Exactly one, or "current" means nothing.
	if count := strings.Count(body, "nav-link-current"); count != 1 {
		t.Fatalf("%d nav links marked current, want 1", count)
	}
	// The palette is the keyboard route to the same destinations.
	if !strings.Contains(body, `id="command-palette"`) {
		t.Fatalf("the command palette is missing: %s", body)
	}
}

// Signed-out pages keep the centred shell, with no sidebar to navigate.
func TestAnonymousPagesHaveNoSidebar(t *testing.T) {
	t.Parallel()
	server := newLoginTestServer(t, fakeSessionAuthenticator{localAuth: true})

	body := loginBody(t, server)

	if strings.Contains(body, `class="sidebar"`) {
		t.Fatalf("a signed-out page rendered the sidebar: %s", body)
	}
	if !strings.Contains(body, `class="page auth-shell"`) {
		t.Fatalf("the centred auth shell is missing: %s", body)
	}
}
