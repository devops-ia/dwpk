package ui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	dwpkv1alpha1 "github.com/devops-ia/dwpk/api/v1alpha1"
)

func testRegistry() dwpkv1alpha1.ImageRegistry {
	return dwpkv1alpha1.ImageRegistry{
		ObjectMeta: metav1.ObjectMeta{Name: "team-ecr"},
		Spec: dwpkv1alpha1.ImageRegistrySpec{
			Provider: dwpkv1alpha1.ImageRegistryProviderAWSECR,
			AWS:      &dwpkv1alpha1.AWSRegistry{Region: "eu-west-1"},
			Sync: dwpkv1alpha1.RegistrySync{
				IntervalSeconds: 900,
				Tags:            dwpkv1alpha1.TagSelector{Mode: "latest", Limit: 1},
			},
		},
		Status: dwpkv1alpha1.ImageRegistryStatus{
			Images: 3,
			Conditions: []metav1.Condition{
				{Type: "Ready", Status: metav1.ConditionTrue, Reason: "Synced", Message: "registry synced"},
			},
		},
	}
}

// The registries admin screen renders the registry, its status chip and its
// sync count - confirming imageRegistryRows actually feeds the template.
func TestAdminRegistriesRendersRegistries(t *testing.T) {
	t.Parallel()
	body := screenBody(t, "/admin/registries", fakeAPI{imageRegistries: []dwpkv1alpha1.ImageRegistry{testRegistry()}})

	if !strings.Contains(body, "team-ecr") {
		t.Error("the registry name is missing from the page")
	}
	if !strings.Contains(body, "Ready") {
		t.Error("the registry status is missing from the page")
	}
	if !strings.Contains(body, "3 images") {
		t.Error("the registry's synced image count is missing from the page")
	}
}

// The sidebar carries a dedicated Registries entry, separate from Catalog.
func TestSidebarLinksToRegistries(t *testing.T) {
	t.Parallel()
	body := screenBody(t, "/admin/registries", fakeAPI{})

	if !strings.Contains(body, `href="/admin/registries"`) {
		t.Error("the sidebar does not link to /admin/registries")
	}
}

// The origin badge on a catalog entry is derived from the image reference,
// not stored - confirming registry.OriginOf actually reaches the template.
func TestAdminCatalogShowsOriginBadge(t *testing.T) {
	t.Parallel()
	body := screenBody(t, "/admin/catalog", fakeAPI{images: []dwpkv1alpha1.WorkspaceImage{{
		ObjectMeta: metav1.ObjectMeta{Name: "python"},
		Spec: dwpkv1alpha1.WorkspaceImageSpec{
			DisplayName: "Python",
			Image:       "123456789012.dkr.ecr.eu-west-1.amazonaws.com/dwpk/python:3.12",
		},
	}}})

	if !strings.Contains(body, `<span class="badge">AWS</span>`) {
		t.Error("the AWS origin badge is missing from the catalog entry")
	}
}

// A synced entry says so, and names which registry - the admin form is
// editable but the next sync overwrites it, and the screen should not let
// that be a silent surprise.
func TestAdminCatalogFlagsASyncedEntry(t *testing.T) {
	t.Parallel()
	body := screenBody(t, "/admin/catalog", fakeAPI{images: []dwpkv1alpha1.WorkspaceImage{{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "team-ecr-python-latest",
			Labels: map[string]string{dwpkv1alpha1.ImageRegistryLabel: "team-ecr"},
		},
		Spec: dwpkv1alpha1.WorkspaceImageSpec{DisplayName: "Python", Image: "example.com/python:latest"},
	}}})

	if !strings.Contains(body, "Synced from team-ecr") {
		t.Error("the synced-from badge/hint is missing for a registry-owned entry")
	}
}

func TestAdminCreateImageRegistry(t *testing.T) {
	t.Parallel()
	var created dwpkv1alpha1.ImageRegistry
	server, csrf := newAdminScreenServer(t, fakeAPI{createdRegistry: &created})

	form := "name=Team+ECR&region=eu-west-1&intervalSeconds=600&include=dwpk%2F.%2A&tagMode=latest&tagLimit=1"
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, authedFormRequest("/admin/registries", csrf, form))

	if recorder.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302, body = %s", recorder.Code, recorder.Body.String())
	}
	if created.Spec.Provider != dwpkv1alpha1.ImageRegistryProviderAWSECR {
		t.Errorf("provider = %q, want aws-ecr", created.Spec.Provider)
	}
	if created.Spec.AWS == nil || created.Spec.AWS.Region != "eu-west-1" {
		t.Fatalf("aws = %+v, want region eu-west-1", created.Spec.AWS)
	}
	if created.Spec.Sync.IntervalSeconds != 600 {
		t.Errorf("intervalSeconds = %d, want 600", created.Spec.Sync.IntervalSeconds)
	}
	if len(created.Spec.Sync.Include) != 1 || created.Spec.Sync.Include[0] != "dwpk/.*" {
		t.Errorf("include = %v, want [dwpk/.*]", created.Spec.Sync.Include)
	}
}

func TestAdminUpdateImageRegistryClearsRoleARN(t *testing.T) {
	t.Parallel()
	var edit ImageRegistryEdit
	server, csrf := newAdminScreenServer(t, fakeAPI{editedRegistry: &edit})

	form := "region=eu-west-1&intervalSeconds=900&tagMode=latest&tagLimit=1"
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, authedFormRequest("/admin/registries/team-ecr/update", csrf, form))

	if recorder.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302, body = %s", recorder.Code, recorder.Body.String())
	}
	if edit.Name != "team-ecr" {
		t.Errorf("edited %q, want team-ecr", edit.Name)
	}
	if edit.RoleARN != "" {
		t.Errorf("roleArn = %q, want empty (the form omitted it)", edit.RoleARN)
	}
	if edit.ImagePullSecretRef != nil {
		t.Errorf("ImagePullSecretRef = %v, want nil (the form left it blank)", edit.ImagePullSecretRef)
	}
}

func TestAdminForceSyncImageRegistry(t *testing.T) {
	t.Parallel()
	var synced string
	server, csrf := newAdminScreenServer(t, fakeAPI{forceSyncedName: &synced})

	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, authedFormRequest("/admin/registries/team-ecr/force-sync", csrf, ""))

	if recorder.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302, body = %s", recorder.Code, recorder.Body.String())
	}
	if synced != "team-ecr" {
		t.Errorf("force-synced %q, want team-ecr", synced)
	}
}

func TestAdminDeleteImageRegistry(t *testing.T) {
	t.Parallel()
	var deleted string
	server, csrf := newAdminScreenServer(t, fakeAPI{deletedRegistry: &deleted})

	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, authedFormRequest("/admin/registries/team-ecr/delete", csrf, ""))

	if recorder.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302, body = %s", recorder.Code, recorder.Body.String())
	}
	if deleted != "team-ecr" {
		t.Errorf("deleted %q, want team-ecr", deleted)
	}
}
