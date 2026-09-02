package ui

import (
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"

	dwpkv1alpha1 "github.com/devops-ia/dwpk/api/v1alpha1"
	"github.com/devops-ia/dwpk/internal/auth"
	"k8s.io/apimachinery/pkg/api/resource"
)

// ProfileData backs /profile: who the signed-in person is, what they are
// allowed to use, and what they are currently using.
type ProfileData struct {
	Session    RequestSession
	Quota      QuotaRow
	Workspaces []WorkspaceRow
	// LocalUsername is empty for a session that signed in through an identity
	// provider, which has no password to change here. The screen says so
	// rather than showing a form that cannot work.
	LocalUsername string
	Error         string
	Notice        string

	// Tokens are this person's API tokens. TokensEnabled distinguishes "token
	// auth is not configured" from "you have none", which look identical
	// otherwise and mean very different things.
	Tokens        []TokenRow
	TokensEnabled bool
	// Keys are this person's default SSH public keys, applied to workspaces
	// they create.
	Keys []KeyRow
	// GitSSHKeys are this person's private keys for git access to their own
	// repositories, mounted into every workspace in their namespace.
	GitSSHKeys []GitSSHKeyInfo
	// Issued is the plaintext of a token just created. It is shown once and is
	// never recoverable, so the screen has to say so beside it.
	Issued string
}

// WorkspaceRow is one of the signed-in person's own workspaces.
type WorkspaceRow struct {
	Name   string
	Image  string
	Size   string
	Status string
}

func (s *Server) handleProfile(w http.ResponseWriter, r *http.Request) {
	session, ok := requestSessionFrom(r.Context())
	if !ok {
		s.redirectToLogin(w, r)
		return
	}
	s.renderProfile(w, r, session, "", doneNotice(r))
}

// renderProfileIssued renders the profile with a freshly minted token on it.
// The plaintext is never stored, so this is the only moment it can be shown.
func (s *Server) renderProfileIssued(w http.ResponseWriter, r *http.Request, session RequestSession, issued string) {
	s.renderProfileWith(w, r, session, "", "", issued)
}

func (s *Server) renderProfile(w http.ResponseWriter, r *http.Request, session RequestSession, errorMessage, notice string) {
	s.renderProfileWith(w, r, session, errorMessage, notice, "")
}

func (s *Server) renderProfileWith(w http.ResponseWriter, r *http.Request, session RequestSession, errorMessage, notice, issued string) {
	api, err := s.clientFactory.ForToken(session.Token)
	if err != nil {
		s.writeErrorPage(w, r, err)
		return
	}
	userSpace, err := api.GetUserSpace(r.Context(), session.Identity.UserSpaceName)
	if err != nil {
		s.writeErrorPage(w, r, err)
		return
	}
	workspaces, err := api.ListWorkspaces(r.Context(), session.Identity.UserSpaceNamespace)
	if err != nil {
		s.writeErrorPage(w, r, err)
		return
	}
	gitSSHKeys, err := api.GetGitSSHKeys(r.Context(), session.Identity.UserSpaceNamespace)
	if err != nil {
		s.writeErrorPage(w, r, err)
		return
	}

	data := ProfileData{
		Session:       session,
		Quota:         quotaRows([]dwpkv1alpha1.UserSpace{*userSpace}, workspaces, s.gpuResource(r))[0],
		Workspaces:    workspaceRows(workspaces),
		LocalUsername: s.localUsernameFor(r, session.Identity),
		Error:         errorMessage,
		Notice:        notice,
		Keys:          keyRows(userSpace.Spec.SSHAuthorizedKeys),
		GitSSHKeys:    gitSSHKeys,
		Tokens:        s.profileTokens(r, session),
		TokensEnabled: s.apiTokens != nil,
		Issued:        issued,
	}

	status := http.StatusOK
	if errorMessage != "" {
		status = http.StatusBadRequest
	}
	s.renderAuthedPage(w, r, status, session, "My profile", ProfilePage(data))
}

