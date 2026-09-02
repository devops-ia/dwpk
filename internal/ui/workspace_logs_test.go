package ui

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	dwpkv1alpha1 "github.com/devops-ia/dwpk/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func logServer(t *testing.T, api fakeAPI) (*Server, string) {
	t.Helper()
	server := newTestServer(t)
	server.loginFlow = fakeSessionAuthenticator{
		identity: SessionIdentity{UserSpaceNamespace: testOwnerNS},
		token:    testToken,
	}
	csrf, _ := server.csrfStore.Ensure("session-1")
	server.clientFactory = fakeAPIClientFactory{api: api}
	return server, csrf
}

func runningWorkspace() *dwpkv1alpha1.Workspace {
	return &dwpkv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: testWorkspaceName, Namespace: testOwnerNS},
		Status: dwpkv1alpha1.WorkspaceStatus{
			State:   dwpkv1alpha1.WorkspaceStateRunning,
			PodName: "dev-0",
		},
	}
}

// Every log outcome has to say which one it is. A blank pane leaves a reader
// unable to tell a quiet container from a broken one from a refused request.
func TestWorkspaceLogsNameEveryOutcome(t *testing.T) {
	t.Parallel()

	suspended := runningWorkspace()
	suspended.Status.PodName = ""
	suspended.Status.State = dwpkv1alpha1.WorkspaceStateSuspended

	tests := []struct {
		name string
		api  fakeAPI
		want string
	}{
		{
			name: "output is shown as written",
			api:  fakeAPI{workspace: runningWorkspace(), logs: testLogLine},
			want: testLogLine,
		},
		{
			name: "no pod says so rather than showing nothing",
			api:  fakeAPI{workspace: suspended},
			want: "No pod is running",
		},
		{
			name: "an empty tail is distinguished from a failure",
			api:  fakeAPI{workspace: runningWorkspace(), logs: ""},
			want: "No output yet",
		},
		{
			name: "a refusal carries the API server's own message",
			api: fakeAPI{
				workspace: runningWorkspace(),
				logsErr:   errors.New("pods dev-0 is forbidden: cannot get resource pods/log"),
			},
			want: "is forbidden: cannot get resource pods/log",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server, csrf := logServer(t, test.api)
			recorder := httptest.NewRecorder()
			server.Handler().ServeHTTP(recorder, authedRequest(http.MethodGet, "/w/dev/logs", csrf))

			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", recorder.Code)
			}
			if !strings.Contains(recorder.Body.String(), test.want) {
				t.Fatalf("body missing %q: %s", test.want, recorder.Body.String())
			}
		})
	}
}

// The status card polls itself every three seconds. Anything on it is
// destroyed and rebuilt each tick, so the terminal must not be on it - that
// was the bug: the socket was orphaned and the output wiped mid-session.
func TestPolledFragmentExcludesTheTerminal(t *testing.T) {
	t.Parallel()
	server, csrf := logServer(t, fakeAPI{workspace: runningWorkspace()})

	fragment := httptest.NewRecorder()
	server.Handler().ServeHTTP(fragment, authedRequest(http.MethodGet, "/w/dev/status", csrf))
	body := fragment.Body.String()

	if strings.Contains(body, "data-terminal ") {
		t.Fatalf("the polled fragment still carries the terminal: %s", body)
	}
	if strings.Contains(body, "data-tab-button") {
		t.Fatalf("the polled fragment still carries the tab bar: %s", body)
	}
	if !strings.Contains(body, `id="workspace-status"`) {
		t.Fatalf("the fragment lost the id it replaces itself by: %s", body)
	}

	// The full page must still contain both, or the split moved them nowhere.
	page := httptest.NewRecorder()
	server.Handler().ServeHTTP(page, authedRequest(http.MethodGet, "/w/dev", csrf))
	for _, want := range []string{"data-terminal", "data-tab-button", `id="workspace-status"`} {
		if !strings.Contains(page.Body.String(), want) {
			t.Fatalf("page missing %q", want)
		}
	}
}

// A Failed workspace has to say why on the screen, not only in kubectl.
func TestFailedWorkspaceShowsTheControllersReason(t *testing.T) {
	t.Parallel()
	failed := runningWorkspace()
	failed.Status.State = dwpkv1alpha1.WorkspaceStateFailed
	failed.Status.Conditions = []metav1.Condition{{
		Type:    "Ready",
		Status:  metav1.ConditionFalse,
		Reason:  "ImagePullBackOff",
		Message: "back-off pulling image ghcr.io/example/python:3.13",
	}}

	server, csrf := logServer(t, fakeAPI{workspace: failed})
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, authedRequest(http.MethodGet, "/w/dev/status", csrf))

	body := recorder.Body.String()
	if !strings.Contains(body, "ImagePullBackOff: back-off pulling image") {
		t.Fatalf("no failure reason on the card: %s", body)
	}
	// Recovery is the same verb as starting, relabelled so it reads as a retry.
	if !strings.Contains(body, "Retry") {
		t.Fatalf("no retry action offered: %s", body)
	}
}

