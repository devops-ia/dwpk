package ui

import (
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/devops-ia/dwpk/internal/auth"
	workspacepkg "github.com/devops-ia/dwpk/internal/workspace"
)

// TokenRow is one API token as the profile screen lists it. The plaintext is
// deliberately absent: it exists only in the response that created the token.
type TokenRow struct {
	Name      string
	Scope     string
	CreatedAt string
	// Expires reads "Never" for a token with no expiry, so the column never
	// leaves a blank that could mean either "never" or "we do not know".
	Expires string
}

// TokenExpiryOption is one choice in the expiry picker.
type TokenExpiryOption struct {
	Value string
	Label string
}

// tokenExpiryOptions are presets rather than a date picker: a token's lifetime
// has a handful of sensible answers, and "never" must be one of them because CI
// jobs and service integrations outlive any date somebody picks today.
func tokenExpiryOptions() []TokenExpiryOption {
	return []TokenExpiryOption{
		{Value: "30d", Label: "30 days"},
		{Value: "7d", Label: "7 days"},
		{Value: "90d", Label: "90 days"},
		{Value: neverLabel, Label: "No expiration"},
	}
}

// tokenExpiryFrom turns a preset into a moment.
//
// Anything unrecognised falls back to the default rather than to "never": for a
// credential, the safe direction to fail in is the one that stops working on its
// own.
func tokenExpiryFrom(choice string) time.Time {
	switch choice {
	case neverLabel:
		return time.Time{}
	case "7d":
		return time.Now().Add(7 * 24 * time.Hour)
	case "90d":
		return time.Now().Add(90 * 24 * time.Hour)
	default:
		return time.Now().Add(30 * 24 * time.Hour)
	}
}

func expiryLabel(at time.Time) string {
	if at.IsZero() {
		return "Never"
	}
	return at.Format(time.DateOnly)
}

// profileTokens lists this person's API tokens.
//
// The scope is read back from which ServiceAccount the token mints for, not
// from a stored label. That account's RBAC is what actually decides what the
// token can do, so deriving the label from it means the screen cannot claim a
// scope the token does not have.
func (s *Server) profileTokens(r *http.Request, session RequestSession) []TokenRow {
	if s.apiTokens == nil {
		return nil
	}
	records, err := s.apiTokens.List(r.Context(), auth.TokenKindApplication, session.Identity.UserSpaceNamespace)
	if err != nil {
		// The token list is one panel on this page, not its purpose. Failing the
		// whole profile because tokens could not be read would take the quota and
		// the password form down with it.
		return nil
	}
	rows := make([]TokenRow, 0, len(records))
	for _, record := range records {
		rows = append(rows, TokenRow{
			Name:      record.SecretName,
			Scope:     scopeOf(record.SubjectServiceAccount),
			CreatedAt: record.CreatedAt.Format(time.DateTime),
			Expires:   expiryLabel(record.ExpiresAt),
		})
	}
	return rows
}

func scopeOf(serviceAccount string) string {
	if serviceAccount == workspacepkg.ReadOnlySessionServiceAccountName {
		return string(workspacepkg.TokenScopeRead)
	}
	return string(workspacepkg.TokenScopeFull)
}

// handleProfileIssueToken creates a token at the chosen scope.
//
// The scope picks a ServiceAccount and stops mattering after that: the account's
// RBAC is the enforcement, so no later request has to remember what scope a
// token was issued at, and no check here can be forgotten (SPEC §8.1).
func (s *Server) handleProfileIssueToken(w http.ResponseWriter, r *http.Request) {
	session, ok := requestSessionFrom(r.Context())
	if !ok {
		s.redirectToLogin(w, r)
		return
	}
	if s.apiTokens == nil {
		s.writeErrorPage(w, r, errAPITokensDisabled)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.writeErrorPage(w, r, errInvalidForm)
		return
	}

	scope := workspacepkg.TokenScope(strings.TrimSpace(r.Form.Get("scope")))
	record, err := s.apiTokens.Issue(r.Context(), auth.TokenGrant{
		Kind:                  auth.TokenKindApplication,
		SubjectNamespace:      session.Identity.UserSpaceNamespace,
		SubjectServiceAccount: workspacepkg.ServiceAccountForScope(scope),
		ExpiresAt:             tokenExpiryFrom(strings.TrimSpace(r.Form.Get("expires"))),
	})
	if err != nil {
		s.renderProfile(w, r, session, err.Error(), "")
		return
	}

	// Rendered rather than redirected: the plaintext exists only in this
	// response, and a redirect would throw it away.
	s.renderProfileIssued(w, r, session, record.Plaintext)
}

// handleProfileRevokeToken deletes one of this person's own tokens.
//
// Ownership is checked by listing theirs first. Every token Secret lives in one
// namespace the caller cannot see, so a name on its own would let anyone revoke
// anyone's token - the API server cannot refuse it, because the UI's own
// credential is what reads that namespace.
func (s *Server) handleProfileRevokeToken(w http.ResponseWriter, r *http.Request) {
	session, ok := requestSessionFrom(r.Context())
	if !ok {
		s.redirectToLogin(w, r)
		return
	}
	if s.apiTokens == nil {
		s.writeErrorPage(w, r, errAPITokensDisabled)
		return
	}

	name := r.PathValue("name")
	mine := s.profileTokens(r, session)
	if !slices.ContainsFunc(mine, func(row TokenRow) bool { return row.Name == name }) {
		s.writeErrorPage(w, r, apiError{status: http.StatusNotFound, message: "no such token"})
		return
	}
	if err := s.apiTokens.Revoke(r.Context(), name); err != nil {
		s.renderProfile(w, r, session, err.Error(), "")
		return
	}
	s.redirectToProfileTab(w, r, "api", "token-revoked")
}
