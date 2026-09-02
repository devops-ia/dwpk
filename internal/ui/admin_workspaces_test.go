package ui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	dwpkv1alpha1 "github.com/devops-ia/dwpk/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func workspaceIn(namespace, name string) dwpkv1alpha1.Workspace {
	return dwpkv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
	}
}

func memberOf(name, namespace string) dwpkv1alpha1.UserSpace {
	return dwpkv1alpha1.UserSpace{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       dwpkv1alpha1.UserSpaceSpec{Owner: name + "@example.com"},
		Status:     dwpkv1alpha1.UserSpaceStatus{Namespace: namespace},
	}
}

// An administrator lists the cluster; everybody else lists their own namespace
// and nothing else. There is no third case any more - the manager role was the
// third case, and it went with Projects.
func TestWorkspaceVisibilityFollowsRole(t *testing.T) {
	t.Parallel()

	var listed []string
	newAPI := func() fakeAPI {
		listed = nil
		return fakeAPI{
			listedNamespaces: &listed,
			userSpaces: []dwpkv1alpha1.UserSpace{
				memberOf("alice", "dwpk-alice"),
				memberOf("bob", "dwpk-bob"),
			},
			workspaces: []dwpkv1alpha1.Workspace{
				workspaceIn("dwpk-alice", "one"),
				workspaceIn("dwpk-bob", "two"),
			},
		}
	}

	t.Run("an administrator lists the cluster", func(t *testing.T) {
		_, err := visibleWorkspaces(context.Background(), newAPI(), SessionIdentity{
			Role:               dwpkv1alpha1.UserSpaceRoleAdmin,
			UserSpaceNamespace: "dwpk-alice",
		})
		if err != nil {
			t.Fatalf("visibleWorkspaces() error = %v", err)
		}
		if !slices.Contains(listed, "") {
			t.Errorf("an admin listed %v, want one cluster-wide LIST", listed)
		}
	})

	t.Run("everybody else lists only their own", func(t *testing.T) {
		_, err := visibleWorkspaces(context.Background(), newAPI(), SessionIdentity{
			Role:               dwpkv1alpha1.UserSpaceRoleUser,
			UserSpaceNamespace: "dwpk-alice",
		})
		if err != nil {
			t.Fatalf("visibleWorkspaces() error = %v", err)
		}
		if slices.Contains(listed, "") {
			t.Fatalf("a user issued a cluster-wide LIST, which the API server refuses: %v", listed)
		}
		if len(listed) != 1 || listed[0] != "dwpk-alice" {
			t.Errorf("listed %v, want only their own namespace", listed)
		}
	})
}

// Creating in somebody else's namespace is an administrator's job. Offering the
// dropdown to anyone else is offering options whose every entry 403s.
func TestCreateOnBehalfIsOfferedOnlyToAdmins(t *testing.T) {
	t.Parallel()
	api := fakeAPI{userSpaces: []dwpkv1alpha1.UserSpace{
		memberOf("alice", "dwpk-alice"),
		memberOf("bob", "dwpk-bob"),
	}}

	targets, err := creatableNamespaces(context.Background(), api, SessionIdentity{
		Role: dwpkv1alpha1.UserSpaceRoleAdmin,
	})
	if err != nil {
		t.Fatalf("creatableNamespaces() error = %v", err)
	}
	if len(targets) != 2 {
		t.Errorf("an admin was offered %d namespaces, want both", len(targets))
	}

	targets, err = creatableNamespaces(context.Background(), api, SessionIdentity{
		Role: dwpkv1alpha1.UserSpaceRoleUser,
	})
	if err != nil {
		t.Fatalf("creatableNamespaces() error = %v", err)
	}
	if len(targets) != 0 {
		t.Errorf("a user was offered %d namespaces, want none", len(targets))
	}
}

