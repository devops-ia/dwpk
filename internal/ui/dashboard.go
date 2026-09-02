package ui

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"

	dwpkv1alpha1 "github.com/devops-ia/dwpk/api/v1alpha1"
)

// DashboardData backs the screen a signed-in person lands on. It answers "what
// do I have running, and how much of my allowance is left" - the two questions
// the app used to make you visit two other screens to answer.
type DashboardData struct {
	Session    RequestSession
	Cards      []WorkspaceCard
	Quota      QuotaRow
	HasQuota   bool
	Running    int
	Stopped    int
	ImageCount int
}

// WorkspaceCard is one workspace as it appears on the dashboard: enough to
// decide what to do with it without opening it.
type WorkspaceCard struct {
	Name  string
	Image string
	// Resources reads "500m CPU · 1Gi", the shape at a glance.
	Resources string
	Status    string
	Running   bool
	// Uptime is how long the workspace has held its current state, taken from
	// the Ready condition's transition time. Empty when the controller has not
	// reported one yet.
	Uptime string
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
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
	workspaces, err := api.ListWorkspaces(r.Context(), namespace)
	if err != nil {
		s.writeErrorPage(w, r, err)
		return
	}
	images, err := api.ListWorkspaceImages(r.Context())
	if err != nil {
		s.writeErrorPage(w, r, err)
		return
	}

	data := DashboardData{
		Session:    session,
		Cards:      workspaceCards(workspaces, s.now()),
		ImageCount: len(images),
	}
	for _, ws := range workspaces {
		if ws.Spec.Running {
			data.Running++
		} else {
			data.Stopped++
		}
	}

	// The quota strip is a nicety, not the point of the page. A UserSpace the
	// caller cannot read - which is normal while one is still being created -
	// leaves the bars off rather than failing the whole screen.
	if userSpace, err := api.GetUserSpace(r.Context(), session.Identity.UserSpaceName); err == nil {
		rows := quotaRows([]dwpkv1alpha1.UserSpace{*userSpace}, workspaces, s.gpuResource(r))
		if len(rows) > 0 {
			data.Quota = rows[0]
			data.HasQuota = true
		}
	}

	s.renderAuthedPage(w, r, http.StatusOK, session, "Overview", DashboardPage(data))
}

func workspaceCards(workspaces []dwpkv1alpha1.Workspace, now time.Time) []WorkspaceCard {
	cards := make([]WorkspaceCard, 0, len(workspaces))
	for _, ws := range workspaces {
		cards = append(cards, WorkspaceCard{
			Name:      ws.Name,
			Image:     ws.Spec.ImageRef.Name,
			Resources: resourceSummary(ws.Spec.Resources.Requests),
			Status:    ws.Status.State,
			Running:   ws.Spec.Running,
			Uptime:    stateAge(ws, now),
		})
	}
	return cards
}

// stateAge reports how long the workspace has been in its current state, read
// from the Ready condition rather than from a field of our own: the controller
// already stamps a transition time there every time the state changes.
func stateAge(ws dwpkv1alpha1.Workspace, now time.Time) string {
	for _, condition := range ws.Status.Conditions {
		if condition.Type != conditionTypeReady || condition.LastTransitionTime.IsZero() {
			continue
		}
		return shortDuration(now.Sub(condition.LastTransitionTime.Time))
	}
	return ""
}

// shortDuration prints an age the way kubectl does. Anything under a minute is
// "just now": a workspace that has been up for 40 seconds is not usefully
// different from one that has been up for 20.
func shortDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + "m"
	case d < 24*time.Hour:
		return strconv.Itoa(int(d.Hours())) + "h"
	default:
		return strconv.Itoa(int(d.Hours()/24)) + "d"
	}
}

// resourceSummary is the shape of a workspace in a few characters, for a card
// that has one line to say it in.
func resourceSummary(requests corev1.ResourceList) string {
	parts := []string{}
	if cpu, ok := requests[corev1.ResourceCPU]; ok {
		parts = append(parts, cpu.String()+" CPU")
	}
	if memory, ok := requests[corev1.ResourceMemory]; ok {
		parts = append(parts, memory.String())
	}
	return strings.Join(parts, " · ")
}
