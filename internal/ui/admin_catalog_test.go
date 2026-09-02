package ui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	dwpkv1alpha1 "github.com/devops-ia/dwpk/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// The object name is derived so nobody has to invent one, and it has to be a
// DNS label whatever they typed.
func TestSlugify(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"Python 3.12":            "python-3-12",
		"  Node   22  ":          "node-22",
		"Go":                     "go",
		"C++ / build tools":      "c-build-tools",
		"Ünïcödé":                "n-c-d",
		"---":                    "",
		"":                       "",
		strings.Repeat("a", 200): strings.Repeat("a", 60),
	}
	for input, want := range tests {
		if got := slugify(input); got != want {
			t.Errorf("slugify(%q) = %q, want %q", input, got, want)
		}
	}
}

// 60 characters, not 63: a collision appends "-10", and a slug that used the
// whole budget would overflow the label limit on the retry rather than on the
// first attempt, which is the worst possible time to find out.
func TestSlugLeavesRoomForACollisionSuffix(t *testing.T) {
	t.Parallel()
	slug := slugify(strings.Repeat("x", 200))
	if len(slug)+len("-10") > 63 {
		t.Errorf("slug is %d characters; a collision suffix would overflow the DNS label limit", len(slug))
	}
}

func TestAdminCatalogEditsAnEntry(t *testing.T) {
	t.Parallel()
	var edit WorkspaceImageEdit
	server, csrf := newAdminScreenServer(t, fakeAPI{editedImage: &edit})

	form := "displayName=Python+3.13&description=Updated&tags=backend,+python" +
		"&deprecated=true&deprecateAt=2026-12-01"
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, authedFormRequest("/admin/catalog/python/update", csrf, form))

	if recorder.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302, body = %s", recorder.Code, recorder.Body.String())
	}
	if edit.Name != testImageName {
		t.Fatalf("edited %q, want python", edit.Name)
	}
	if edit.DisplayName != "Python 3.13" || !edit.Deprecated {
		t.Fatalf("edit = %+v", edit)
	}
	if strings.Join(edit.Tags, "|") != "backend|python" {
		t.Fatalf("tags = %v, want [backend python]", edit.Tags)
	}
	if edit.DeprecateAt == nil || edit.DeprecateAt.Format("2006-01-02") != "2026-12-01" {
		t.Fatalf("deprecateAt = %v, want 2026-12-01", edit.DeprecateAt)
	}
}

// A blank date clears a scheduled deprecation rather than leaving it. A form
// submits nothing for an emptied field, so the absence is the answer.
func TestBlankDateClearsAScheduledDeprecation(t *testing.T) {
	t.Parallel()
	var edit WorkspaceImageEdit
	server, csrf := newAdminScreenServer(t, fakeAPI{editedImage: &edit})

	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder,
		authedFormRequest("/admin/catalog/python/update", csrf, "displayName=Python"))

	if edit.DeprecateAt != nil {
		t.Fatalf("deprecateAt = %v, want nil", edit.DeprecateAt)
	}
}

// A bad date must come back on the same screen. Losing the page over a typo is
// the difference between a correction and a re-entry.
func TestAdminCatalogReportsABadDateOnTheSamePage(t *testing.T) {
	t.Parallel()
	server, csrf := newAdminScreenServer(t, fakeAPI{
		images: []dwpkv1alpha1.WorkspaceImage{{ObjectMeta: metav1.ObjectMeta{Name: testImageName}}},
	})

	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder,
		authedFormRequest("/admin/catalog/python/update", csrf, "deprecateAt=next+tuesday"))

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "not a date") {
		t.Fatalf("no error message on the page: %s", recorder.Body.String())
	}
}

