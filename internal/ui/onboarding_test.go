package ui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	dwpkv1alpha1 "github.com/devops-ia/dwpk/api/v1alpha1"
)

func newOnboardingServer(t *testing.T, onboarded *string) (*Server, string) {
	t.Helper()
	server := newTestServer(t)
	server.loginFlow = fakeSessionAuthenticator{
		identity: SessionIdentity{
			Email:              testOwner,
			UserSpaceName:      testUsername,
			UserSpaceNamespace: testOwnerNS,
			Role:               dwpkv1alpha1.UserSpaceRoleUser,
		},
		token:            testToken,
		markedOnboarding: onboarded,
	}
	server.clientFactory = fakeAPIClientFactory{api: fakeAPI{
		patchedOnboarded: onboarded,
	}}
	csrf, err := server.csrfStore.Ensure(testSession)
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	return server, csrf
}

func TestOnboardingPageRendersAllFourSteps(t *testing.T) {
	t.Parallel()
	server, csrf := newOnboardingServer(t, nil)

	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, authedRequest(http.MethodGet, onboardingPath, csrf))
	body := recorder.Body.String()

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, body)
	}
	for _, want := range []string{
		"Push your SSH key",
		"Browse the catalog",
		"Start your first workspace",
		"You're set",
		`action="/onboarding/complete"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("onboarding page missing %q: %s", want, body)
		}
	}
}

func TestOnboardingCompleteStampsUserSpaceAndRefreshesSessionThenRedirects(t *testing.T) {
	t.Parallel()
	var patchedName, markedSession string
	server, csrf := newOnboardingServer(t, &patchedName)
	server.loginFlow = fakeSessionAuthenticator{
		identity: SessionIdentity{
			Email:              testOwner,
			UserSpaceName:      testUsername,
			UserSpaceNamespace: testOwnerNS,
		},
		token:            testToken,
		markedOnboarding: &markedSession,
	}

	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, authedFormRequest(onboardingCompletePath, csrf, ""))

	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Location"); got != "/" {
		t.Fatalf("Location = %q", got)
	}
	if patchedName != testUsername {
		t.Fatalf("PatchUserSpaceOnboardingCompleted called with %q, want %q", patchedName, testUsername)
	}
	if markedSession != testSession {
		t.Fatalf("MarkOnboardingCompleted called with %q, want %q", markedSession, testSession)
	}
}

// A first sign-in lands on the wizard, because someone who has never seen the
// product needs telling where things are. It is the landing page and nothing
// more: every link on it works, and the next request goes wherever it asks.
func TestFirstSignInLandsOnTheWizardAndLaterOnesDoNot(t *testing.T) {
	t.Parallel()
	pending := true
	server := newTestServer(t)
	server.loginFlow = fakeSessionAuthenticator{localAuth: true, onboardingPending: &pending}

	landing := func() string {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/login/local",
			strings.NewReader("username=someone&password=hunter2"))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		server.Handler().ServeHTTP(recorder, request)
		if recorder.Code != http.StatusFound {
			t.Fatalf("login status = %d, body = %s", recorder.Code, recorder.Body.String())
		}
		return recorder.Header().Get("Location")
	}

	if got := landing(); got != onboardingPath {
		t.Fatalf("a new user landed on %q, want the wizard", got)
	}

	pending = false
	if got := landing(); got != "/" {
		t.Fatalf("a returning user landed on %q, want Overview", got)
	}
}

// Somewhere specific always beats the wizard: a person following a link to a
// workspace wants the workspace, whether or not they have been introduced.
func TestAnExplicitDestinationBeatsTheWizard(t *testing.T) {
	t.Parallel()
	pending := true
	server := newTestServer(t)
	server.loginFlow = fakeSessionAuthenticator{localAuth: true, onboardingPending: &pending}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/login/local",
		strings.NewReader("username=someone&password=hunter2&next=%2Fcatalog"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	server.Handler().ServeHTTP(recorder, request)

	if got := recorder.Header().Get("Location"); got != catalogPath {
		t.Fatalf("landed on %q, want the page that was asked for", got)
	}
}

// The wizard walks a brand-new user through the product, so a link that lands
// on an error page is the worst possible first impression. Step 3 pointed at
// the create form with no image named, which cannot render: the form reads its
// sizes and resources from an image. The catalog is the way in.
func TestOnboardingNeverLinksToTheCreateFormWithoutAnImage(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)

	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, authedRequest(http.MethodGet, "/onboarding", ""))

	parts := strings.Split(recorder.Body.String(), `class="button-link" href="`)[1:]
	links := make([]string, 0, len(parts))
	for _, part := range parts {
		link, _, _ := strings.Cut(part, `"`)
		links = append(links, link)
	}
	if len(links) == 0 {
		t.Fatal("wizard offers no links at all")
	}
	for _, link := range links {
		if link == "/new" {
			t.Error(`wizard links to "/new" with no image, which cannot render a form`)
		}
	}
}

// With the wizard optional, the sidebar is the only thing telling someone it
// exists. It has to be there for as long as they have not finished it, and gone
// the moment they have — an invitation that outlives its usefulness is clutter.
func TestTheSidebarOffersTheWizardOnlyWhileItIsUnfinished(t *testing.T) {
	t.Parallel()
	pending := true
	server := newTestServer(t)
	server.loginFlow = fakeSessionAuthenticator{onboardingPending: &pending}

	overview := func() string {
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, authedRequest(http.MethodGet, "/", ""))
		return recorder.Body.String()
	}

	body := overview()
	if !strings.Contains(body, `href="/onboarding"`) {
		t.Error("the sidebar offers no way into the wizard")
	}
	if !strings.Contains(body, "nav-link-callout") {
		t.Error("the wizard entry is not marked out from the ordinary links")
	}

	pending = false
	if strings.Contains(overview(), `href="/onboarding"`) {
		t.Error("the sidebar still offers a wizard that has been finished")
	}
}
