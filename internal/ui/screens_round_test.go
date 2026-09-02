package ui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	dwpkv1alpha1 "github.com/devops-ia/dwpk/api/v1alpha1"
)

const (
	fieldCPU = "cpu"
	fieldGPU = "gpu"
)

func mustQuantity(value string) resource.Quantity {
	return resource.MustParse(value)
}

// screenBody renders one screen as an administrator and returns its HTML.
func screenBody(t *testing.T, path string, api fakeAPI) string {
	t.Helper()
	server, csrf := newAdminScreenServer(t, api)
	// The dashboard resolves the viewer's own UserSpace by name.
	server.loginFlow = fakeSessionAuthenticator{
		identity: SessionIdentity{
			UserSpaceName:      testUsername,
			UserSpaceNamespace: testOwnerNS,
			Role:               dwpkv1alpha1.UserSpaceRoleAdmin,
		},
		token: testToken,
	}
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, authedRequest(http.MethodGet, path, csrf))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET %s = %d: %s", path, recorder.Code, recorder.Body.String())
	}
	return recorder.Body.String()
}

// The Overview used to replace the whole page with the empty state, so somebody
// with nothing yet could not see what they were allowed to create. The numbers
// are exactly what they need in order to decide.
func TestOverviewShowsQuotaWithNoWorkspaces(t *testing.T) {
	t.Parallel()

	body := screenBody(t, "/", fakeAPI{
		userSpaces: []dwpkv1alpha1.UserSpace{{
			ObjectMeta: metav1.ObjectMeta{Name: testUsername},
			Spec: dwpkv1alpha1.UserSpaceSpec{
				Owner: testOwner,
				Quota: dwpkv1alpha1.UserSpaceQuota{
					CPU:        mustQuantity("4"),
					Memory:     mustQuantity("8Gi"),
					Storage:    mustQuantity("50Gi"),
					GPU:        2,
					Workspaces: 3,
				},
			},
			Status: dwpkv1alpha1.UserSpaceStatus{Namespace: testOwnerNS},
		}},
	})

	// The usage panel and its limits are present even though nothing is running.
	if !strings.Contains(body, "Usage") {
		t.Fatalf("no usage panel on an empty overview: %s", body)
	}
	for _, want := range []string{"0 / 3", "0 / 4", "0 / 8Gi", "0 / 2", "0 / 50Gi"} {
		if !strings.Contains(body, want) {
			t.Errorf("usage is missing %q", want)
		}
	}
	// And the prompt survives - it moved inside the section, it did not go.
	if !strings.Contains(body, "No workspaces yet") {
		t.Error("the empty state was lost when the quota was added back")
	}
}

// A card opens a read-only dialog. The dialog has to be INSIDE the grid: that
// element is what htmx replaces on every filter keystroke, and a dialog outside
// it is orphaned the moment somebody types.
func TestCatalogDetailDialogLivesInsideTheSwappedGrid(t *testing.T) {
	t.Parallel()

	body := screenBody(t, "/catalog", fakeAPI{
		images: []dwpkv1alpha1.WorkspaceImage{{
			ObjectMeta: metav1.ObjectMeta{Name: "python-3-12"},
			Spec: dwpkv1alpha1.WorkspaceImageSpec{
				DisplayName: "Python 3.12",
				Image:       "ghcr.io/example/python:3.12",
				Shell:       "/bin/bash",
				HomePath:    "/home/dev",
				RunAsUser:   1000,
				Maintainer:  "platform@example.com",
				Command:     []string{"sleep", "infinity"},
			},
		}},
		allowedImages: map[string]bool{"python-3-12": true},
	})

	opener := strings.Index(body, `data-dialog-open="image-python-3-12"`)
	dialog := strings.Index(body, `<dialog id="image-python-3-12"`)
	if opener < 0 || dialog < 0 {
		t.Fatalf("no detail dialog rendered: %s", body)
	}

	grid := strings.Index(body, `id="catalog-grid"`)
	gridEnd := strings.Index(body[grid:], "</section>") + grid
	if grid >= opener || opener >= dialog || dialog >= gridEnd {
		t.Fatalf("the dialog is not inside #catalog-grid (grid=%d opener=%d dialog=%d end=%d)",
			grid, opener, dialog, gridEnd)
	}

	// It shows what the card deliberately omits.
	for _, want := range []string{"/bin/bash", "/home/dev", "uid 1000", "platform@example.com", "sleep infinity"} {
		if !strings.Contains(body, want) {
			t.Errorf("the detail dialog omits %q", want)
		}
	}
}

// The warning has to reach both catalogs, and has to survive the final day -
// the case a day count floors to zero and hides.
func TestDeprecationWarningReachesBothCatalogs(t *testing.T) {
	t.Parallel()

	soon := &dwpkv1alpha1.WorkspaceImage{
		ObjectMeta: metav1.ObjectMeta{Name: "python-3-12"},
		Spec: dwpkv1alpha1.WorkspaceImageSpec{
			DisplayName: "Python 3.12",
			Image:       "ghcr.io/example/python:3.12",
			DeprecateAt: &metav1.Time{Time: time.Now().Add(20 * time.Hour)},
		},
	}
	api := fakeAPI{
		images:        []dwpkv1alpha1.WorkspaceImage{*soon},
		allowedImages: map[string]bool{"python-3-12": true},
	}

	for _, path := range []string{"/catalog", "/admin/catalog"} {
		body := screenBody(t, path, api)
		if !strings.Contains(body, "Deprecates today") {
			t.Errorf("%s shows no warning 20 hours before the date: %s", path, body)
		}
		// Still offered: the date has not arrived.
		if strings.Contains(body, ">Deprecated<") {
			t.Errorf("%s calls the entry deprecated before its date", path)
		}
	}
}

