package ui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/devops-ia/dwpk/internal/auth"
)

type fakeTokenStore struct {
	issued    auth.TokenGrant
	records   []auth.TokenRecord
	lookup    auth.TokenRecord
	lookupErr error
	revoked   []string
}

func (f *fakeTokenStore) Issue(ctx context.Context, grant auth.TokenGrant) (auth.TokenRecord, error) {
	f.issued = grant
	return auth.TokenRecord{
		SecretName:            "dwpk-token-new",
		SubjectNamespace:      grant.SubjectNamespace,
		SubjectServiceAccount: grant.SubjectServiceAccount,
		ExpiresAt:             grant.ExpiresAt,
		Plaintext:             "dwpk_new",
	}, nil
}

func (f *fakeTokenStore) Lookup(ctx context.Context, plaintext string) (auth.TokenRecord, error) {
	if f.lookupErr != nil {
		return auth.TokenRecord{}, f.lookupErr
	}
	return f.lookup, nil
}

func (f *fakeTokenStore) List(ctx context.Context, kind auth.TokenKind, namespace string) ([]auth.TokenRecord, error) {
	matching := []auth.TokenRecord{}
	for _, record := range f.records {
		if namespace == "" || record.SubjectNamespace == namespace {
			matching = append(matching, record)
		}
	}
	return matching, nil
}

func (f *fakeTokenStore) Revoke(ctx context.Context, secretName string) error {
	f.revoked = append(f.revoked, secretName)
	return nil
}

type fakeMinter struct {
	token     string
	namespace string
}

func (f *fakeMinter) Mint(ctx context.Context, namespace, serviceAccount string) (string, time.Time, error) {
	f.namespace = namespace
	return f.token, time.Now().Add(time.Hour), nil
}

type fakeLocalUsers struct {
	password string
	users    []auth.LocalUser
	deleted  []string
}

func (f *fakeLocalUsers) Create(ctx context.Context, username, password, owner string) (auth.LocalUser, error) {
	if username == "" {
		return auth.LocalUser{}, auth.ErrEmptyUsername
	}
	for _, user := range f.users {
		if user.Username == username {
			return auth.LocalUser{}, auth.ErrLocalUserExists
		}
	}
	user := auth.LocalUser{SecretName: "dwpk-local-user-x", Username: username, Owner: owner}
	f.users = append(f.users, user)
	return user, nil
}

func (f *fakeLocalUsers) List(ctx context.Context) ([]auth.LocalUser, error) { return f.users, nil }

func (f *fakeLocalUsers) Delete(ctx context.Context, secretName string) error {
	f.deleted = append(f.deleted, secretName)
	return nil
}

func (f *fakeLocalUsers) FindByOwner(ctx context.Context, owner string) ([]auth.LocalUser, error) {
	matching := make([]auth.LocalUser, 0, 1)
	for _, user := range f.users {
		if user.Owner == owner {
			matching = append(matching, user)
		}
	}
	return matching, nil
}

func (f *fakeLocalUsers) ResetPassword(ctx context.Context, username, newPassword string) error {
	for _, user := range f.users {
		if user.Username == username {
			f.password = newPassword
			return nil
		}
	}
	return auth.ErrLocalUserNotFound
}

func (f *fakeLocalUsers) SetPassword(ctx context.Context, username, currentPassword, newPassword string) error {
	for _, user := range f.users {
		if user.Username == username {
			if currentPassword != f.password {
				return auth.ErrInvalidCredentials
			}
			f.password = newPassword
			return nil
		}
	}
	return auth.ErrLocalUserNotFound
}

const (
	testOwner         = "alice@example.com"
	testSession       = "session-1"
	testNamespace     = "dwpk-bob"
	testTokenName     = "dwpk-token-alice"
	testUsername      = "alice"
	testOwnerNS       = "dwpk-alice"
	testToken         = "minted"
	testProject       = "platform"
	testProjectAlt    = "research"
	testWorkspaceName = "dev"
	testImageName     = "python"
	testLogLine       = "listening on :22\n"
	testPodName       = "dev-0"
	testProjectTitle  = "Platform"
)

