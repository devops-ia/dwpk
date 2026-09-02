package ui

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// profilePage is the only fragment target on the site so far.
const profilePage = "/profile"

// A link like /profile#keys promises to open a particular tab. Two things have
// to hold for it to keep that promise, and neither did: the script has to read
// the fragment, and the fragment has to name a tab that exists.

func TestTheTabScriptReadsTheFragment(t *testing.T) {
	t.Parallel()
	script, err := Assets.ReadFile("assets/app.js")
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}

	for _, needed := range []string{"location.hash", "hashchange"} {
		if !strings.Contains(string(script), needed) {
			t.Errorf("app.js never mentions %s, so a #fragment selects no tab", needed)
		}
	}
}

func TestEveryFragmentLinkNamesATabThatExists(t *testing.T) {
	t.Parallel()
	// The profile page is the only fragment target so far, and it needs a
	// UserSpace to render at all, so every page here is served by the fixture
	// that has one.
	server, csrf := newProfileServer(t, nil)

	render := func(path string) string {
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, authedRequest(http.MethodGet, path, csrf))
		if recorder.Code != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200", path, recorder.Code)
		}
		return recorder.Body.String()
	}

	tabsOn := func(path string) map[string]bool {
		names := map[string]bool{}
		for _, match := range regexp.MustCompile(`data-tab-button="([^"]+)"`).FindAllStringSubmatch(render(path), -1) {
			names[match[1]] = true
		}
		return names
	}

	links := regexp.MustCompile(`href="(/[^"#]*)#([^"]+)"`)
	for _, page := range []string{"/", onboardingPath, profilePage} {
		for _, link := range links.FindAllStringSubmatch(render(page), -1) {
			target, fragment := link[1], link[2]
			if !tabsOn(target)[fragment] {
				t.Errorf("%s links to %s#%s, but %s has no such tab", page, target, fragment, target)
			}
		}
	}
}

// Generating a key pair is the one step of the wizard that happens on the
// person's own machine, where the platform can only point. Both places that ask
// for a key link the same two guides, and both open in a new tab: leaving the
// form mid-paste loses what was typed.
func TestBothPlacesAskingForAKeyLinkTheGuides(t *testing.T) {
	t.Parallel()
	server, csrf := newProfileServer(t, nil)

	for _, path := range []string{profilePage, onboardingPath} {
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, authedRequest(http.MethodGet, path, csrf))
		body := recorder.Body.String()

		for _, guide := range []string{
			"https://docs.github.com/en/authentication/connecting-to-github-with-ssh",
			"https://docs.github.com/en/authentication/troubleshooting-ssh",
		} {
			if !strings.Contains(body, guide) {
				t.Errorf("%s does not link %s", path, guide)
			}
		}
		if strings.Contains(body, "docs.github.com") && !strings.Contains(body, `rel="noopener"`) {
			t.Errorf("%s opens an external guide without rel=noopener", path)
		}
	}
}
