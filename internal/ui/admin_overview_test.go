package ui

import (
	"testing"

	dwpkv1alpha1 "github.com/devops-ia/dwpk/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func userSpaceIn(name, namespace string) dwpkv1alpha1.UserSpace {
	return dwpkv1alpha1.UserSpace{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: dwpkv1alpha1.UserSpaceSpec{
			Owner: name + "@example.com",
			Role:  dwpkv1alpha1.UserSpaceRoleUser,
		},
		Status: dwpkv1alpha1.UserSpaceStatus{Namespace: namespace},
	}
}

// The overview joins people to their workspace counts. It used to group by
// project; with projects gone it is one list, and the join is the only part
// that was ever worth testing.
func TestOverviewJoinsWorkspaceCountsToPeople(t *testing.T) {
	t.Parallel()

	data := overviewData(
		[]dwpkv1alpha1.UserSpace{
			userSpaceIn("alice", "dwpk-alice"),
			userSpaceIn("bob", "dwpk-bob"),
		},
		[]dwpkv1alpha1.Workspace{
			workspaceIn("dwpk-alice", "one"),
			workspaceIn("dwpk-alice", "two"),
		},
	)

	if data.Users != 2 || data.Workspaces != 2 {
		t.Fatalf("totals = %d users, %d workspaces", data.Users, data.Workspaces)
	}
	byName := map[string]UserSummary{}
	for _, user := range data.UserList {
		byName[user.Name] = user
	}
	if byName["alice"].Workspaces != 2 {
		t.Errorf("alice has %d workspaces, want 2", byName["alice"].Workspaces)
	}
	// Somebody with none still appears. A screen that lists only people with
	// workspaces cannot answer "who has an account and is not using it".
	if _, ok := byName["bob"]; !ok {
		t.Error("a person with no workspaces was dropped from the overview")
	}
	if byName["bob"].Workspaces != 0 {
		t.Errorf("bob has %d workspaces, want 0", byName["bob"].Workspaces)
	}
}

// Only storage counts a stopped workspace. Everything else - CPU, memory, GPU
// and the count itself - is about what is running, because a stopped workspace
// has no pod. Reporting storage as running-only would show headroom the
// ResourceQuota does not agree exists.
func TestQuotaCountsRunningForEverythingExceptStorage(t *testing.T) {
	t.Parallel()

	shaped := func(name string, running bool) dwpkv1alpha1.Workspace {
		storage := resource.MustParse("10Gi")
		return dwpkv1alpha1.Workspace{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testOwnerNS},
			Spec: dwpkv1alpha1.WorkspaceSpec{
				Running: running,
				Storage: &storage,
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("2Gi"),
					},
					Limits: corev1.ResourceList{"nvidia.com/gpu": resource.MustParse("1")},
				},
			},
		}
	}

	rows := quotaRows(
		[]dwpkv1alpha1.UserSpace{{
			ObjectMeta: metav1.ObjectMeta{Name: testUsername},
			Status:     dwpkv1alpha1.UserSpaceStatus{Namespace: testOwnerNS},
		}},
		[]dwpkv1alpha1.Workspace{shaped("up", true), shaped("down", false)},
		"nvidia.com/gpu",
	)

	got := rows[0]
	if got.CPUUsed != "1" {
		t.Errorf("CPU = %s, want 1 - the stopped workspace has no pod", got.CPUUsed)
	}
	if got.MemoryUsed != "2Gi" {
		t.Errorf("memory = %s, want 2Gi", got.MemoryUsed)
	}
	if got.GPUUsed != "1" {
		t.Errorf("GPU = %s, want 1 - a stopped workspace holds no GPU", got.GPUUsed)
	}
	if got.StorageUsed != "20Gi" {
		t.Errorf("storage = %s, want 20Gi - a stopped workspace keeps its volume", got.StorageUsed)
	}
	if got.WorkspaceCount != 1 {
		t.Errorf("count = %d, want 1 - the limit is on running workspaces", got.WorkspaceCount)
	}
}
