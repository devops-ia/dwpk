package ui

import (
	"testing"
	"time"

	dwpkv1alpha1 "github.com/devops-ia/dwpk/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func testCatalogEntry() dwpkv1alpha1.WorkspaceImage {
	return dwpkv1alpha1.WorkspaceImage{
		ObjectMeta: metav1.ObjectMeta{Name: "python-3-12"},
		Spec: dwpkv1alpha1.WorkspaceImageSpec{
			DisplayName: "Python 3.12",
			Description: "Python with poetry and ruff",
			Image:       "example.com/python:3.12",
			Tags:        []string{"backend", "python"},
		},
	}
}

func TestWorkspaceVSCodeLinkOpensAtHomePath(t *testing.T) {
	t.Parallel()
	ws := &dwpkv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: "dev"},
		Status:     dwpkv1alpha1.WorkspaceStatus{Endpoint: "dev@dwpk.example.com"},
	}
	if got := workspaceVSCodeLink(ws, "dwpk.example.com", "/home/dev"); got != "vscode://vscode-remote/ssh-remote+dev@dwpk.example.com/home/dev" {
		t.Errorf("workspaceVSCodeLink() = %q, want a link opened at /home/dev", got)
	}
}

// A blank homePath - the catalog entry could not be read - falls back to the
// filesystem root rather than producing a broken link.
func TestWorkspaceVSCodeLinkFallsBackToRootWithNoHomePath(t *testing.T) {
	t.Parallel()
	ws := &dwpkv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: "dev"},
		Status:     dwpkv1alpha1.WorkspaceStatus{Endpoint: "dev@dwpk.example.com"},
	}
	if got := workspaceVSCodeLink(ws, "dwpk.example.com", ""); got != "vscode://vscode-remote/ssh-remote+dev@dwpk.example.com/" {
		t.Errorf("workspaceVSCodeLink() = %q, want a link opened at /", got)
	}
}

func TestWorkspaceImageVisible(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		mutate         func(*dwpkv1alpha1.WorkspaceImage)
		text, tag      string
		showDeprecated bool
		want           bool
	}{
		"no filters shows everything":     {want: true},
		"matches the display name":        {text: "python 3.12", want: true},
		"matches a tag as free text":      {text: "backend", want: true},
		"matches the object name":         {text: "python-3-12", want: true},
		"is case insensitive":             {text: "PYTHON", want: true},
		"does not match something absent": {text: "rust", want: false},
		"matches the tag filter":          {tag: "backend", want: true},
		"rejects another tag":             {tag: "frontend", want: false},
		"hides a deprecated entry": {
			mutate: func(i *dwpkv1alpha1.WorkspaceImage) { i.Spec.Deprecated = true },
			want:   false,
		},
		"shows a deprecated entry when asked": {
			mutate:         func(i *dwpkv1alpha1.WorkspaceImage) { i.Spec.Deprecated = true },
			showDeprecated: true,
			want:           true,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			image := testCatalogEntry()
			if test.mutate != nil {
				test.mutate(&image)
			}
			if got := workspaceImageVisible(image, test.text, test.tag, test.showDeprecated); got != test.want {
				t.Errorf("visible = %v, want %v", got, test.want)
			}
		})
	}
}

// The date and the flag are two ways of saying the same thing, so one method
// answers for both and no caller has to remember there are two.
func TestDeprecationDateBehavesAsTheFlag(t *testing.T) {
	t.Parallel()
	now := time.Now()

	image := testCatalogEntry()
	image.Spec.DeprecateAt = &metav1.Time{Time: now.Add(48 * time.Hour)}
	if image.IsDeprecated(now) {
		t.Error("an entry deprecating in two days is already deprecated")
	}
	notice, warn := image.DeprecationNotice(now)
	if !warn || notice != "Deprecates in 2 days" {
		t.Errorf("DeprecationNotice = %q, %v; want \"Deprecates in 2 days\", true", notice, warn)
	}

	image.Spec.DeprecateAt = &metav1.Time{Time: now.Add(-time.Hour)}
	if !image.IsDeprecated(now) {
		t.Error("an entry past its date is not deprecated")
	}
	// Past the date there is nothing to count down to: it says "deprecated"
	// with the other badge instead of counting toward a moment behind it.
	if _, warn := image.DeprecationNotice(now); warn {
		t.Error("an entry past its date still offers a countdown")
	}
}

// The regression this method exists for. Gating the badge on a day COUNT hid
// the warning for the final twenty-four hours, because integer division floors
// to zero - so the one day it mattered was the one day it said nothing.
func TestTheWarningSurvivesTheLastDay(t *testing.T) {
	t.Parallel()
	now := time.Now()

	for _, test := range []struct {
		remaining time.Duration
		want      string
	}{
		{20 * time.Hour, "Deprecates today"},
		{30 * time.Hour, "Deprecates tomorrow"},
		{12 * 24 * time.Hour, "Deprecates in 12 days"},
		{time.Minute, "Deprecates today"},
	} {
		image := testCatalogEntry()
		image.Spec.DeprecateAt = &metav1.Time{Time: now.Add(test.remaining)}

		notice, warn := image.DeprecationNotice(now)
		if !warn {
			t.Errorf("with %s left the entry warns not at all", test.remaining)
			continue
		}
		if notice != test.want {
			t.Errorf("with %s left = %q, want %q", test.remaining, notice, test.want)
		}
		// And it stays usable throughout: the date has not arrived.
		if image.IsDeprecated(now) {
			t.Errorf("with %s left the entry is already refused", test.remaining)
		}
	}
}

func TestQuotaRows(t *testing.T) {
	t.Parallel()
	userSpace := dwpkv1alpha1.UserSpace{
		ObjectMeta: metav1.ObjectMeta{Name: "alice"},
		Spec: dwpkv1alpha1.UserSpaceSpec{
			Owner: "alice@example.com",
			Quota: dwpkv1alpha1.UserSpaceQuota{
				CPU:        resource.MustParse("8"),
				Memory:     resource.MustParse("32Gi"),
				Storage:    resource.MustParse("100Gi"),
				Workspaces: 2,
			},
		},
		Status: dwpkv1alpha1.UserSpaceStatus{Namespace: "dwpk-alice"},
	}
	storage := resource.MustParse("20Gi")
	workspace := dwpkv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: "dev", Namespace: "dwpk-alice"},
		Spec: dwpkv1alpha1.WorkspaceSpec{
			ImageRef: dwpkv1alpha1.WorkspaceImageReference{Name: "python-3-12"},
			Storage:  &storage,
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("500m"),
					corev1.ResourceMemory: resource.MustParse("1Gi"),
				},
			},
			// Running, or its CPU and memory would not be counted: a stopped
			// workspace has no pod.
			Running: true,
		},
	}

	rows := quotaRows(
		[]dwpkv1alpha1.UserSpace{userSpace},
		[]dwpkv1alpha1.Workspace{workspace},
		"nvidia.com/gpu",
	)
	if len(rows) != 1 {
		t.Fatalf("rows = %d", len(rows))
	}
	if rows[0].CPUUsed != "500m" || rows[0].MemoryUsed != "1Gi" || rows[0].StorageUsed != "20Gi" {
		t.Fatalf("unexpected quota row: %#v", rows[0])
	}
}
