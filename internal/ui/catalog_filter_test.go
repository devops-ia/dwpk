package ui

import (
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	dwpkv1alpha1 "github.com/devops-ia/dwpk/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// The filter form issues its own GET and swaps the result into the card grid.
// If that URL and the route serving the catalog ever disagree, the form pulls
// down some other page and drops it inside the grid - which is what happened
// when the catalog moved off "/" and the form kept pointing there. Nothing
// caught it, because every test asked for the page rather than for the form's
// target.
func TestCatalogFilterPostsToTheRouteThatServesTheCatalog(t *testing.T) {
	t.Parallel()

	server := newTestServer(t)
	server.loginFlow = fakeSessionAuthenticator{
		identity: SessionIdentity{UserSpaceNamespace: testOwnerNS}, token: testToken,
	}
	csrf, _ := server.csrfStore.Ensure(testSession)
	server.clientFactory = fakeAPIClientFactory{api: fakeAPI{
		images: []dwpkv1alpha1.WorkspaceImage{{
			ObjectMeta: metav1.ObjectMeta{Name: testImageName},
			Spec:       dwpkv1alpha1.WorkspaceImageSpec{DisplayName: "Python"},
		}},
		allowedImages: map[string]bool{testImageName: true},
	}}

	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, authedRequest(catalogPath, csrf))
	body := recorder.Body.String()

	target := regexp.MustCompile(`hx-get="([^"]*)"`).FindStringSubmatch(body)
	if target == nil {
		t.Fatalf("the filter form has no hx-get: %s", body)
	}
	if target[1] != catalogPath {
		t.Fatalf("filter targets %q, but the catalog is served from %q", target[1], catalogPath)
	}

	// And the target must actually return the grid rather than a whole page,
	// or the swap replaces one screen with another.
	fragment := httptest.NewRecorder()
	request := authedRequest(target[1]+"?q=py", csrf)
	request.Header.Set("HX-Request", boolTrue)
	server.Handler().ServeHTTP(fragment, request)

	if strings.Contains(fragment.Body.String(), "<html") {
		t.Fatalf("the filter target returned a full page, not a fragment: %s", fragment.Body.String())
	}
	if !strings.Contains(fragment.Body.String(), `id="catalog-grid"`) {
		t.Fatalf("the fragment is not the card grid: %s", fragment.Body.String())
	}
}

// The icon route answers 404 for an image with no icon set, which is right. The
// card asked for one anyway, so every icon-less entry drew a broken image and a
// console 404 - the page deciding to fetch something it already knows is absent.
func TestCatalogAsksForNoIconWhenTheImageHasNone(t *testing.T) {
	t.Parallel()

	body := screenBody(t, "/catalog", fakeAPI{
		images: []dwpkv1alpha1.WorkspaceImage{{
			ObjectMeta: metav1.ObjectMeta{Name: "plain"},
			Spec:       dwpkv1alpha1.WorkspaceImageSpec{DisplayName: "Plain", Image: "x"},
		}, {
			ObjectMeta: metav1.ObjectMeta{Name: "pretty"},
			Spec: dwpkv1alpha1.WorkspaceImageSpec{
				DisplayName: "Pretty", Image: "x", Icon: "https://example.com/i.svg",
			},
		}},
		allowedImages: map[string]bool{"plain": true, "pretty": true},
	})

	if strings.Contains(body, "/workspace-images/plain/icon") {
		t.Error("the card requests an icon the image does not have")
	}
	if !strings.Contains(body, "/workspace-images/pretty/icon") {
		t.Error("the card dropped an icon the image does have")
	}
}