func workspaceRows(workspaces []dwpkv1alpha1.Workspace) []WorkspaceRow {
	rows := make([]WorkspaceRow, 0, len(workspaces))
	for _, ws := range workspaces {
		rows = append(rows, WorkspaceRow{
			Name:   ws.Name,
			Image:  ws.Spec.ImageRef.Name,
			Status: ws.Status.State,
		})
	}
	slices.SortFunc(rows, func(a, b WorkspaceRow) int { return strings.Compare(a.Name, b.Name) })
	return rows
}

// localUsernameFor finds the caller's own local credential, if they have one.
// A lookup failure is reported as "no local user" rather than as an error
// page: the rest of the profile is still worth showing.
//
// A session that signed in through an identity provider gets "" without a
// lookup, even when the same person also holds a local account. The provider
// owns those credentials, so there is no password to change here. Every
// password path goes through this function, which is why the check lives here
// rather than in each handler.
func (s *Server) localUsernameFor(r *http.Request, identity SessionIdentity) string {
	owner := identity.Email
	if s.localUsers == nil || owner == "" || !identity.LocalLogin {
		return ""
	}
	users, err := s.localUsers.FindByOwner(r.Context(), owner)
	if err != nil || len(users) != 1 {
		return ""
	}
	return users[0].Username
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	session, ok := requestSessionFrom(r.Context())
	if !ok {
		s.redirectToLogin(w, r)
		return
	}
	if s.localUsers == nil {
		s.writeErrorPage(w, r, errLocalUsersDisabled)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.writeErrorPage(w, r, errInvalidForm)
		return
	}

	// The username comes from the session's owner, never from the form: a
	// posted username would let anyone change anyone's password.
	username := s.localUsernameFor(r, session.Identity)
	if username == "" {
		s.writeErrorPage(w, r, apiError{status: http.StatusForbidden, message: "this account has no password to change here"})
		return
	}
	if r.Form.Get("new_password") != r.Form.Get("confirm_password") {
		s.renderProfile(w, r, session, "The new passwords do not match.", "")
		return
	}

	err := s.localUsers.SetPassword(r.Context(), username, r.Form.Get("current_password"), r.Form.Get("new_password"))
	switch {
	case errors.Is(err, auth.ErrInvalidCredentials):
		s.renderProfile(w, r, session, "That is not your current password.", "")
	case err != nil:
		s.renderProfile(w, r, session, err.Error(), "")
	default:
		s.redirectToProfileTab(w, r, "password", "password")
	}
}

// redirectToProfileTab sends a mutation's caller back to /profile on the tab
// it came from, with the given doneMessages key rendered as the success
// notice.
//
// A redirect rather than a direct render: the browser's address bar ends up
// at a GET it can safely refresh, instead of the POST target ("Confirm form
// resubmission?"). The #tab fragment is never sent to the server - it only
// exists for app.js's selectTabFromHash, read once on the page's next load -
// which is what keeps the person on the tab they were using instead of
// bouncing them back to Overview.
func (s *Server) redirectToProfileTab(w http.ResponseWriter, r *http.Request, tab, doneKey string) {
	http.Redirect(w, r, s.path(donePath("/profile", doneKey))+"#"+tab, http.StatusSeeOther)
}

// quotaMeterValue and quotaMeterMax turn a Kubernetes quantity into a number
// <meter> understands. Milli-CPU and Gi are not comparable as strings, and a
// meter with mismatched units draws a lie.
func quotaMeterValue(quantity string) string {
	return quotaScalar(quantity, "0")
}

func quotaMeterMax(quantity string) string {
	// A max of zero makes every meter look full, so an unparseable or absent
	// limit falls back to 1 and pairs with a value of 0: an empty bar.
	return quotaScalar(quantity, "1")
}

func quotaScalar(quantity, fallback string) string {
	parsed, err := resource.ParseQuantity(strings.TrimSpace(quantity))
	if err != nil {
		return fallback
	}
	value := parsed.AsApproximateFloat64()
	if value <= 0 {
		return fallback
	}
	return strconv.FormatFloat(value, 'f', -1, 64)
}