// The catalog admin screen filters by which registry synced an entry -
// separate from the free-text search, and combinable with it.
func TestAdminCatalogFiltersByRegistry(t *testing.T) {
	t.Parallel()
	images := []dwpkv1alpha1.WorkspaceImage{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "team-ecr-python",
				Labels: map[string]string{dwpkv1alpha1.ImageRegistryLabel: "team-ecr"},
			},
			Spec: dwpkv1alpha1.WorkspaceImageSpec{DisplayName: "Python", Image: "example.com/python:latest"},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "hand-rolled"},
			Spec:       dwpkv1alpha1.WorkspaceImageSpec{DisplayName: "Hand rolled", Image: "example.com/other:latest"},
		},
	}
	server, csrf := newAdminScreenServer(t, fakeAPI{
		images:          images,
		imageRegistries: []dwpkv1alpha1.ImageRegistry{testRegistry()},
	})

	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, authedRequest(http.MethodGet, "/admin/catalog?registry=team-ecr", csrf))
	body := recorder.Body.String()

	if !strings.Contains(body, "team-ecr-python") {
		t.Error("the entry synced from team-ecr is missing")
	}
	if strings.Contains(body, "hand-rolled") {
		t.Error("an entry from a different registry leaked through the filter")
	}
	if !strings.Contains(body, `<option value="team-ecr" selected`) {
		t.Error("the registry select does not keep the selected filter")
	}
}

// The registry <select> offers every configured registry as an option, even
// with no filter applied - confirming registries fetched purely to fill it.
func TestAdminCatalogOffersEveryRegistryAsAFilterOption(t *testing.T) {
	t.Parallel()
	body := screenBody(t, "/admin/catalog", fakeAPI{imageRegistries: []dwpkv1alpha1.ImageRegistry{testRegistry()}})

	if !strings.Contains(body, `<option value="team-ecr"`) {
		t.Error("the registry filter is missing the configured registry")
	}
}

// Registry management moved to its own page; the catalog screen no longer
// carries the add/edit/delete registry forms.
func TestAdminCatalogNoLongerManagesRegistries(t *testing.T) {
	t.Parallel()
	body := screenBody(t, "/admin/catalog", fakeAPI{imageRegistries: []dwpkv1alpha1.ImageRegistry{testRegistry()}})

	if strings.Contains(body, "add-registry-dialog") {
		t.Error("the catalog screen still carries the add-registry dialog")
	}
}

// A catalog entry with an icon renders it through the same-origin icon proxy,
// not the raw external URL - the CSP's img-src is 'self'.
func TestAdminCatalogRendersEntryIcon(t *testing.T) {
	t.Parallel()
	body := screenBody(t, "/admin/catalog", fakeAPI{images: []dwpkv1alpha1.WorkspaceImage{{
		ObjectMeta: metav1.ObjectMeta{Name: "python"},
		Spec: dwpkv1alpha1.WorkspaceImageSpec{
			DisplayName: "Python",
			Image:       "example.com/python:latest",
			Icon:        "https://example.com/icon.svg",
		},
	}}})

	if !strings.Contains(body, `src="/workspace-images/python/icon"`) {
		t.Errorf("the entry's icon does not use the same-origin proxy path: %s", body)
	}
	if strings.Contains(body, `src="https://example.com/icon.svg"`) {
		t.Error("the entry's icon links the raw external URL directly, which the CSP's img-src 'self' will block")
	}
}

func TestAdminCatalogRefusesANonAdmin(t *testing.T) {
	t.Parallel()
	server, csrf := newAdminScreenServer(t, fakeAPI{allowedVerbs: map[string]bool{}})

	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, authedRequest(http.MethodGet, "/admin/catalog", csrf))

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", recorder.Code)
	}
}

// The icon URL is written by whoever edits a catalog entry and is fetched from
// inside the UI pod. Without a scheme guard that reaches places a browser
// never could.
func TestIconURLGuardRefusesNonHTTPSTargets(t *testing.T) {
	t.Parallel()
	refused := []string{
		"file:///etc/passwd",
		"http://169.254.169.254/latest/meta-data/",
		"gopher://internal:70/",
		"https://",
		"::not a url",
	}
	for _, raw := range refused {
		if err := checkIconURL(raw); err == nil {
			t.Fatalf("checkIconURL(%q) error = nil, want a refusal", raw)
		}
	}
	if err := checkIconURL("https://example.com/icon.svg"); err != nil {
		t.Fatalf("checkIconURL() refused an ordinary https URL: %v", err)
	}
}
