package ui

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/devops-ia/dwpk/internal/auth"
)

// ResetLinkData is what an administrator sees after issuing a link. The link is
// shown once and never again - it exists only in this response, like the
// initial admin password and an API token.
type ResetLinkData struct {
	Session  RequestSession
	Username string
	Link     string
	Expires  string
}

// ResetPasswordData backs the unauthenticated page a link leads to.
type ResetPasswordData struct {
	Token string
	Error string
	// Valid is false once the token has been rejected, in which case the form is
	// replaced by an explanation rather than left there to be retried.
	Valid bool
}

// handleAdminIssueReset creates a reset link for a local user.
//
// The link is handed back to the administrator rather than emailed. dwpk has no
// mail infrastructure, and adding one would mean an SMTP credential, egress
// from the UI pod and a class of delivery failures to diagnose - for a step a
// human is already standing in front of. This mirrors how the initial admin
// password is delivered.
func (s *Server) handleAdminIssueReset(w http.ResponseWriter, r *http.Request) {
	session, ok := s.requireLocalUserAdmin(w, r)
	if !ok {
		return
	}
	if s.passwordResets == nil {
		s.writeErrorPage(w, r, errLocalUsersDisabled)
		return
	}

	username := r.PathValue("name")
	token, expires, err := s.passwordResets.Issue(r.Context(), username)
	if err != nil {
		s.renderPeople(w, r, session, err.Error())
		return
	}

	s.renderAuthedPage(w, r, http.StatusOK, session, "Password reset", ResetLinkPage(ResetLinkData{
		Session:  session,
		Username: username,
		Link:     s.absoluteResetLink(r, token),
		Expires:  expires.Format(time.DateTime) + " UTC",
	}))
}

// absoluteResetLink builds the URL to hand over. It is absolute because the
// recipient pastes it into a browser rather than clicking it inside the app,
// and it honours the base path so a dwpk behind a sub-path still works.
func (s *Server) absoluteResetLink(r *http.Request, token string) string {
	scheme := "https"
	if r.TLS == nil && !strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "http"
	}
	return scheme + "://" + r.Host + s.path("/reset/"+token)
}

// handleResetForm renders the page a link leads to. It is deliberately
// unauthenticated: the person using it cannot sign in, which is the whole
// reason they were sent it.
func (s *Server) handleResetForm(w http.ResponseWriter, r *http.Request) {
	if s.passwordResets == nil {
		s.writeErrorPage(w, r, errLocalUsersDisabled)
		return
	}
	// The token is not checked here. Checking it would turn this page into an
	// oracle for guessing tokens, and the form is harmless to render.
	s.renderAnonymousPage(w, r, http.StatusOK, "Set a new password",
		ResetPasswordPage(ResetPasswordData{Token: r.PathValue("token"), Valid: true}))
}

// handleResetSubmit redeems a token and sets the password.
func (s *Server) handleResetSubmit(w http.ResponseWriter, r *http.Request) {
	if s.passwordResets == nil || s.localUsers == nil {
		s.writeErrorPage(w, r, errLocalUsersDisabled)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.writeErrorPage(w, r, errInvalidForm)
		return
	}

	token := strings.TrimSpace(r.Form.Get("token"))
	password := r.Form.Get("password")
	if password == "" || password != r.Form.Get("confirm") {
		s.renderAnonymousPage(w, r, http.StatusBadRequest, "Set a new password",
			ResetPasswordPage(ResetPasswordData{
				Token: token,
				Valid: true,
				Error: "The two passwords do not match.",
			}))
		return
	}

	// Redeemed before the password is written, and consumed either way. A token
	// that failed to complete a reset must not be retryable - asking for a new
	// link is the safe failure.
	username, err := s.passwordResets.Redeem(r.Context(), token)
	if err != nil {
		status := http.StatusBadRequest
		if !errors.Is(err, auth.ErrResetTokenInvalid) {
			status = http.StatusInternalServerError
		}
		s.renderAnonymousPage(w, r, status, "Set a new password",
			ResetPasswordPage(ResetPasswordData{
				Error: "This link is invalid or has expired. Ask an administrator for a new one.",
			}))
		return
	}

	if err := s.localUsers.ResetPassword(r.Context(), username, password); err != nil {
		s.renderAnonymousPage(w, r, http.StatusBadRequest, "Set a new password",
			ResetPasswordPage(ResetPasswordData{Error: err.Error()}))
		return
	}
	http.Redirect(w, r, s.path("/login"), http.StatusSeeOther)
}
