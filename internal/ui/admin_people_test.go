package ui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	dwpkv1alpha1 "github.com/devops-ia/dwpk/api/v1alpha1"
	"github.com/devops-ia/dwpk/internal/auth"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func userSpaceNamed(name, owner string) dwpkv1alpha1.UserSpace {
	return dwpkv1alpha1.UserSpace{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       dwpkv1alpha1.UserSpaceSpec{Owner: owner},
	}
}

// The join is the whole point of merging the two screens, and each of these
// three shapes used to be invisible on one screen or the other.
func TestPeopleRowsJoinsUserSpacesToLocalLogins(t *testing.T) {
	t.Parallel()
	rows := peopleRows(
		[]dwpkv1alpha1.UserSpace{
			userSpaceNamed("alice", "alice@example.com"),
			userSpaceNamed("bob", "bob@example.com"),
		},
		[]auth.LocalUser{
			{SecretName: "s1", Username: "alice", Owner: "alice@example.com"},
			{SecretName: "s2", Username: "alice-ci", Owner: "alice@example.com"},
			{SecretName: "s3", Username: "ghost", Owner: "ghost@example.com"},
		})

	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3", len(rows))
	}

	byOwner := map[string]PersonRow{}
	for _, row := range rows {
		byOwner[row.Owner] = row
	}

	// Two logins for one owner: both must be shown, not just the first.
	if summary := signInSummary(byOwner["alice@example.com"]); summary != "alice, alice-ci" {
		t.Fatalf("alice sign-in = %q, want both logins", summary)
	}
	// A UserSpace with no local login is the normal OAuth2 case.
	if summary := signInSummary(byOwner["bob@example.com"]); summary != "Identity provider" {
		t.Fatalf("bob sign-in = %q", summary)
	}
	// A login whose owner has no UserSpace cannot sign in, and must not be
	// silently dropped.
	ghost := byOwner["ghost@example.com"]
	if ghost.HasUserSpace {
		t.Fatal("ghost@example.com has no UserSpace but the row claims one")
	}
	if summary := signInSummary(ghost); summary != "ghost" {
		t.Fatalf("ghost sign-in = %q", summary)
	}
}

// Local auth is off by default. The screen must still list UserSpaces rather
// than 404-ing the way the old local-users screen did.
func TestAdminPeopleScreenWorksWithLocalAuthDisabled(t *testing.T) {
	t.Parallel()
	server, csrf := newAdminScreenServer(t, fakeAPI{
		userSpaces: []dwpkv1alpha1.UserSpace{userSpaceNamed(testUsername, testOwner)},
	})

	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, authedRequest("/admin/users", csrf))
	body := recorder.Body.String()

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, body)
	}
	if !strings.Contains(body, testOwner) {
		t.Fatalf("the UserSpace is missing: %s", body)
	}
	// With no local user store there is nothing to create a password against.
	if strings.Contains(body, "Add a password sign-in") {
		t.Fatalf("offered a local login form with local auth disabled: %s", body)
	}
}

// A non-admin used to reach /admin/userspaces and see whatever the API server
// let through. The merged screen refuses before rendering anything.
func TestAdminPeopleScreenRefusesANonAdmin(t *testing.T) {
	t.Parallel()
	server, csrf := newAdminScreenServer(t, fakeAPI{allowedVerbs: map[string]bool{}})

	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, authedRequest("/admin/users", csrf))

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", recorder.Code)
	}
}

// A role of manager grants nothing on its own. The screen has to say so, or an
// administrator sets the role, believes the job is done, and the person walks

// An ordinary user is never flagged: the warning is about a role that promises
