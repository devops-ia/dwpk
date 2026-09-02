package ui

import (
	"net/http"
	"slices"
	"strings"

	dwpkv1alpha1 "github.com/devops-ia/dwpk/api/v1alpha1"
	"github.com/devops-ia/dwpk/internal/auth"
)

// PersonRow is one human on the merged users screen: their UserSpace and any
// local logins that share its owner.
//
// The two used to be separate screens, which meant an operator had to know
// that "UserSpace alice" and "local user alice" were the same person, and had
// no way to see when the two had drifted apart.
type PersonRow struct {
	UserSpaceName string
	Owner         string
	// UID is metadata.uid - the identifier Kubernetes already assigns and
	// guarantees unique. There is no second UUID field, because two identifiers
	// for one object eventually disagree about which is canonical.
	UID       string
	Username  string
	Email     string
	Namespace string
	Status    string
	Role      string
	Disabled  bool
	Quota     dwpkv1alpha1.UserSpaceQuota
	// LocalUsers are the local logins joined on owner. Empty means the person
	// signs in through an identity provider, which is the normal case.
	LocalUsers []LocalUserResponse
	// HasUserSpace is false for a local login whose owner matches no UserSpace.
	// Such an account exists but cannot sign in, and the screen says so rather
	// than hiding it.
	HasUserSpace bool
}

// PeopleData backs GET /admin/users.
type PeopleData struct {
	Session   RequestSession
	Rows      []PersonRow
	LocalAuth bool
	Error     string
}

func (s *Server) handleAdminPeople(w http.ResponseWriter, r *http.Request) {
	session, ok := s.requireUserSpaceAdmin(w, r)
	if !ok {
		return
	}
	s.renderPeople(w, r, session, "")
}

func (s *Server) renderPeople(w http.ResponseWriter, r *http.Request, session RequestSession, message string) {
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

	var localUsers []auth.LocalUser
	if s.localUsers != nil {
		if localUsers, err = s.localUsers.List(r.Context()); err != nil {
			s.writeErrorPage(w, r, err)
			return
		}
	}

	status := http.StatusOK
	if message != "" {
		status = http.StatusBadRequest
	}
	s.renderAuthedPage(w, r, status, session, "Users", AdminPeoplePage(PeopleData{
		Session:   session,
		Rows:      peopleRows(userSpaces, localUsers),
		LocalAuth: s.localUsers != nil,
		Error:     message,
	}))
}

// peopleRows joins UserSpaces to local logins on the owner. Every input on
// either side produces a row: a local login whose owner has no UserSpace is a
// misconfiguration worth seeing, not a record to drop.
func peopleRows(
	userSpaces []dwpkv1alpha1.UserSpace,
	localUsers []auth.LocalUser,
) []PersonRow {
	loginsByOwner := make(map[string][]LocalUserResponse, len(localUsers))
	for _, user := range localUsers {
		loginsByOwner[user.Owner] = append(loginsByOwner[user.Owner], localUserResponse(user))
	}

	rows := make([]PersonRow, 0, len(userSpaces)+len(loginsByOwner))
	for _, userSpace := range userSpaces {
		owner := userSpace.Spec.Owner
		rows = append(rows, PersonRow{
			UserSpaceName: userSpace.Name,
			Owner:         owner,
			UID:           string(userSpace.UID),
			Username:      userSpace.LoginName(),
			Email:         userSpace.ContactEmail(),
			// status.namespace is the authority: spec.namespace is a request,
			// and until the controller has acted on it the two differ.
			Namespace:    userSpace.Status.Namespace,
			Status:       userSpace.Status.State,
			Role:         userSpace.Spec.EffectiveRole(),
			Disabled:     userSpace.Spec.Disabled,
			Quota:        userSpace.Spec.Quota,
			LocalUsers:   loginsByOwner[owner],
			HasUserSpace: true,
		})
		delete(loginsByOwner, owner)
	}

	for owner, logins := range loginsByOwner {
		rows = append(rows, PersonRow{Owner: owner, LocalUsers: logins})
	}

	slices.SortFunc(rows, func(a, b PersonRow) int {
		if order := strings.Compare(a.Owner, b.Owner); order != 0 {
			return order
		}
		return strings.Compare(a.UserSpaceName, b.UserSpaceName)
	})
	return rows
}

// requireUserSpaceAdmin gates the screen on the caller's own RBAC. It differs
// from requireLocalUserAdmin in tolerating local auth being switched off: the
// merged screen still has UserSpaces to show.
func (s *Server) requireUserSpaceAdmin(w http.ResponseWriter, r *http.Request) (RequestSession, bool) {
	session, ok := requestSessionFrom(r.Context())
	if !ok {
		s.redirectToLogin(w, r)
		return RequestSession{}, false
	}
	api, err := s.clientFactory.ForToken(session.Token)
	if err != nil {
		s.writeErrorPage(w, r, err)
		return RequestSession{}, false
	}
	allowed, err := api.CanI(r.Context(), "delete", "userspaces", "")
	if err != nil {
		s.writeErrorPage(w, r, err)
		return RequestSession{}, false
	}
	if !allowed {
		s.writeErrorPage(w, r, apiError{status: http.StatusForbidden, message: "administrator privileges required"})
		return RequestSession{}, false
	}
	return session, true
}

// signInSummary describes how a person gets in. It names the local usernames
// when there are any, because more than one local login for the same owner is
// possible and an operator needs to see all of them.
func signInSummary(row PersonRow) string {
	if len(row.LocalUsers) == 0 {
		return "Identity provider"
	}
	names := make([]string, 0, len(row.LocalUsers))
	for _, user := range row.LocalUsers {
		names = append(names, user.Username)
	}
	return strings.Join(names, ", ")
}

// sessionAPI is the pair every mutating admin handler needs: the session, and a
// Kubernetes client built from that session's own token.
//
// It holds no authorization of its own. Whether the caller may do the thing is
// decided by the API server when the call is made, which is the rule the whole
// UI is built on (SPEC §8.1) - this only saves four handlers from repeating the
// same two error paths.
func (s *Server) sessionAPI(w http.ResponseWriter, r *http.Request) (RequestSession, RequestAPI, bool) {
	session, ok := requestSessionFrom(r.Context())
	if !ok {
		s.redirectToLogin(w, r)
		return RequestSession{}, nil, false
	}
	api, err := s.clientFactory.ForToken(session.Token)
	if err != nil {
		s.writeErrorPage(w, r, err)
		return RequestSession{}, nil, false
	}
	return session, api, true
}