// The resources form is a table now. GPU and Storage span both columns because
// neither has a range: a PVC has one size, and Kubernetes requires an extended
// resource's request and limit to match.
func TestResourceTableSpansTheSingleValuedRows(t *testing.T) {
	t.Parallel()

	body := screenBody(t, "/new?image=python-3-12", fakeAPI{
		images: []dwpkv1alpha1.WorkspaceImage{{
			ObjectMeta: metav1.ObjectMeta{Name: "python-3-12"},
			Spec:       dwpkv1alpha1.WorkspaceImageSpec{DisplayName: "Python 3.12", Image: "x"},
		}},
		allowedImages: map[string]bool{"python-3-12": true},
	})

	for _, want := range []string{"<th>Resource</th>", "<th>Request</th>"} {
		if !strings.Contains(body, want) {
			t.Errorf("the resources table is missing %q", want)
		}
	}
	if strings.Contains(body, "<th>Min</th>") {
		t.Error("the resources table still has a Min column - there is no configurable floor")
	}
	// The field names are the contract with resourceValuesFrom; renaming one
	// silently drops it from the submitted workspace.
	for _, name := range []string{"cpu_request", "memory_limit", "storage", fieldGPU, "gpu_resource"} {
		if !strings.Contains(body, `name="`+name+`"`) {
			t.Errorf("the form no longer submits %q", name)
		}
	}
	if strings.Count(body, `colspan="2"`) != 0 {
		t.Errorf("want no spanning cells - there is only one data column now, got %d", strings.Count(body, `colspan="2"`))
	}
}

// One column per resource, each carrying a maximum. The floor is always
// zero and is not configurable, so there is only one bound to submit.
//
// Usage is text rather than a second input. It is measured from the live
// Workspace objects on every render and there is nothing in Kubernetes to write
// it to, so a box would take a number and discard it.
func TestQuotaCellsHoldAMaximumPerResource(t *testing.T) {
	t.Parallel()

	body := screenBody(t, "/admin/quota", fakeAPI{
		userSpaces: []dwpkv1alpha1.UserSpace{{
			ObjectMeta: metav1.ObjectMeta{Name: testUsername},
			Spec: dwpkv1alpha1.UserSpaceSpec{
				Owner: testOwner,
				Quota: dwpkv1alpha1.UserSpaceQuota{
					CPU: mustQuantity("4"), Memory: mustQuantity("8Gi"),
					Storage: mustQuantity("50Gi"), GPU: 2, Workspaces: 3,
				},
			},
			Status: dwpkv1alpha1.UserSpaceStatus{Namespace: testOwnerNS},
		}},
	})

	for _, header := range []string{"Workspaces", "CPU", "Memory", "GPU", "Storage"} {
		if !strings.Contains(body, "<th>"+header) {
			t.Errorf("the quota table is missing a %s column", header)
		}
	}

	// Five resources, one bound each.
	if got := strings.Count(body, `class="quota-bound"`); got != 5 {
		t.Errorf("bound inputs = %d, want 5 (a maximum per resource)", got)
	}
	for _, field := range []string{"workspaces", fieldCPU, "memory", fieldGPU, "storage"} {
		if strings.Contains(body, `name="min-`+field+`"`) {
			t.Errorf("a minimum input is still rendered for %s", field)
		}
		if !strings.Contains(body, `name="`+field+`"`) {
			t.Errorf("no maximum input for %s", field)
		}
	}

	// Usage is a caption, not a control: nothing about it can be submitted.
	if !strings.Contains(body, `class="quota-used-note"`) {
		t.Error("measured usage is not shown")
	}
	if strings.Contains(body, `class="quota-used"`) || strings.Contains(body, "readonly") {
		t.Error("usage is still rendered as an input")
	}
}

// The Global screen must name every variable the process reads, including the
// per-provider ones - and must name the secret without printing it.
func TestGlobalSettingsListsTheProviderVariables(t *testing.T) {
	t.Parallel()

	server, csrf := newAdminScreenServer(t, fakeAPI{})
	server.runtime = RuntimeSettings{
		GatewayHost: "dwpk.example.com",
		Kubeconfig:  "",
		Port:        "8080",
		ProviderDetails: []ProviderSettings{{
			Name:        "entra-id",
			ClientID:    "abc123",
			IssuerURL:   "https://login.example.com",
			RedirectURL: "https://dwpk.example.com/callback/entra-id",
		}},
	}

	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, authedRequest(http.MethodGet, "/admin/settings", csrf))
	body := recorder.Body.String()

	for _, want := range []string{
		"DWPK__UI_PORT",
		"DWPK__UI_KUBECONFIG",
		"DWPK__UI_PROVIDER_ENTRA_ID_CLIENT_ID",
		"DWPK__UI_PROVIDER_ENTRA_ID_ISSUER_URL",
		"DWPK__UI_PROVIDER_ENTRA_ID_REDIRECT_URL",
		"DWPK__UI_PROVIDER_ENTRA_ID_CLIENT_SECRET",
		"abc123",
		"gpuResourceName",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the settings screen omits %q", want)
		}
	}
	// The secret is named so its absence is visibly deliberate, never shown.
	if !strings.Contains(body, "set, and never shown") {
		t.Error("the client secret row does not say why it is blank")
	}
}
