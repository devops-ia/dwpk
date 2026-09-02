package ui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	dwpkv1alpha1 "github.com/devops-ia/dwpk/api/v1alpha1"
	"github.com/devops-ia/dwpk/internal/auth"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func newProfileServer(t *testing.T, localUsers *fakeLocalUsers) (*Server, string) {
	t.Helper()
	server := newTestServer(t)
	server.loginFlow = fakeSessionAuthenticator{
		identity: SessionIdentity{
			Email:              testOwner,
			UserSpaceName:      testUsername,
			UserSpaceNamespace: testOwnerNS,
			Role:               dwpkv1alpha1.UserSpaceRoleUser,
			LocalLogin:         true,
		},
		token: testToken,
	}
	server.clientFactory = fakeAPIClientFactory{api: fakeAPI{
		userSpaces: []dwpkv1alpha1.UserSpace{{
			ObjectMeta: metav1.ObjectMeta{Name: testUsername},
			Spec: dwpkv1alpha1.UserSpaceSpec{
				Owner: testOwner,
				Quota: dwpkv1alpha1.UserSpaceQuota{
					CPU:        resource.MustParse("4"),
					Memory:     resource.MustParse("8Gi"),
					Storage:    resource.MustParse("50Gi"),
					Workspaces: 2,
				},
			},
			Status: dwpkv1alpha1.UserSpaceStatus{Namespace: testOwnerNS},
		}},
	}}
	if localUsers != nil {
		server.localUsers = localUsers
	}
	csrf, err := server.csrfStore.Ensure(testSession)
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	return server, csrf
}

func TestProfileShowsIdentityAndQuota(t *testing.T) {
	t.Parallel()
	server, csrf := newProfileServer(t, nil)

	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, authedRequest(http.MethodGet, "/profile", csrf))
	body := recorder.Body.String()

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, body)
	}
	for _, want := range []string{testOwner, testOwnerNS, "8Gi", "50Gi"} {
		if !strings.Contains(body, want) {
			t.Fatalf("profile missing %q: %s", want, body)
		}
	}
	// An OAuth-only account has no password here, and must not be shown a
	// form that cannot work.
	if strings.Contains(body, `name="current_password"`) {
		t.Fatalf("password form rendered for an account with no local user: %s", body)
	}
}

func TestProfileSettingsTabShowsThemeChoiceFromCookie(t *testing.T) {
	t.Parallel()
	server, csrf := newProfileServer(t, nil)

	request := authedRequest(http.MethodGet, "/profile", csrf)
	request.AddCookie(&http.Cookie{Name: themeCookieName, Value: themeDark})
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	body := recorder.Body.String()

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, body)
	}
	if !strings.Contains(body, `data-tab-panel="settings"`) {
		t.Fatalf("settings tab panel missing: %s", body)
	}
	if !strings.Contains(body, `<select id="settings-theme" name="theme" data-theme-option>`) {
		t.Fatalf("theme control is not a dropdown: %s", body)
	}
	if !strings.Contains(body, `<option value="dark" selected>`) {
		t.Fatalf("dark option not selected for dark cookie: %s", body)
	}
	if strings.Contains(body, `<option value="light" selected>`) {
		t.Fatalf("light option selected despite dark cookie: %s", body)
	}
	if !strings.Contains(body, `<select id="settings-language" disabled>`) {
		t.Fatalf("language select missing or not disabled: %s", body)
	}
}

func TestProfileSettingsTabDefaultsToSystemWithNoCookie(t *testing.T) {
	t.Parallel()
	server, csrf := newProfileServer(t, nil)

	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, authedRequest(http.MethodGet, "/profile", csrf))
	body := recorder.Body.String()

	if !strings.Contains(body, `<option value="system" selected>`) {
		t.Fatalf("system option not selected with no theme cookie: %s", body)
	}
}

func TestProfileChangePasswordActsOnTheSessionOwnerOnly(t *testing.T) {
	t.Parallel()
	localUsers := &fakeLocalUsers{
		password: "old-password",
		users: []auth.LocalUser{
			{SecretName: "s1", Username: testUsername, Owner: testOwner},
			{SecretName: "s2", Username: "mallory", Owner: "mallory@example.com"},
		},
	}
	server, csrf := newProfileServer(t, localUsers)

	// A posted username must be ignored: the owner comes from the session.
	form := "current_password=old-password&new_password=new-password&confirm_password=new-password&username=mallory"
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, authedFormRequest("/profile/password", csrf, form))

	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if loc := recorder.Header().Get("Location"); !strings.Contains(loc, "done=password") || !strings.HasSuffix(loc, "#password") {
		t.Fatalf("Location = %q, want a redirect back to the password tab with a done notice", loc)
	}
	if localUsers.password != "new-password" {
		t.Fatalf("password = %q, want the change to have applied to the session owner", localUsers.password)
	}
}

func TestProfileChangePasswordRejectsAMismatchedConfirmation(t *testing.T) {
	t.Parallel()
	localUsers := &fakeLocalUsers{
		password: "old-password",
		users:    []auth.LocalUser{{SecretName: "s1", Username: testUsername, Owner: testOwner}},
	}
	server, csrf := newProfileServer(t, localUsers)

	form := "current_password=old-password&new_password=one&confirm_password=two"
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, authedFormRequest("/profile/password", csrf, form))

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
	if localUsers.password != "old-password" {
		t.Fatalf("password = %q, want it unchanged", localUsers.password)
	}
}

// signInThroughProvider rewrites the test server's session so it looks like an
// OAuth2 login: same person, same owner email, but not a local login.
func signInThroughProvider(server *Server) {
	flow := server.loginFlow.(fakeSessionAuthenticator)
	flow.identity.LocalLogin = false
	server.loginFlow = flow
}

func TestProfileHidesThePasswordFormForAProviderLogin(t *testing.T) {
	t.Parallel()
	// The owner also holds a local account. Holding one is not the same as
	// having signed in with it, and only the latter may change a password here.
	localUsers := &fakeLocalUsers{
		password: "old-password",
		users:    []auth.LocalUser{{SecretName: "s1", Username: testUsername, Owner: testOwner}},
	}
	server, csrf := newProfileServer(t, localUsers)
	signInThroughProvider(server)

	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, authedRequest(http.MethodGet, "/profile", csrf))
	body := recorder.Body.String()

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, body)
	}
	if strings.Contains(body, `name="current_password"`) {
		t.Fatalf("password form rendered for a provider login: %s", body)
	}
	if !strings.Contains(body, "You sign in through an identity provider") {
		t.Fatalf("identity provider notice missing: %s", body)
	}
}

func TestProfileChangePasswordRejectsAProviderLogin(t *testing.T) {
	t.Parallel()
	localUsers := &fakeLocalUsers{
		password: "old-password",
		users:    []auth.LocalUser{{SecretName: "s1", Username: testUsername, Owner: testOwner}},
	}
	server, csrf := newProfileServer(t, localUsers)
	signInThroughProvider(server)

	form := "current_password=old-password&new_password=new-password&confirm_password=new-password"
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, authedFormRequest("/profile/password", csrf, form))

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", recorder.Code)
	}
	if localUsers.password != "old-password" {
		t.Fatalf("password = %q, want it unchanged", localUsers.password)
	}
}