// A plain form POST - the dashboard cards - must not be handed a fragment.
// The browser navigates to it, so a fragment means a page with no stylesheet,
// no script and no way back: "Stop loses the styles".
func TestPlainFormPostRedirectsInsteadOfReturningAFragment(t *testing.T) {
	t.Parallel()
	server, csrf := logServer(t, fakeAPI{workspace: runningWorkspace()})

	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, authedFormRequest("/w/dev/stop", csrf, ""))

	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303: %s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "status-card") {
		t.Fatal("a plain POST was answered with the fragment")
	}
}

// The same handler must still answer htmx with the fragment, or the workspace
// page starts doing full-page navigations on every Start.
func TestHtmxPostStillGetsTheFragment(t *testing.T) {
	t.Parallel()
	server, csrf := logServer(t, fakeAPI{workspace: runningWorkspace()})

	request := authedFormRequest("/w/dev/stop", csrf, "")
	request.Header.Set("HX-Request", boolTrue)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), `id="workspace-status"`) {
		t.Fatalf("htmx did not get the fragment: %s", recorder.Body.String())
	}
}

// The status card replaces itself with outerHTML, and htmx fires load triggers
// on swapped-in content. A "load" trigger there means every response arms the
// next request with no delay: a hot loop against the API server that reads on
// screen as a page that will not stop reloading.
func TestPolledFragmentNeverTriggersOnLoad(t *testing.T) {
	t.Parallel()

	for _, settled := range []bool{true, false} {
		if trigger := workspacePollTrigger(settled); strings.Contains(trigger, "load") {
			t.Fatalf("settled=%v gives trigger %q, which re-arms on every swap", settled, trigger)
		}
	}

	// A settled workspace polls not at all; an unsettled one polls on a timer.
	// "none" rather than "" - see TestSettledStatusCardStatesItsTriggerExplicitly.
	if got := workspacePollTrigger(true); got != "none" {
		t.Fatalf("a settled workspace polls %q; it should state an explicit none", got)
	}
	if got := workspacePollTrigger(false); !strings.Contains(got, "every") {
		t.Fatalf("an unsettled workspace does not poll: %q", got)
	}
}

// A settled card must say "do not fire" rather than say nothing.
//
// Omitting hx-trigger does not mean "no trigger" to htmx. With no hx-trigger,
// htmx picks a default from the element: submit for a form, change for an
// input, and click for everything else - so a bare <div> silently becomes
// click-to-fire. This card is a <div> that swaps itself with outerHTML and
// contains the Delete button and its dialog, so a click on Delete opened the
// dialog and simultaneously fetched a replacement card, destroying the dialog
// in the same frame. The button looked dead.
//
// The absence of an attribute is not testable by reading the element, which is
// how the old assertion here (that hx-trigger must NOT appear) passed while the
// page was broken. Pin the presence instead.
func TestSettledStatusCardStatesItsTriggerExplicitly(t *testing.T) {
	t.Parallel()
	server, csrf := logServer(t, fakeAPI{workspace: runningWorkspace()})

	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, authedRequest(http.MethodGet, "/w/dev/status", csrf))

	if !strings.Contains(recorder.Body.String(), `hx-trigger="none"`) {
		t.Fatalf("a settled card leaves htmx to guess a trigger, and it guesses click: %s", recorder.Body.String())
	}
}

// A session is a real shell in somebody's pod, so the tab must not open one
// just because the page loaded. The standalone window is the opposite case: it
// IS the terminal, so it connects on load. The attribute is what tells them
// apart, and getting it backwards would put the old behaviour back silently.
func TestOnlyTheStandaloneTerminalAutostarts(t *testing.T) {
	t.Parallel()
	server, csrf := logServer(t, fakeAPI{workspace: runningWorkspace()})

	tab := httptest.NewRecorder()
	server.Handler().ServeHTTP(tab, authedRequest(http.MethodGet, "/w/dev", csrf))
	if strings.Contains(tab.Body.String(), "data-terminal-autostart") {
		t.Error("the workspace page starts a shell on load; it must wait to be opened")
	}
	// And it offers the way in, which is not called "Reconnect" before there has
	// been a connection.
	if !strings.Contains(tab.Body.String(), ">Connect</button>") {
		t.Error("the tab offers no way to start the terminal")
	}
	// Popping out hands the session over rather than running a second shell.
	if !strings.Contains(tab.Body.String(), "data-terminal-popout") {
		t.Error("the pop-out link does not close the tab's session")
	}

	window := httptest.NewRecorder()
	server.Handler().ServeHTTP(window, authedRequest(http.MethodGet, "/w/dev/terminal", csrf))
	if !strings.Contains(window.Body.String(), "data-terminal-autostart") {
		t.Error("the standalone terminal window does not connect on load")
	}
}
