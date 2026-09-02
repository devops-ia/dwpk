package ui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/devops-ia/dwpk/internal/auth"
	"github.com/devops-ia/dwpk/internal/workspace"
)

const (
	bearerPrefix = "Bearer "
	// errorField is the single key every JSON error reply uses.
	errorField = "error"
)

var (
	errAPITokensDisabled = errors.New("API token authentication is not configured")
	errNoCredentials     = apiError{status: http.StatusUnauthorized, message: "missing session cookie or bearer token"}
)

// withAPISession authenticates /api/v1 requests by either a dwpk_ bearer
// token or the browser session cookie, and answers in JSON rather than
// rendering an error page.
//
// The two credentials differ in CSRF exposure, so they differ in treatment: a
// bearer token has to be attached deliberately by the client, while a cookie
// rides along on any cross-site request, so only the cookie path carries the
// CSRF check the browser UI already uses.
func (s *Server) withAPISession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if bearer := bearerToken(r); bearer != "" {
			session, err := s.sessionFromAPIToken(r.Context(), bearer)
			if err != nil {
				writeJSONError(w, err)
				return
			}
			next.ServeHTTP(w, r.WithContext(withRequestSession(r.Context(), session)))
			return
		}

		session, err := s.sessionFromCookie(w, r)
		if err != nil {
			writeJSONError(w, err)
			return
		}
		if requestNeedsCSRF(r.Method) && !s.csrfStore.Valid(session.SessionID, requestCSRFToken(r)) {
			writeJSONError(w, apiError{status: http.StatusForbidden, message: "invalid CSRF token"})
			return
		}
		next.ServeHTTP(w, r.WithContext(withRequestSession(r.Context(), session)))
	})
}

// sessionFromAPIToken resolves a dwpk_ bearer token to the ServiceAccount it
// was issued for, then mints a fresh short-lived Kubernetes token for that
// ServiceAccount. The stored token is only ever an identity claim; every
// Kubernetes call still runs under a freshly minted, expiring credential.
func (s *Server) sessionFromAPIToken(ctx context.Context, bearer string) (RequestSession, error) {
	if !auth.LooksLikeAPIToken(bearer) {
		return RequestSession{}, apiError{status: http.StatusUnauthorized, message: "bearer token is not a dwpk API token"}
	}
	if s.apiTokens == nil || s.tokenMinter == nil {
		return RequestSession{}, errAPITokensDisabled
	}

	record, err := s.apiTokens.Lookup(ctx, bearer)
	if err != nil {
		if errors.Is(err, auth.ErrTokenNotFound) {
			return RequestSession{}, apiError{status: http.StatusUnauthorized, message: "unknown API token"}
		}
		return RequestSession{}, err
	}

	serviceAccount := record.SubjectServiceAccount
	if serviceAccount == "" {
		serviceAccount = workspace.SessionServiceAccountName
	}
	token, _, err := s.tokenMinter.Mint(ctx, record.SubjectNamespace, serviceAccount)
	if err != nil {
		return RequestSession{}, err
	}

	identity := SessionIdentity{UserSpaceNamespace: record.SubjectNamespace}
	// Resolve the owning UserSpace so a token session carries the same project
	// membership a browser session does. Without it the catalog filter fell
	// back to the default project for every token holder, quietly showing the
	// wrong entries to anyone outside it.
	if s.clientFactory != nil {
		if resolved, err := s.identityForNamespace(ctx, token, record.SubjectNamespace); err == nil {
			identity = resolved
		}
	}

	return RequestSession{Identity: identity, Token: token}, nil
}

// identityForNamespace finds the UserSpace a namespace belongs to and reads its
// membership. It runs under the caller's own freshly minted token, so it can
// only see what that identity is allowed to see.
func (s *Server) identityForNamespace(ctx context.Context, token, namespace string) (SessionIdentity, error) {
	api, err := s.clientFactory.ForToken(token)
	if err != nil {
		return SessionIdentity{}, err
	}
	userSpaces, err := api.ListUserSpaces(ctx)
	if err != nil {
		return SessionIdentity{}, err
	}
	for i := range userSpaces {
		if userSpaces[i].Status.Namespace != namespace {
			continue
		}
		return SessionIdentity{
			UserSpaceName:      userSpaces[i].Name,
			UserSpaceNamespace: namespace,
			Role:               userSpaces[i].Spec.EffectiveRole(),
		}, nil
	}
	return SessionIdentity{}, fmt.Errorf("no UserSpace owns namespace %q", namespace)
}

// sessionFromCookie repeats what withSession does for HTML routes, minus the
// redirect-to-login: an API client gets a 401 it can act on, not a login page.
func (s *Server) sessionFromCookie(w http.ResponseWriter, r *http.Request) (RequestSession, error) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || strings.TrimSpace(cookie.Value) == "" {
		return RequestSession{}, errNoCredentials
	}

	identity, err := s.loginFlow.SessionIdentity(cookie.Value)
	if err != nil {
		s.csrfStore.Delete(cookie.Value)
		s.clearCookie(w, sessionCookieName)
		return RequestSession{}, apiError{status: http.StatusUnauthorized, message: "session expired"}
	}
	token, err := s.loginFlow.MintTokenForSession(r.Context(), cookie.Value)
	if err != nil {
		return RequestSession{}, err
	}
	csrfToken, err := s.csrfStore.Ensure(cookie.Value)
	if err != nil {
		return RequestSession{}, err
	}

	return RequestSession{SessionID: cookie.Value, Identity: identity, Token: token, CSRFToken: csrfToken}, nil
}

func bearerToken(r *http.Request) string {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(header) <= len(bearerPrefix) || !strings.EqualFold(header[:len(bearerPrefix)], bearerPrefix) {
		return ""
	}
	return strings.TrimSpace(header[len(bearerPrefix):])
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if body != nil {
		_ = json.NewEncoder(w).Encode(body)
	}
}

func writeJSONError(w http.ResponseWriter, err error) {
	status, message := statusAndMessage(err)
	writeJSON(w, status, map[string]string{errorField: message})
}
