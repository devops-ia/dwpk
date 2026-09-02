package ui

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	dwpkv1alpha1 "github.com/devops-ia/dwpk/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func newAdminScreenServer(t *testing.T, api fakeAPI) (*Server, string) {
	t.Helper()
	// The merged users screen gates on a SelfSubjectAccessReview, so the fake
	// has to answer it the way the API server would for an admin.
	if api.allowedVerbs == nil {
		api.allowedVerbs = map[string]bool{"delete userspaces": true}
	}
	server := newTestServer(t)
	server.loginFlow = fakeSessionAuthenticator{
		identity: SessionIdentity{UserSpaceNamespace: testOwnerNS, Role: dwpkv1alpha1.UserSpaceRoleAdmin},
		token:    testToken,
	}
	server.clientFactory = fakeAPIClientFactory{api: api}
	csrf, err := server.csrfStore.Ensure(testSession)
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	return server, csrf
}

// countCells returns how many <td> and <th> the first table row of each kind
// has, so header and body can be compared.
func countCells(t *testing.T, body, tag string) int {
	t.Helper()
	row := regexp.MustCompile(`(?s)<tr>(.*?)</tr>`).FindAllStringSubmatch(body, -1)
	if len(row) == 0 {
		t.Fatalf("no table rows in body: %s", body)
	}
	for _, match := range row {
		if count := strings.Count(match[1], "<"+tag); count > 0 {
			return count
		}
	}
	return 0
}

// The colspan hack merged four columns into one cell, so Project, Role and
// Access labelled nothing. Every header must now have its own cell.
func TestAdminPeopleTableAlignsCellsWithHeaders(t *testing.T) {
	t.Parallel()
	server, csrf := newAdminScreenServer(t, fakeAPI{
		userSpaces: []dwpkv1alpha1.UserSpace{{
			ObjectMeta: metav1.ObjectMeta{Name: testUsername},
			Spec: dwpkv1alpha1.UserSpaceSpec{
				Owner: testOwner,
				Role:  dwpkv1alpha1.UserSpaceRoleAdmin,
			},
		}},
	})

	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, authedRequest("/admin/users", csrf))
	body := recorder.Body.String()

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, body)
	}
	if strings.Contains(body, "colspan") {
		t.Fatal("the admin table still merges cells with colspan")
	}
	headers, cells := countCells(t, body, "th"), countCells(t, body, "td")
	if headers != cells {
		t.Fatalf("%d headers but %d cells: the columns do not line up", headers, cells)
	}
	// The role renders as a selected option, so saving the row keeps it rather
	// than resetting whoever it belongs to back to the default.
	if !strings.Contains(body, `<option value="admin" selected>`) {
		t.Fatalf("the role is not selected: %s", body)
	}
}

func TestAdminWorkspacesScreenListsEveryNamespace(t *testing.T) {
	t.Parallel()
	server, csrf := newAdminScreenServer(t, fakeAPI{
		workspaces: []dwpkv1alpha1.Workspace{
			{
				ObjectMeta: metav1.ObjectMeta{Name: testWorkspaceName, Namespace: "dwpk-bob"},
				Spec:       dwpkv1alpha1.WorkspaceSpec{Running: true},
			},
			{
				ObjectMeta: metav1.ObjectMeta{Name: "lab", Namespace: testOwnerNS},
				Spec:       dwpkv1alpha1.WorkspaceSpec{Running: false},
			},
		},
	})

	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, authedRequest("/admin/workspaces", csrf))
	body := recorder.Body.String()

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, body)
	}
	for _, want := range []string{"dwpk-bob", testOwnerNS, testWorkspaceName, "lab"} {
		if !strings.Contains(body, want) {
			t.Fatalf("workspaces page missing %q", want)
		}
	}
	// Every row links to the workspace itself, which is where the controls are.
	if !strings.Contains(body, ">Open</a>") {
		t.Fatalf("no way through to a workspace: %s", body)
	}
}

func TestAdminQuotaEditPatchesTheLimits(t *testing.T) {
	t.Parallel()
	var patched dwpkv1alpha1.UserSpaceQuota
	server, csrf := newAdminScreenServer(t, fakeAPI{patchedQuota: &patched})

	request := authedFormRequest("/admin/quota/"+testUsername, csrf,
		"cpu=16&memory=64Gi&storage=200Gi&workspaces=5")
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusFound {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if patched.CPU.String() != "16" || patched.Memory.String() != "64Gi" {
		t.Fatalf("patched cpu/memory = %s/%s", patched.CPU.String(), patched.Memory.String())
	}
	if patched.Workspaces != 5 {
		t.Fatalf("patched workspaces = %d, want 5", patched.Workspaces)
	}
}

func TestAdminQuotaEditRejectsAnUnparseableQuantity(t *testing.T) {
	t.Parallel()
	var patched dwpkv1alpha1.UserSpaceQuota
	server, csrf := newAdminScreenServer(t, fakeAPI{patchedQuota: &patched})

	request := authedFormRequest("/admin/quota/"+testUsername, csrf, "cpu=plenty")
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
	if !patched.CPU.IsZero() {
		t.Fatal("patched a quota from an invalid form")
	}
}

func TestAdminCreateUserSpaceUsesDefaultsForBlankFields(t *testing.T) {
	t.Parallel()
	var created dwpkv1alpha1.UserSpace
	server, csrf := newAdminScreenServer(t, fakeAPI{createdUS: &created})

	request := authedFormRequest("/admin/userspaces", csrf, "name=dana&owner=dana@example.com")
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusFound {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if created.Name != "dana" || created.Spec.Owner != "dana@example.com" {
		t.Fatalf("created = %+v", created.Spec)
	}
	if created.Spec.Quota.Workspaces < 1 {
		t.Fatal("a blank quota produced a tenant that cannot hold a workspace")
	}
	if created.Spec.NetworkPolicy != dwpkv1alpha1.NetworkPolicyIsolated {
		t.Fatalf("networkPolicy = %q, want the isolated default", created.Spec.NetworkPolicy)
	}
}

func TestAdminCreateUserSpaceRequiresNameAndOwner(t *testing.T) {
	t.Parallel()
	var created dwpkv1alpha1.UserSpace
	server, csrf := newAdminScreenServer(t, fakeAPI{createdUS: &created})

	request := authedFormRequest("/admin/userspaces", csrf, "name=dana")
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
	if created.Name != "" {
		t.Fatal("created a UserSpace with no owner")
	}
}
