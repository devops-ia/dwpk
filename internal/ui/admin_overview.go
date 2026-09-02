package ui

import (
	"net/http"

	dwpkv1alpha1 "github.com/devops-ia/dwpk/api/v1alpha1"
)

// AdminOverviewData is the whole platform on one screen: how many of each thing
// there are, and who is on it. It answers "what is on this cluster" without
// visiting four screens and doing the joins by eye.
type AdminOverviewData struct {
	Session RequestSession
	Notice  string

	Users      int
	Workspaces int
	Running    int

	// UserList is everyone, with their workspace count already joined on.
	UserList []UserSummary
}

type UserSummary struct {
	Name       string
	Owner      string
	Role       string
	Namespace  string
	Workspaces int
	Disabled   bool
}

func (s *Server) handleAdminOverview(w http.ResponseWriter, r *http.Request) {
	session, ok := s.requireUserSpaceAdmin(w, r)
	if !ok {
		return
	}
	api, err := s.clientFactory.ForToken(session.Token)
	if err != nil {
		s.writeErrorPage(w, r, err)
		return
	}
	userSpaces, err := api.ListUserSpaces(r.Context())
	if err != nil {
		s.writeErrorPage(w, r, err)
		return
	}
	workspaces, err := visibleWorkspaces(r.Context(), api, session.Identity)
	if err != nil {
		s.writeErrorPage(w, r, err)
		return
	}

	data := overviewData(userSpaces, workspaces)
	data.Session = session
	data.Notice = doneNotice(r)
	s.renderAuthedPage(w, r, http.StatusOK, session, "Administration", AdminOverviewPage(data))
}

// overviewData is pure: it takes what the cluster said and returns what the
// screen shows, so the grouping is table-testable without an API server.
func overviewData(
	userSpaces []dwpkv1alpha1.UserSpace,
	workspaces []dwpkv1alpha1.Workspace,
) AdminOverviewData {
	workspacesByNamespace := map[string]int{}
	running := 0
	for _, ws := range workspaces {
		workspacesByNamespace[ws.Namespace]++
		if ws.Spec.Running {
			running++
		}
	}

	users := make([]UserSummary, 0, len(userSpaces))
	for _, us := range userSpaces {
		namespace := us.Status.Namespace
		users = append(users, UserSummary{
			Name:       us.Name,
			Owner:      us.Spec.Owner,
			Role:       us.Spec.Role,
			Namespace:  namespace,
			Workspaces: workspacesByNamespace[namespace],
			Disabled:   us.Spec.Disabled,
		})
	}

	data := AdminOverviewData{
		Users:      len(userSpaces),
		Workspaces: len(workspaces),
		Running:    running,
	}
	data.UserList = users
	return data
}
