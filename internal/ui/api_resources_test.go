package ui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	dwpkv1alpha1 "github.com/devops-ia/dwpk/api/v1alpha1"
	"github.com/devops-ia/dwpk/internal/auth"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// apiServer returns the server alone: every remaining spec here reads, and a
// read needs no CSRF token.
func apiServer(t *testing.T, identity SessionIdentity, api fakeAPI) *Server {
	t.Helper()
	server := newTestServer(t)
	server.loginFlow = fakeSessionAuthenticator{identity: identity, token: testToken}
	server.clientFactory = fakeAPIClientFactory{api: api}
	_, _ = server.csrfStore.Ensure(testSession)
	return server
}

// The API used to list session.Identity.UserSpaceNamespace whatever the role,
// so an admin saw fewer workspaces through the API than through the browser.
// One identity must not get two answers to the same question.
func TestAPIListWorkspacesScopesByRoleLikeTheScreens(t *testing.T) {
	t.Parallel()

	all := []dwpkv1alpha1.Workspace{
		workspaceIn(testOwnerNS, "mine"),
		workspaceIn("dwpk-bob", "theirs"),
		workspaceIn("dwpk-carol", "elsewhere"),
	}

	tests := []struct {
		name string
		role string
		want []string
	}{
		{name: "an admin lists the cluster", role: dwpkv1alpha1.UserSpaceRoleAdmin,
			want: []string{"mine", "theirs", "elsewhere"}},
		{name: "a plain user lists only their own namespace", role: dwpkv1alpha1.UserSpaceRoleUser,
			want: []string{"mine"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := apiServer(t,
				SessionIdentity{UserSpaceNamespace: testOwnerNS, Email: testOwner, Role: test.role},
				fakeAPI{workspaces: all})

			recorder := httptest.NewRecorder()
			server.Handler().ServeHTTP(recorder, authedRequest(apiVersion+"/workspaces", ""))

			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
			}
			for _, want := range test.want {
				if !strings.Contains(recorder.Body.String(), `"`+want+`"`) {
					t.Fatalf("%s missing from the list: %s", want, recorder.Body.String())
				}
			}
			if test.role == dwpkv1alpha1.UserSpaceRoleUser && strings.Contains(recorder.Body.String(), "elsewhere") {
				t.Fatalf("a plain user was shown another namespace: %s", recorder.Body.String())
			}
		})
	}
}

func TestAPIWorkspaceLogs(t *testing.T) {
	t.Parallel()

	running := &dwpkv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: testWorkspaceName, Namespace: testOwnerNS},
		Status:     dwpkv1alpha1.WorkspaceStatus{State: dwpkv1alpha1.WorkspaceStateRunning, PodName: testPodName},
	}
	suspended := running.DeepCopy()
	suspended.Status.PodName = ""

	t.Run("returns the tail", func(t *testing.T) {
		t.Parallel()
		server := apiServer(t, SessionIdentity{UserSpaceNamespace: testOwnerNS},
			fakeAPI{workspace: running, logs: testLogLine})

		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder,
			authedRequest(apiVersion+"/workspaces/dev/logs", ""))

		var got LogsResponse
		if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode: %v (%s)", err, recorder.Body.String())
		}
		if got.Lines != testLogLine || got.Pod != testPodName {
			t.Fatalf("response = %+v", got)
		}
	})

	// A stopped workspace is not an error in the log stream; it is a state the
	// caller can fix. 409 says so; a 500 would not.
	t.Run("a stopped workspace is a conflict, not a failure", func(t *testing.T) {
		t.Parallel()
		server := apiServer(t, SessionIdentity{UserSpaceNamespace: testOwnerNS},
			fakeAPI{workspace: suspended})

		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder,
			authedRequest(apiVersion+"/workspaces/dev/logs", ""))

		if recorder.Code != http.StatusConflict {
			t.Fatalf("status = %d, want 409: %s", recorder.Code, recorder.Body.String())
		}
	})

	t.Run("rejects a nonsense tail", func(t *testing.T) {
		t.Parallel()
		server := apiServer(t, SessionIdentity{UserSpaceNamespace: testOwnerNS},
			fakeAPI{workspace: running})

		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder,
			authedRequest(apiVersion+"/workspaces/dev/logs?tail=-5", ""))

		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", recorder.Code)
		}
	})
}

// The REST API shares localUsernameFor with the browser screens, so it has to
// refuse the same request for the same reason: holding a local account is not
// the same as having signed in with one.
func TestAPIChangePasswordRejectsAProviderLogin(t *testing.T) {
	t.Parallel()

	localUsers := &fakeLocalUsers{
		password: "old-password",
		users:    []auth.LocalUser{{SecretName: "s1", Username: testUsername, Owner: testOwner}},
	}
	identity := SessionIdentity{
		Email:              testOwner,
		UserSpaceName:      testUsername,
		UserSpaceNamespace: testOwnerNS,
		Role:               dwpkv1alpha1.UserSpaceRoleUser,
	}
	server := apiServer(t, identity, fakeAPI{})
	server.localUsers = localUsers
	csrf, err := server.csrfStore.Ensure(testSession)
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}

	body := `{"current_password":"old-password","new_password":"new-password"}`
	request := httptest.NewRequest(http.MethodPost, "/api/v1/profile/password", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(csrfHeaderName, csrf)
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: testSession})

	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409, body = %s", recorder.Code, recorder.Body.String())
	}
	if localUsers.password != "old-password" {
		t.Fatalf("password = %q, want it unchanged", localUsers.password)
	}
}
