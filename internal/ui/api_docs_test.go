package ui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The API docs page and its spec must never require a session - that's the
// whole point of making them public (they're read-only documentation, not a
// control surface).
func TestAPIDocsRoutesArePublic(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)

	t.Run("GET /api/docs", func(t *testing.T) {
		t.Parallel()
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/docs", nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", recorder.Code)
		}
		if !strings.Contains(recorder.Body.String(), "swagger-ui-bundle.js") {
			t.Fatal("page does not reference the vendored Swagger UI bundle")
		}
		if !strings.Contains(recorder.Body.String(), "/api/v1/openapi.yaml") {
			t.Fatal("page does not point Swagger UI at the spec route")
		}
	})

	t.Run("GET /api/v1/openapi.yaml", func(t *testing.T) {
		t.Parallel()
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/openapi.yaml", nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", recorder.Code)
		}
		if !strings.Contains(recorder.Body.String(), "openapi: 3.0.3") {
			t.Fatal("response does not look like the OpenAPI document")
		}
	})
}

// The page shipped blank, and the test above passed the whole time: it asks
// for a 200 and for the bundle to be mentioned, both of which an empty page
// satisfies. What actually broke was the CSP.
//
// The handler sends `default-src 'self'` with `'unsafe-inline'` on style-src
// only, so the inline <script> that called SwaggerUIBundle was refused and the
// viewer never initialised. The fix was to move that bootstrap into a file
// rather than to loosen the header, so the rule to hold is simply: this page
// carries no inline script. Nothing about a screenshot or a 200 can tell you
// that; the markup can.
func TestAPIDocsPageRunsNoInlineScript(t *testing.T) {
	t.Parallel()
	body := newTestServer(t).apiDocsBody(t)

	for _, tag := range strings.Split(body, "<script")[1:] {
		open := strings.Index(tag, ">")
		if open < 0 {
			t.Fatalf("malformed script tag: %s", tag)
		}
		if attrs := tag[:open]; !strings.Contains(attrs, "src=") {
			t.Fatalf("inline script on a page whose CSP forbids one: <script%s>", attrs)
		}
		if code := strings.TrimSpace(tag[open+1 : strings.Index(tag, "</script>")]); code != "" {
			t.Fatalf("script tag has a body, which the CSP refuses to run: %q", code)
		}
	}
	if !strings.Contains(body, "api-docs.js") {
		t.Fatal("page never loads the bootstrap that initialises Swagger UI")
	}
}

// The vendored stylesheet draws Swagger UI's expand arrows as data: SVG URLs.
// Under the platform's default CSP those are blocked and the arrows vanish,
// which a body-text assertion would never notice.
func TestAPIDocsCSPAllowsTheStylesheetsDataURIIcons(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)

	request := httptest.NewRequest(http.MethodGet, "/api/docs", nil)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)

	policy := recorder.Header().Get("Content-Security-Policy")
	if !strings.Contains(policy, "img-src 'self' data:") {
		t.Fatalf("CSP blocks the icons the vendored CSS draws inline: %q", policy)
	}
}

// Every file the page pulls in must actually be served. A missing one is a 404
// in the console of a page we are asking people to trust - which is how the
// dangling sourceMappingURL in the vendored CSS was found.
func TestAPIDocsPageAssetsAllResolve(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	body := server.apiDocsBody(t)

	var refs []string
	for _, attr := range []string{`src="`, `href="`} {
		for _, part := range strings.Split(body, attr)[1:] {
			refs = append(refs, part[:strings.Index(part, `"`)])
		}
	}
	if len(refs) == 0 {
		t.Fatal("page references nothing at all")
	}

	for _, ref := range refs {
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, ref, nil))
		if recorder.Code != http.StatusOK {
			t.Errorf("%s -> %d", ref, recorder.Code)
		}
	}

	css, err := Assets.ReadFile("assets/vendor/swagger-ui.css")
	if err != nil {
		t.Fatalf("read vendored css: %v", err)
	}
	// Source maps are deliberately not vendored (assets/vendor/README.md), so
	// a reference to one is a guaranteed 404 the moment devtools are open.
	if strings.Contains(string(css), "sourceMappingURL") {
		t.Error("vendored CSS points at a source map that is not shipped")
	}
}

func (s *Server) apiDocsBody(t *testing.T) string {
	t.Helper()
	recorder := httptest.NewRecorder()
	s.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/docs", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	return recorder.Body.String()
}

// The exact route /api/v1/openapi.yaml must win over the /api/v1/ prefix
// handler that requires a session - otherwise the spec would 401 instead of
// rendering, breaking the whole page above it.
func TestOpenAPISpecRouteBeatsTheAuthenticatedAPIPrefix(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)

	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/openapi.yaml", nil))
	if recorder.Code == http.StatusUnauthorized {
		t.Fatal("the openapi.yaml route was shadowed by the authenticated /api/v1/ prefix")
	}
}

// The copy served from the embedded binary and the copy checked into
// docs/openapi.yaml (the one `redocly lint` and this repo's readers look at)
// must be identical, or one of them is quietly lying.
func TestEmbeddedOpenAPISpecMatchesDocsCopy(t *testing.T) {
	t.Parallel()

	embedded, err := Assets.ReadFile("assets/openapi.yaml")
	if err != nil {
		t.Fatalf("read embedded spec: %v", err)
	}

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}
	docsPath := filepath.Join(filepath.Dir(thisFile), "..", "..", "docs", "openapi.yaml")
	onDisk, err := os.ReadFile(docsPath)
	if err != nil {
		t.Fatalf("read %s: %v", docsPath, err)
	}

	if string(embedded) != string(onDisk) {
		t.Fatalf("internal/ui/assets/openapi.yaml has drifted from docs/openapi.yaml; copy docs/openapi.yaml over it")
	}
}

// A public page nothing links to is a page nobody finds. Before this, the only
// mentions of /api/docs anywhere in the repository were its route registration,
// this test file, and one line of INSTALLATION.md - so you had to already know
// the URL to reach it.
//
// Both links matter and for different reasons: the sidebar is how a signed-in
// user finds it, and the login page is the only surface a reader without an
// account ever sees, which is the audience the public routing was for.
func TestAPIDocsIsLinkedFromTheApplication(t *testing.T) {
	t.Parallel()
	server, csrf := logServer(t, fakeAPI{workspace: runningWorkspace()})

	signedIn := httptest.NewRecorder()
	server.Handler().ServeHTTP(signedIn, authedRequest(http.MethodGet, "/", csrf))
	if !strings.Contains(signedIn.Body.String(), `href="/api/docs"`) {
		t.Error("the sidebar does not link to the API reference")
	}

	anonymous := httptest.NewRecorder()
	server.Handler().ServeHTTP(anonymous, httptest.NewRequest(http.MethodGet, "/login", nil))
	if !strings.Contains(anonymous.Body.String(), `href="/api/docs"`) {
		t.Error("the login page does not link to the API reference")
	}
}

// htmx inserts its own <style> element for .htmx-indicator unless told not to,
// and the application CSP refuses it. The rules live in app.css instead; this
// pins the config that stops htmx duplicating them.
func TestLayoutStopsHTMXInjectingItsOwnStylesheet(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)

	request := httptest.NewRequest(http.MethodGet, "/login", nil)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)

	if !strings.Contains(recorder.Body.String(), `"includeIndicatorStyles":false`) {
		t.Fatal("htmx will inject a style element the CSP blocks")
	}
	css, err := Assets.ReadFile("assets/app.css")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(css), ".htmx-indicator {") {
		t.Fatal("indicator styles were disabled without app.css taking them over")
	}
}
