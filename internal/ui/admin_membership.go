package ui

import (
	"net/http"
	"strings"

	dwpkv1alpha1 "github.com/devops-ia/dwpk/api/v1alpha1"
)

// handleAdminUpdateMembership moves a user between projects, promotes or
// demotes them, and enables or disables their account.
//
// The write goes out under the administrator's own token, so an operator
// without the admin ClusterRole gets a 403 from the API server rather than a
// courtesy check here.
func (s *Server) handleAdminUpdateMembership(w http.ResponseWriter, r *http.Request) {
	session, ok := requestSessionFrom(r.Context())
	if !ok {
		s.redirectToLogin(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.writeErrorPage(w, r, errInvalidForm)
		return
	}
	membership, err := membershipFromForm(r.PathValue("name"), r.Form.Get("role"), r.Form.Get("disabled"))
	if err != nil {
		s.writeErrorPage(w, r, err)
		return
	}
	api, err := s.clientFactory.ForToken(session.Token)
	if err != nil {
		s.writeErrorPage(w, r, err)
		return
	}
	if err := api.PatchUserSpaceMembership(r.Context(), membership); err != nil {
		s.writeErrorPage(w, r, err)
		return
	}
	http.Redirect(w, r, s.path("/admin/users"), http.StatusFound)
}

// membershipFromForm validates the editable fields before they reach the API
// server. The CRD enum would reject a bad role anyway; catching it here makes
// the message about the form rather than about a merge patch.
//
// Granting "admin" is accepted here and refused at admission if the
// caller is not one already - the API server is the only place that can judge
// the requester's own rights.
func membershipFromForm(name, role, disabled string) (UserSpaceMembership, error) {
	if name == "" {
		return UserSpaceMembership{}, apiError{status: http.StatusBadRequest, message: "userspace name required"}
	}

	role = roleOrDefault(strings.TrimSpace(role))
	if !validRoles[role] {
		return UserSpaceMembership{}, apiError{
			status:  http.StatusBadRequest,
			message: "role must be one of user or admin",
		}
	}

	return UserSpaceMembership{
		Name:     name,
		Role:     role,
		Disabled: isTruthy(disabled),
	}, nil
}

var validRoles = map[string]bool{
	dwpkv1alpha1.UserSpaceRoleUser:  true,
	dwpkv1alpha1.UserSpaceRoleAdmin: true,
}

func roleOrDefault(role string) string {
	if role == "" {
		return dwpkv1alpha1.UserSpaceRoleUser
	}
	return role
}

func isTruthy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", boolTrue, "on", "yes":
		return true
	default:
		return false
	}
}
