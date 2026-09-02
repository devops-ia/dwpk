package ui

import (
	"net/http/httptest"
	"strings"
	"testing"

	dwpkv1alpha1 "github.com/devops-ia/dwpk/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Every delete must be a question before it is an action. The property under
// test is not the wording but the shape: the button on the page opens a dialog,
// and only the form inside that dialog posts.
func TestDeleteIsAlwaysConfirmedFirst(t *testing.T) {
	t.Parallel()

	server, csrf := newAdminScreenServer(t, fakeAPI{
		workspaces: []dwpkv1alpha1.Workspace{{
			ObjectMeta: metav1.ObjectMeta{Name: testWorkspaceName, Namespace: testOwnerNS},
			Spec:       dwpkv1alpha1.WorkspaceSpec{ImageRef: dwpkv1alpha1.WorkspaceImageReference{Name: testImageName}},
			Status:     dwpkv1alpha1.WorkspaceStatus{State: dwpkv1alpha1.WorkspaceStateRunning},
		}},
		userSpaces: []dwpkv1alpha1.UserSpace{{
			ObjectMeta: metav1.ObjectMeta{Name: testUsername},
			Spec:       dwpkv1alpha1.UserSpaceSpec{Owner: testOwner, Role: dwpkv1alpha1.UserSpaceRoleAdmin},
		}},
	})

	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, authedRequest("/admin/workspaces", csrf))
	body := recorder.Body.String()

	if !strings.Contains(body, `data-dialog-open="delete-ws-dwpk-alice-dev"`) {
		t.Fatalf("delete does not open a dialog: %s", body)
	}
	if !strings.Contains(body, `<dialog id="delete-ws-dwpk-alice-dev"`) {
		t.Fatalf("no confirmation dialog rendered: %s", body)
	}

	// The home volume is named, and the dialog says what happens to it. It is
	// deleted by default now, so the sentence has to say that: a StatefulSet
	// would never remove it, and somebody reading a stale reassurance would lose
	// a home directory believing it was safe.
	if !strings.Contains(body, "home-dev-0") || !strings.Contains(body, "unless you untick") {
		t.Fatalf("the dialog does not say what happens to the home volume: %s", body)
	}
	// Ticked by default, as asked, and typed confirmation before it can fire.
	if !strings.Contains(body, `name="delete_volume" value="true" checked`) {
		t.Error("the volume checkbox is not ticked by default")
	}
	if !strings.Contains(body, `data-confirm-name="dev"`) {
		t.Error("the dialog does not require the name to be typed")
	}

	// A <label> is a grid, so a bare "write <strong>dev</strong> to confirm"
	// puts each of its three inline children on its own row and the sentence
	// reads as a vertical list. It has to be one element to be one line.
	const prompt = `<span class="confirm-prompt">Remove persistent volume immediately, ` +
		`write <strong>dev</strong> to confirm</span>`
	if !strings.Contains(body, prompt) {
		t.Errorf("the confirmation prompt is not a single line: %s", body)
	}

	// The POST itself must live inside the dialog, not beside the trigger.
	trigger := strings.Index(body, `data-dialog-open="delete-ws-dwpk-alice-dev"`)
	dialog := strings.Index(body, `<dialog id="delete-ws-dwpk-alice-dev"`)
	post := strings.Index(body, "/admin/workspaces/dwpk-alice/dev/delete")
	if trigger >= dialog || dialog >= post {
		t.Fatalf("the delete form is not inside the dialog (trigger=%d dialog=%d post=%d)", trigger, dialog, post)
	}
}

// The same dialog exists twice, on the admin table and on a user's own
// workspace page. The user only ever sees the second one, so a fix verified
// against the first proves nothing about what they reported.
func TestWorkspacePageDeletePromptIsOneLine(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	server.loginFlow = fakeSessionAuthenticator{identity: SessionIdentity{UserSpaceNamespace: testOwnerNS}, token: testToken}
	csrf, _ := server.csrfStore.Ensure("session-1")
	server.clientFactory = fakeAPIClientFactory{api: fakeAPI{workspace: &dwpkv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: testWorkspaceName, Namespace: testOwnerNS},
		Status:     dwpkv1alpha1.WorkspaceStatus{State: dwpkv1alpha1.WorkspaceStateRunning},
	}}}

	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, authedRequest("/w/dev", csrf))
	body := recorder.Body.String()

	const prompt = `<span class="confirm-prompt">Remove persistent volume immediately, ` +
		`write <strong>dev</strong> to confirm</span>`
	if !strings.Contains(body, prompt) {
		t.Errorf("the confirmation prompt is not a single line: %s", body)
	}
	if !strings.Contains(body, `class="checkbox"`) {
		t.Error("the volume checkbox is not styled like the rest of the form")
	}
}