func newAPITestServer(t *testing.T) *Server {
	t.Helper()
	server := newTestServer(t)
	server.loginFlow = fakeSessionAuthenticator{identity: SessionIdentity{UserSpaceNamespace: testOwnerNS, Email: testOwner}, token: testToken}
	return server
}

func bearerRequest(method, path, token string) *http.Request {
	request := httptest.NewRequest(method, path, nil)
	request.Header.Set("Authorization", "Bearer "+token)
	return request
}

// A dwpk_ bearer token must reach the API server as a freshly minted
// ServiceAccount token for the namespace the token was issued to, never as
// the stored token itself.
func TestAPIBearerTokenMintsForItsOwnNamespace(t *testing.T) {
	t.Parallel()
	server := newAPITestServer(t)
	minter := &fakeMinter{token: "minted-for-bob"}
	server.apiTokens = &fakeTokenStore{lookup: auth.TokenRecord{
		SecretName: "dwpk-token-1", SubjectNamespace: testNamespace, SubjectServiceAccount: "workspace",
	}}
	server.tokenMinter = minter

	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, bearerRequest(http.MethodGet, "/api/v1/session", "dwpk_abc"))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if minter.namespace != testNamespace {
		t.Fatalf("minted for namespace %q, want dwpk-bob", minter.namespace)
	}
	var body SessionResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.UserSpaceNamespace != testNamespace {
		t.Fatalf("namespace = %q", body.UserSpaceNamespace)
	}
}

func TestAPIRejectsUnknownAndForeignBearerTokens(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		token string
		store APITokenStore
	}{
		{name: "unknown dwpk token", token: "dwpk_nope", store: &fakeTokenStore{lookupErr: auth.ErrTokenNotFound}},
		{name: "not a dwpk token", token: "eyJhbGciOi.some.jwt", store: &fakeTokenStore{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := newAPITestServer(t)
			server.apiTokens = test.store
			server.tokenMinter = &fakeMinter{token: testToken}

			recorder := httptest.NewRecorder()
			server.Handler().ServeHTTP(recorder, bearerRequest(http.MethodGet, "/api/v1/session", test.token))

			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401, body = %s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

// An unauthenticated API call must answer 401 in JSON. The HTML routes
// redirect to the login page, which a script client cannot act on.
func TestAPIWithoutCredentialsReturnsJSONUnauthorized(t *testing.T) {
	t.Parallel()
	server := newAPITestServer(t)

	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/workspaces", nil))

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", recorder.Code)
	}
	if contentType := recorder.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
		t.Fatalf("Content-Type = %q, want JSON", contentType)
	}
}

// Cookie-authenticated writes stay behind the CSRF check: unlike a bearer
// header, a cookie is attached by the browser on cross-site requests.
func TestAPICookieWriteRequiresCSRFToken(t *testing.T) {
	t.Parallel()
	server := newAPITestServer(t)
	if _, err := server.csrfStore.Ensure(testSession); err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces", strings.NewReader(`{"name":"dev"}`))
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: testSession})
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", recorder.Code)
	}
}

func TestAPICookieWriteSucceedsWithCSRFToken(t *testing.T) {
	t.Parallel()
	server := newAPITestServer(t)
	server.clientFactory = fakeAPIClientFactory{api: fakeAPI{}}
	csrf, err := server.csrfStore.Ensure(testSession)
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}

	body := `{"name":"dev","image":"python","cpu":"1","memory":"2Gi","storage":"20Gi","ssh_public_key":"ssh-ed25519 AAAA"}`
	request := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces", strings.NewReader(body))
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: testSession})
	request.Header.Set(csrfHeaderName, csrf)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

