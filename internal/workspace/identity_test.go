package workspace

import (
	"testing"

	dwpkv1alpha1 "github.com/devops-ia/dwpk/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// The whole point of the second ServiceAccount is that a browser session and a
// workspace container never share an identity. If a pod is ever built naming
// the session account, an administrator's session rights become reachable from
// inside their own container.
func TestWorkspacePodNeverRunsAsTheSessionAccount(t *testing.T) {
	t.Parallel()

	if ServiceAccountName == SessionServiceAccountName {
		t.Fatal("the pod and session ServiceAccounts must be different names")
	}

	img := &dwpkv1alpha1.WorkspaceImage{
		ObjectMeta: metav1.ObjectMeta{Name: "python"},
		Spec: dwpkv1alpha1.WorkspaceImageSpec{
			Image:     "example.com/python:3.12",
			HomePath:  "/home/dev",
			RunAsUser: 1000,
		},
	}

	spec := podSpec(testWorkspace(), img, nil)
	if spec.ServiceAccountName != ServiceAccountName {
		t.Fatalf("pod ServiceAccountName = %q, want %q", spec.ServiceAccountName, ServiceAccountName)
	}
	if spec.ServiceAccountName == SessionServiceAccountName {
		t.Fatal("workspace pod runs as the session ServiceAccount")
	}
}
