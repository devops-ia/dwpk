package ui

import (
	"fmt"
	"net/http"
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EventRow is one Kubernetes event as the workspace screen shows it.
type EventRow struct {
	When    string
	Type    string
	Reason  string
	Message string
}

// WorkspaceEventsData backs the Events tab.
//
// Message and Detail are the soft-failure pair the logs tab already uses: every
// way this can go wrong - no pod yet, no permission, nothing has happened -
// renders as a sentence in the panel rather than as an error page over the whole
// workspace. A tab that cannot load is not a reason to lose the terminal.
type WorkspaceEventsData struct {
	Conditions []metav1.Condition
	Events     []EventRow
	Message    string
	Detail     string
}

// handleWorkspaceEvents renders the events for a workspace's pod.
//
// Read with the caller's own token, like everything else (SPEC §8.1). The
// per-user Role already grants events, so this needs no new RBAC - but a 403 is
// still rendered as the API server's own words rather than swallowed.
func (s *Server) handleWorkspaceEvents(w http.ResponseWriter, r *http.Request) {
	session, ok := requestSessionFrom(r.Context())
	if !ok {
		s.redirectToLogin(w, r)
		return
	}
	api, err := s.clientFactory.ForToken(session.Token)
	if err != nil {
		s.writeErrorPage(w, r, err)
		return
	}

	namespace := session.Identity.UserSpaceNamespace
	ws, err := api.GetWorkspace(r.Context(), namespace, r.PathValue("name"))
	if err != nil {
		s.renderFragment(w, r, WorkspaceEvents(WorkspaceEventsData{
			Message: "This workspace could not be read.",
			Detail:  err.Error(),
		}))
		return
	}

	data := WorkspaceEventsData{Conditions: ws.Status.Conditions}
	events, err := api.ListEvents(r.Context(), namespace)
	switch {
	case err != nil:
		data.Message = "Events could not be read."
		data.Detail = err.Error()
	default:
		data.Events = eventRows(events, ws.Name, time.Now())
		if len(data.Events) == 0 {
			data.Message = "Nothing has happened to this workspace yet."
		}
	}
	s.renderFragment(w, r, WorkspaceEvents(data))
}

// eventRows keeps the events about this workspace and its pod, newest first.
//
// Filtered here rather than with a field selector because the interesting
// events name two different objects - the StatefulSet's own, and the pod's -
// and one selector cannot ask for both. The list is a namespace's worth, which
// is small.
func eventRows(events []corev1.Event, workspaceName string, now time.Time) []EventRow {
	rows := make([]EventRow, 0, len(events))
	for i := range events {
		event := &events[i]
		if !aboutWorkspace(event, workspaceName) {
			continue
		}
		rows = append(rows, EventRow{
			When:    relativeTime(eventTime(event), now),
			Type:    event.Type,
			Reason:  event.Reason,
			Message: event.Message,
		})
	}
	slices.Reverse(rows)
	return rows
}

// aboutWorkspace matches the workspace, its StatefulSet and its pods, all of
// which share the workspace's name as a prefix (`<name>`, `<name>-0`).
func aboutWorkspace(event *corev1.Event, workspaceName string) bool {
	name := event.InvolvedObject.Name
	if name == workspaceName {
		return true
	}
	return len(name) > len(workspaceName) &&
		name[:len(workspaceName)] == workspaceName &&
		name[len(workspaceName)] == '-'
}

// eventTime prefers the series or last-seen time, falling back to creation.
// A repeated event updates the later fields and leaves creationTimestamp at the
// first occurrence, so reading only that would sort a still-firing event to the
// bottom.
func eventTime(event *corev1.Event) time.Time {
	if event.Series != nil && !event.Series.LastObservedTime.IsZero() {
		return event.Series.LastObservedTime.Time
	}
	if !event.LastTimestamp.IsZero() {
		return event.LastTimestamp.Time
	}
	if !event.EventTime.IsZero() {
		return event.EventTime.Time
	}
	return event.CreationTimestamp.Time
}

// relativeTime is "3m ago", which is what you want when reading a log and not
// what a timestamp gives you.
func relativeTime(at, now time.Time) string {
	if at.IsZero() {
		return "-"
	}
	elapsed := now.Sub(at)
	switch {
	case elapsed < time.Minute:
		return "just now"
	case elapsed < time.Hour:
		return fmt.Sprintf("%dm ago", int(elapsed.Minutes()))
	case elapsed < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(elapsed.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(elapsed.Hours()/24))
	}
}
