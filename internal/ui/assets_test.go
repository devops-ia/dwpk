package ui

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The terminal is dead without these, and a missing embed shows up only as a
// blank box in a browser - never as a failing build. The CSP forbids loading
// them from anywhere else, so if they are not in the binary they do not exist.
func TestVendoredTerminalAssetsAreEmbeddedAndServed(t *testing.T) {
	t.Parallel()

	wanted := []string{
		"assets/vendor/xterm.js",
		"assets/vendor/xterm.css",
		"assets/vendor/xterm-addon-fit.js",
	}

	for _, name := range wanted {
		info, err := fs.Stat(Assets, name)
		if err != nil {
			t.Fatalf("%s is not embedded: %v", name, err)
		}
		if info.Size() == 0 {
			t.Fatalf("%s is embedded but empty", name)
		}
	}

	server := newTestServer(t)
	for _, name := range wanted {
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/"+name, nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("GET /%s = %d, want 200", name, recorder.Code)
		}
	}
}

// The workspace page and the pop-out window load xterm; nothing else should,
// because it is roughly half a megabyte.
func TestOnlyTerminalPagesLoadXterm(t *testing.T) {
	t.Parallel()

	server := newTestServer(t)
	server.loginFlow = fakeSessionAuthenticator{
		identity: SessionIdentity{UserSpaceNamespace: testOwnerNS}, token: testToken,
	}
	csrf, _ := server.csrfStore.Ensure(testSession)
	server.clientFactory = fakeAPIClientFactory{api: fakeAPI{workspace: runningWorkspace()}}

	loads := func(path string) bool {
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, authedRequest(path, csrf))
		return strings.Contains(recorder.Body.String(), "vendor/xterm.js")
	}

	for _, path := range []string{"/w/dev", "/w/dev/terminal"} {
		if !loads(path) {
			t.Fatalf("%s does not load xterm, so its terminal cannot work", path)
		}
	}
	for _, path := range []string{"/", catalogPath, "/profile"} {
		if loads(path) {
			t.Fatalf("%s loads xterm and does not need it", path)
		}
	}
}