// DELETE /api/v1/workspaces/{name} deletes the home volume by default,
// matching the UI's own confirmation dialog default - an option has to be
// explicitly turned off, never explicitly turned on, or the two surfaces
// would disagree about what "delete" means.
func TestAPIDeleteWorkspaceDeletesVolumeByDefault(t *testing.T) {
	t.Parallel()
	var deletedWS, deletedClaim string
	server := newAPITestServer(t)
	server.clientFactory = fakeAPIClientFactory{api: fakeAPI{
		deletedWS: &deletedWS, deletedClaim: &deletedClaim,
	}}
	csrf, err := server.csrfStore.Ensure(testSession)
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}

	request := httptest.NewRequest(http.MethodDelete, "/api/v1/workspaces/dev", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: testSession})
	request.Header.Set(csrfHeaderName, csrf)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if deletedWS != testOwnerNS+"/dev" {
		t.Fatalf("deletedWS = %q", deletedWS)
	}
	if deletedClaim != testOwnerNS+"/home-dev-0" {
		t.Fatalf("deletedClaim = %q, want the home volume deleted too", deletedClaim)
	}
}

// delete_volume=false must be honoured: leaving the flag out of a client's
// request should never be the only way to keep a PVC.
func TestAPIDeleteWorkspaceCanKeepVolume(t *testing.T) {
	t.Parallel()
	var deletedWS, deletedClaim string
	server := newAPITestServer(t)
	server.clientFactory = fakeAPIClientFactory{api: fakeAPI{
		deletedWS: &deletedWS, deletedClaim: &deletedClaim,
	}}
	csrf, err := server.csrfStore.Ensure(testSession)
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}

	request := httptest.NewRequest(http.MethodDelete, "/api/v1/workspaces/dev?delete_volume=false", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: testSession})
	request.Header.Set(csrfHeaderName, csrf)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if deletedWS != testOwnerNS+"/dev" {
		t.Fatalf("deletedWS = %q", deletedWS)
	}
	if deletedClaim != "" {
		t.Fatalf("deletedClaim = %q, want the volume left alone", deletedClaim)
	}
}

// The login endpoint hands back the CSRF token in a header so a script
// client can make writes without a second round trip to /session.
func TestAPILocalLoginSetsCookieAndCSRFHeader(t *testing.T) {
	t.Parallel()
	server := newAPITestServer(t)
	server.loginFlow = fakeSessionAuthenticator{identity: SessionIdentity{UserSpaceNamespace: testOwnerNS}, token: testToken, localAuth: true}

	request := httptest.NewRequest(http.MethodPost, "/api/v1/login", strings.NewReader(`{"username":"alice","password":"hunter2"}`))
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get(csrfHeaderName) == "" {
		t.Fatal("missing CSRF token header")
	}
	cookies := recorder.Result().Cookies()
	if len(cookies) == 0 || cookies[0].Name != sessionCookieName || cookies[0].Value == "" {
		t.Fatalf("missing session cookie: %#v", cookies)
	}
	if !cookies[0].HttpOnly {
		t.Fatal("session cookie must be HttpOnly")
	}
}

func TestAPILocalLoginRejectsBadPassword(t *testing.T) {
	t.Parallel()
	server := newAPITestServer(t)
	server.loginFlow = fakeSessionAuthenticator{localAuth: true, localLoginErr: errors.New("bad password")}

	request := httptest.NewRequest(http.MethodPost, "/api/v1/login", strings.NewReader(`{"username":"alice","password":"wrong"}`))
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", recorder.Code)
	}
	// The reply must not distinguish an unknown user from a wrong password.
	if strings.Contains(recorder.Body.String(), "bad password") {
		t.Fatalf("error body leaks the underlying reason: %s", recorder.Body.String())
	}
}

func TestAPILocalLoginDisabledWhenLocalAuthOff(t *testing.T) {
	t.Parallel()
	server := newAPITestServer(t)

	request := httptest.NewRequest(http.MethodPost, "/api/v1/login", strings.NewReader(`{"username":"alice","password":"x"}`))
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", recorder.Code)
	}
}

