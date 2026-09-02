package ui

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	dwpkv1alpha1 "github.com/devops-ia/dwpk/api/v1alpha1"
)

// The create form is meaningless without an image: it reads the sizes, the
// resources and the home path from one. Reached without a name it rendered a
// 500 - which is exactly what the onboarding wizard's own "Start a workspace"
// link did.
func TestCreateFormWithoutAnImageSendsYouToTheCatalog(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)

	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, authedRequest("/new", ""))

	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("GET /new with no image: status %d, want %d", recorder.Code, http.StatusSeeOther)
	}
	if location := recorder.Header().Get("Location"); location != "/catalog" {
		t.Fatalf("redirected to %q, want the catalog", location)
	}
}

// Browsers compile a pattern attribute with the `v` flag, under which a literal
// hyphen inside a character class must be escaped. An invalid pattern is not an
// error the user ever sees: the browser drops the constraint and the name goes
// to the server unchecked. Go's regexp is more permissive than the browser, so
// this pins the escape rather than merely compiling the expression.
func TestWorkspaceNamePatternCompilesInABrowser(t *testing.T) {
	t.Parallel()
	body := screenBody(t, "/new?image=python-3-12", fakeAPI{
		images: []dwpkv1alpha1.WorkspaceImage{{
			ObjectMeta: metav1.ObjectMeta{Name: "python-3-12"},
			Spec:       dwpkv1alpha1.WorkspaceImageSpec{DisplayName: "Python", Image: "x"},
		}},
		allowedImages: map[string]bool{"python-3-12": true},
	})

	_, after, found := strings.Cut(body, `pattern="`)
	if !found {
		t.Fatal("the name field has no pattern at all")
	}
	pattern, _, _ := strings.Cut(after, `"`)

	if _, err := regexp.Compile(pattern); err != nil {
		t.Fatalf("pattern %q is not a regular expression: %v", pattern, err)
	}
	// A hyphen is a range operator, so it is only legal between two operands.
	// Leading or trailing it means a literal, which the v flag requires escaped.
	for _, class := range strings.Split(pattern, "[")[1:] {
		body := class[:strings.Index(class, "]")]
		if strings.HasPrefix(body, "-") || (strings.HasSuffix(body, "-") && !strings.HasSuffix(body, `\-`)) {
			t.Fatalf("literal hyphen in character class %q must be escaped for the v flag", body)
		}
	}
}

const testEnvKey = "FOO"

func formRequest(t *testing.T, values url.Values) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/new", strings.NewReader(values.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := req.ParseForm(); err != nil {
		t.Fatalf("parse form: %v", err)
	}
	return req
}

// draftFromForm reads env_key/env_value as parallel lists, the shape the
// repeatable KEY=VALUE rows in the create form submit.
func TestDraftFromFormParsesMultipleEnvVars(t *testing.T) {
	t.Parallel()
	req := formRequest(t, url.Values{
		"env_key":   {testEnvKey, "BAR"},
		"env_value": {"one", "two"},
	})

	draft, err := draftFromForm(req, "dwpk-alice")
	if err != nil {
		t.Fatalf("draftFromForm: %v", err)
	}
	if len(draft.Env) != 2 {
		t.Fatalf("Env = %+v, want 2 entries", draft.Env)
	}
	if draft.Env[0].Name != testEnvKey || draft.Env[0].Value != "one" {
		t.Errorf("Env[0] = %+v, want FOO=one", draft.Env[0])
	}
	if draft.Env[1].Name != "BAR" || draft.Env[1].Value != "two" {
		t.Errorf("Env[1] = %+v, want BAR=two", draft.Env[1])
	}
}

// A blank trailing row - what an untouched "Add variable" row looks like - is
// dropped rather than rejected.
func TestDraftFromFormDropsABlankTrailingEnvRow(t *testing.T) {
	t.Parallel()
	req := formRequest(t, url.Values{
		"env_key":   {testEnvKey, ""},
		"env_value": {"one", ""},
	})

	draft, err := draftFromForm(req, "dwpk-alice")
	if err != nil {
		t.Fatalf("draftFromForm: %v", err)
	}
	if len(draft.Env) != 1 {
		t.Fatalf("Env = %+v, want exactly 1 entry", draft.Env)
	}
}

// A key with no value at all is a mistake, not a blank row, and must be
// rejected rather than silently dropped or sent as an empty-string value.
func TestDraftFromFormRejectsAnEnvVarMissingItsName(t *testing.T) {
	t.Parallel()
	req := formRequest(t, url.Values{
		"env_key":   {""},
		"env_value": {"one"},
	})

	if _, err := draftFromForm(req, "dwpk-alice"); err == nil {
		t.Fatal("a value with no name was accepted")
	}
}

// A space or '=' in the name would corrupt the pod's actual environment, so
// it is rejected here rather than reaching the API server.
func TestDraftFromFormRejectsAMalformedEnvVarName(t *testing.T) {
	t.Parallel()
	req := formRequest(t, url.Values{
		"env_key":   {"FOO=BAR"},
		"env_value": {"one"},
	})

	if _, err := draftFromForm(req, "dwpk-alice"); err == nil {
		t.Fatal("a malformed environment variable name was accepted")
	}
}