// CPU reads from Requests and memory/GPU read from Limits - genuinely
// different sides, matching ResourceValues.Requirements: CPU never gets a
// limit (so it's never artificially throttled), memory always gets one (it
// needs a hard ceiling). An unset resource shows nothing rather than "0" -
// unset and zero are different answers.
func TestWorkspaceRowsRenderCPURequestAndOtherLimits(t *testing.T) {
	t.Parallel()

	shaped := workspaceIn("dwpk-alice", "dev")
	shaped.Spec.Resources = corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU: resource.MustParse("500m"),
		},
		Limits: corev1.ResourceList{
			corev1.ResourceMemory: resource.MustParse("1Gi"),
			"nvidia.com/gpu":      resource.MustParse("2"),
		},
	}
	bare := workspaceIn("dwpk-bob", "plain")

	rows := adminWorkspaceRows([]dwpkv1alpha1.Workspace{shaped, bare}, "nvidia.com/gpu")

	byName := map[string]AdminWorkspaceRow{}
	for _, row := range rows {
		byName[row.Name] = row
	}
	if byName["dev"].CPU != "500m" || byName["dev"].Memory != "1Gi" {
		t.Errorf("dev = %s / %s, want 500m / 1Gi", byName["dev"].CPU, byName["dev"].Memory)
	}
	if byName["dev"].GPU != "2" {
		t.Errorf("dev GPU = %q, want 2", byName["dev"].GPU)
	}
	if byName["plain"].CPU != "" || byName["plain"].GPU != "" {
		t.Errorf("an unset resource rendered as %q, want empty", byName["plain"].CPU)
	}
	// Sorted by namespace then name, so a long list stays scannable.
	if rows[0].Namespace != "dwpk-alice" {
		t.Errorf("rows are not sorted by namespace: %v", rows[0].Namespace)
	}
}

// Deleting a workspace is the one action that destroys data, so the name has to
// be typed back. The dialog disables its button until it matches, but a POST
// can be made without ever loading the page - the server is the guard, and this
// is the test of the guard.
func TestWorkspaceDeleteRequiresTheNameTyped(t *testing.T) {
	t.Parallel()

	t.Run("a mismatch deletes nothing", func(t *testing.T) {
		t.Parallel()
		var deletedWS, deletedClaim string
		server, csrf := newAdminScreenServer(t, fakeAPI{
			deletedWS: &deletedWS, deletedClaim: &deletedClaim,
		})

		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, authedFormRequest(
			"/admin/workspaces/dwpk-alice/dev/delete", csrf,
			"confirm_name=dee&delete_volume=true"))

		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", recorder.Code)
		}
		if deletedWS != "" || deletedClaim != "" {
			t.Fatalf("a mismatch still deleted %q and %q", deletedWS, deletedClaim)
		}
	})

	// An empty field is the case a scripted POST hits by accident, and it must
	// not be read as "no confirmation required".
	t.Run("an absent confirmation deletes nothing", func(t *testing.T) {
		t.Parallel()
		var deletedWS string
		server, csrf := newAdminScreenServer(t, fakeAPI{deletedWS: &deletedWS})

		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, authedFormRequest(
			"/admin/workspaces/dwpk-alice/dev/delete", csrf, "delete_volume=true"))

		if deletedWS != "" {
			t.Fatalf("a POST with no confirmation deleted %q", deletedWS)
		}
	})

	t.Run("a match deletes the workspace and its volume", func(t *testing.T) {
		t.Parallel()
		var deletedWS, deletedClaim string
		server, csrf := newAdminScreenServer(t, fakeAPI{
			deletedWS: &deletedWS, deletedClaim: &deletedClaim,
		})

		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, authedFormRequest(
			"/admin/workspaces/dwpk-alice/dev/delete", csrf,
			"confirm_name=dev&delete_volume=true"))

		if recorder.Code != http.StatusFound {
			t.Fatalf("status = %d, want 302: %s", recorder.Code, recorder.Body.String())
		}
		if deletedWS != "dwpk-alice/dev" {
			t.Errorf("deleted workspace = %q", deletedWS)
		}
		// The claim name is derived, and getting it wrong deletes somebody
		// else's volume or none at all.
		if deletedClaim != "dwpk-alice/home-dev-0" {
			t.Errorf("deleted claim = %q, want dwpk-alice/home-dev-0", deletedClaim)
		}
	})

	// Unticking is how you keep the data, and it has to actually keep it.
	t.Run("unticking the box keeps the volume", func(t *testing.T) {
		t.Parallel()
		var deletedWS, deletedClaim string
		server, csrf := newAdminScreenServer(t, fakeAPI{
			deletedWS: &deletedWS, deletedClaim: &deletedClaim,
		})

		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, authedFormRequest(
			"/admin/workspaces/dwpk-alice/dev/delete", csrf, "confirm_name=dev"))

		if deletedWS != "dwpk-alice/dev" {
			t.Errorf("the workspace was not deleted: %q", deletedWS)
		}
		if deletedClaim != "" {
			t.Errorf("the volume was deleted anyway: %q", deletedClaim)
		}
	})
}
