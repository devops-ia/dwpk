package ui

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/devops-ia/dwpk/internal/auth"
)

// Without a cap here, a request body of any size reaches a handler's
// ParseForm/ParseMultipartForm before either ever looks at a form-specific
// memory limit - this is the one place every request passes through,
// authenticated or not, so it is where the cap belongs.
func TestWithSecurityHeadersCapsRequestBodySize(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)

	oversized := bytes.Repeat([]byte("a"), maxRequestBodyBytes+1)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(oversized))

	server.withSecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.ReadAll(r.Body)
		if err == nil {
			t.Fatal("reading an oversized body succeeded; want it capped by MaxBytesReader")
		}
	})).ServeHTTP(recorder, request)
}

func TestWithSecurityHeadersAllowsAnOrdinaryBody(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)

	body := []byte("field=value")
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))

	server.withSecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("ReadAll() error = %v", err)
		}
		if string(got) != string(body) {
			t.Fatalf("body = %q, want %q", got, body)
		}
	})).ServeHTTP(recorder, request)
}

func TestWithSessionRedirectsWhenCookieMissing(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	server.withSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next handler should not run")
	})).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusFound {
		t.Fatalf("status = %d", recorder.Code)
	}
}

func TestWithSessionAddsRequestSession(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	server.loginFlow = fakeSessionAuthenticator{identity: SessionIdentity{UserSpaceNamespace: "dwpk-alice"}, token: "minted"}
	csrf, err := server.csrfStore.Ensure("session-1")
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "session-1"})
	server.withSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		session, ok := requestSessionFrom(r.Context())
		if !ok {
			t.Fatal("session missing from context")
		}
		if session.Token != "minted" || session.CSRFToken != csrf {
			t.Fatalf("unexpected session: %#v", session)
		}
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d", recorder.Code)
	}
}

func TestWithSessionRejectsBadCSRF(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	server.loginFlow = fakeSessionAuthenticator{identity: SessionIdentity{UserSpaceNamespace: "dwpk-alice"}, token: "minted"}
	if _, err := server.csrfStore.Ensure("session-1"); err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/new", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "session-1"})
	server.withSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next handler should not run")
	})).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d", recorder.Code)
	}
}

// TestWithSessionAcceptsMultipartCSRF reproduces the bug where every save of
// the (multipart, because it carries an optional logo file) global settings
// form was rejected with "invalid CSRF token": ParseForm silently does not
// read a multipart body, so the token was always read as empty before this
// fix landed.
func TestWithSessionAcceptsMultipartCSRF(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	server.loginFlow = fakeSessionAuthenticator{identity: SessionIdentity{UserSpaceNamespace: "dwpk-alice"}, token: "minted"}
	csrf, err := server.csrfStore.Ensure("session-1")
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("csrf_token", csrf); err != nil {
		t.Fatalf("WriteField() error = %v", err)
	}
	if err := writer.WriteField("name", "dwpk"); err != nil {
		t.Fatalf("WriteField() error = %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/admin/settings", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "session-1"})

	called := false
	server.withSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(recorder, request)

	if !called {
		t.Fatal("next handler should have run")
	}
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d", recorder.Code)
	}
}

type fakeSessionAuthenticator struct {
	identity      SessionIdentity
	token         string
	localAuth     bool
	localLoginErr error
	providers     []auth.Name
	// markedOnboarding records the sessionID passed to MarkOnboardingCompleted,
	// if a test wants to assert it was called. A pointer so the write is
	// visible to the test despite the value receiver below.
	markedOnboarding *string
	// onboardingPending backs SessionIdentity's OnboardingPending field
	// through a pointer, so a test can observe MarkOnboardingCompleted
	// actually flipping what a later SessionIdentity call returns — the same
	// round-trip the real LoginFlow's identity cache provides.
	onboardingPending *bool
}

func (f fakeSessionAuthenticator) BeginLogin(provider auth.Name) (BeginLoginResult, error) {
	return BeginLoginResult{}, nil
}

func (f fakeSessionAuthenticator) CompleteLogin(ctx context.Context, req CompleteLoginRequest) (string, error) {
	return "session-1", nil
}

func (f fakeSessionAuthenticator) CompleteLocalLogin(ctx context.Context, req LocalLoginRequest) (string, error) {
	if f.localLoginErr != nil {
		return "", f.localLoginErr
	}
	return "session-1", nil
}

func (f fakeSessionAuthenticator) LocalAuthEnabled() bool {
	return f.localAuth
}

func (f fakeSessionAuthenticator) ConfiguredProviders() []auth.Name {
	return f.providers
}

func (f fakeSessionAuthenticator) MintTokenForSession(ctx context.Context, sessionID string) (string, error) {
	return f.token, nil
}

func (f fakeSessionAuthenticator) Logout(sessionID string) error {
	return nil
}

func (f fakeSessionAuthenticator) SessionIdentity(sessionID string) (SessionIdentity, error) {
	identity := f.identity
	if f.onboardingPending != nil {
		identity.OnboardingPending = *f.onboardingPending
	}
	return identity, nil
}

func (f fakeSessionAuthenticator) MarkOnboardingCompleted(sessionID string) {
	if f.markedOnboarding != nil {
		*f.markedOnboarding = sessionID
	}
	if f.onboardingPending != nil {
		*f.onboardingPending = false
	}
}

func newTestServer(t *testing.T) *Server {
	t.Helper()
	server, err := NewServer(ServerConfig{
		LoginFlow:     fakeSessionAuthenticator{},
		ClientFactory: fakeAPIClientFactory{api: fakeAPI{}},
		GatewayHost:   "dwpk.example.com",
		SessionTTL:    time.Minute,
	})
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	return server
}

// The wizard is an invitation, not a gate. It used to be enforced by a
// middleware that rewrote every request, which meant its own links — "Add an
// SSH key", "Browse the catalog" — bounced straight back to step 1. Whether
// someone has finished it now changes only where a login lands and whether the
// sidebar offers a way in.
func TestAPendingWizardBlocksNothing(t *testing.T) {
	t.Parallel()
	pending := true
	server := newTestServer(t)
	server.loginFlow = fakeSessionAuthenticator{onboardingPending: &pending}

	for _, path := range []string{"/", catalogPath, onboardingPath} {
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, authedRequest(http.MethodGet, path, ""))
		if recorder.Code == http.StatusFound {
			t.Errorf("%s redirected to %q with the wizard unfinished", path, recorder.Header().Get("Location"))
		}
	}
}