// Token Secrets all live in one namespace the caller cannot read, so the
// only thing stopping cross-tenant revocation is this ownership check.
func TestAPIRevokeTokenRefusesAnotherNamespacesToken(t *testing.T) {
	t.Parallel()
	server := newAPITestServer(t)
	store := &fakeTokenStore{records: []auth.TokenRecord{
		{SecretName: "dwpk-token-bob", SubjectNamespace: testNamespace},
		{SecretName: testTokenName, SubjectNamespace: testOwnerNS},
	}}
	server.apiTokens = store
	csrf, err := server.csrfStore.Ensure(testSession)
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}

	request := httptest.NewRequest(http.MethodDelete, "/api/v1/tokens/dwpk-token-bob", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: testSession})
	request.Header.Set(csrfHeaderName, csrf)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", recorder.Code)
	}
	if len(store.revoked) != 0 {
		t.Fatalf("revoked another namespace's token: %v", store.revoked)
	}
}

func TestAPIRevokeTokenAllowsOwnToken(t *testing.T) {
	t.Parallel()
	server := newAPITestServer(t)
	store := &fakeTokenStore{records: []auth.TokenRecord{{SecretName: testTokenName, SubjectNamespace: testOwnerNS}}}
	server.apiTokens = store
	csrf, err := server.csrfStore.Ensure(testSession)
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}

	request := httptest.NewRequest(http.MethodDelete, "/api/v1/tokens/dwpk-token-alice", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: testSession})
	request.Header.Set(csrfHeaderName, csrf)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if len(store.revoked) != 1 || store.revoked[0] != testTokenName {
		t.Fatalf("revoked = %v", store.revoked)
	}
}

// Local user management is gated on the caller's own RBAC, so a
// non-administrator must be refused even though the store is configured.
func TestAPILocalUsersRequiresAdminRBAC(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		allowed    map[string]bool
		wantStatus int
	}{
		{name: "non-admin", allowed: map[string]bool{}, wantStatus: http.StatusForbidden},
		{name: "admin", allowed: map[string]bool{"delete userspaces": true}, wantStatus: http.StatusOK},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := newAPITestServer(t)
			server.localUsers = &fakeLocalUsers{users: []auth.LocalUser{{SecretName: "s1", Username: testUsername, Owner: testOwner}}}
			server.clientFactory = fakeAPIClientFactory{api: fakeAPI{allowedVerbs: test.allowed}}
			if _, err := server.csrfStore.Ensure(testSession); err != nil {
				t.Fatalf("Ensure() error = %v", err)
			}

			request := httptest.NewRequest(http.MethodGet, "/api/v1/admin/local-users", nil)
			request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: testSession})
			recorder := httptest.NewRecorder()
			server.Handler().ServeHTTP(recorder, request)

			if recorder.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d, body = %s", recorder.Code, test.wantStatus, recorder.Body.String())
			}
		})
	}
}

func TestAPILocalUsersDisabledWithoutStore(t *testing.T) {
	t.Parallel()
	server := newAPITestServer(t)
	server.clientFactory = fakeAPIClientFactory{api: fakeAPI{allowedVerbs: map[string]bool{"delete userspaces": true}}}
	if _, err := server.csrfStore.Ensure(testSession); err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}

	request := httptest.NewRequest(http.MethodGet, "/api/v1/admin/local-users", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: testSession})
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", recorder.Code)
	}
}

// Bearer authentication must stay unavailable rather than silently falling
// back to something weaker when the token store is not configured.
func TestAPIBearerRejectedWhenTokenStoreAbsent(t *testing.T) {
	t.Parallel()
	server := newAPITestServer(t)

	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, bearerRequest(http.MethodGet, "/api/v1/session", "dwpk_abc"))

	if recorder.Code != http.StatusInternalServerError && recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want a refusal", recorder.Code)
	}
	if strings.Contains(recorder.Body.String(), "\"namespace\"") {
		t.Fatalf("request was served despite no token store: %s", recorder.Body.String())
	}
}
