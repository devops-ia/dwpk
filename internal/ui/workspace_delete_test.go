package ui

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// newOwnerScreenServer is newAdminScreenServer's ordinary-user counterpart:
// an authenticated session with no admin verbs granted, for exercising
// routes an owner uses on their own workspace.
func newOwnerScreenServer(t *testing.T, api fakeAPI) (*Server, string) {
	t.Helper()
	server := newTestServer(t)
	server.loginFlow = fakeSessionAuthenticator{
		identity: SessionIdentity{UserSpaceNamespace: testOwnerNS},
		token:    testToken,
	}
	server.clientFactory = fakeAPIClientFactory{api: api}
	csrf, err := server.csrfStore.Ensure(testSession)
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	return server, csrf
}

// The owner's own /w/{name}/delete requires the typed name to match, exactly
// like the admin route it mirrors - a dialog dismissed without reading types
// nothing, and the server is what actually enforces it.
func TestOwnWorkspaceDeleteRequiresTheNameTyped(t *testing.T) {
	t.Parallel()

	t.Run("a mismatch deletes nothing", func(t *testing.T) {
		t.Parallel()
		var deletedWS, deletedClaim string
		server, csrf := newOwnerScreenServer(t, fakeAPI{
			deletedWS: &deletedWS, deletedClaim: &deletedClaim,
		})

		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, authedFormRequest(
			"/w/dev/delete", csrf, "confirm_name=dee&delete_volume=true"))

		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", recorder.Code)
		}
		if deletedWS != "" || deletedClaim != "" {
			t.Fatalf("a mismatch still deleted %q and %q", deletedWS, deletedClaim)
		}
	})

	t.Run("a match deletes the workspace and its volume in its own namespace", func(t *testing.T) {
		t.Parallel()
		var deletedWS, deletedClaim string
		server, csrf := newOwnerScreenServer(t, fakeAPI{
			deletedWS: &deletedWS, deletedClaim: &deletedClaim,
		})

		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, authedFormRequest(
			"/w/dev/delete", csrf, "confirm_name=dev&delete_volume=true"))

		if recorder.Code != http.StatusFound {
			t.Fatalf("status = %d, want 302, body = %s", recorder.Code, recorder.Body.String())
		}
		if deletedWS != testOwnerNS+"/dev" {
			t.Fatalf("deletedWS = %q", deletedWS)
		}
		if deletedClaim != testOwnerNS+"/home-dev-0" {
			t.Fatalf("deletedClaim = %q", deletedClaim)
		}
	})

	t.Run("unticking the box keeps the volume", func(t *testing.T) {
		t.Parallel()
		var deletedWS, deletedClaim string
		server, csrf := newOwnerScreenServer(t, fakeAPI{
			deletedWS: &deletedWS, deletedClaim: &deletedClaim,
		})

		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, authedFormRequest(
			"/w/dev/delete", csrf, "confirm_name=dev"))

		if recorder.Code != http.StatusFound {
			t.Fatalf("status = %d, want 302, body = %s", recorder.Code, recorder.Body.String())
		}
		if deletedWS != testOwnerNS+"/dev" {
			t.Fatalf("deletedWS = %q", deletedWS)
		}
		if deletedClaim != "" {
			t.Fatalf("deletedClaim = %q, want the volume left alone", deletedClaim)
		}
	})

	// An owner can only act within the namespace their own session carries,
	// unlike the admin route, which takes the namespace from the URL. There
	// is no path parameter here to smuggle a different one in.
	t.Run("a caller cannot delete another namespace's workspace", func(t *testing.T) {
		t.Parallel()
		var deletedWS string
		server, csrf := newOwnerScreenServer(t, fakeAPI{deletedWS: &deletedWS})

		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, authedFormRequest(
			"/w/dev/delete", csrf, "confirm_name=dev&delete_volume=true"))

		if recorder.Code != http.StatusFound {
			t.Fatalf("status = %d, want 302", recorder.Code)
		}
		if deletedWS != testOwnerNS+"/dev" {
			t.Fatalf("deletedWS = %q, want it scoped to the caller's own namespace", deletedWS)
		}
	})
}
