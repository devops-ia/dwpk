package ui

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/devops-ia/dwpk/internal/auth"
)

func (s *Server) handleLoginPicker(w http.ResponseWriter, r *http.Request) {
	next := strings.TrimSpace(r.URL.Query().Get(loginRedirectParam))

	// The drop-down is a plain GET form posting back here, so a chosen
	// provider arrives as a query parameter. Handling it in the picker keeps
	// the feature working without JavaScript and without a second route.
	if provider := strings.TrimSpace(r.URL.Query().Get("provider")); provider != "" {
		s.beginLogin(w, r, auth.Name(provider), next)
		return
	}

	configured := s.loginFlow.ConfiguredProviders()
	s.renderAnonymousPage(w, r, http.StatusOK, "Sign in",
		LoginPicker(providerOptions(configured), next, s.loginFlow.LocalAuthEnabled(), ""))
}

// signInProblem turns a failed login into something worth reading.
//
// A disabled account is named as such, and only that case is. It is reached
// only after the password has already been verified, so saying so tells an
// attacker nothing they did not have to know the password to learn. Everything
// else stays deliberately vague: which half of a wrong username and password
// was wrong is not something the sign-in page should be willing to say.
func (s *Server) signInProblem(r *http.Request, err error) string {
	if err == nil {
		return ""
	}
	if !errors.Is(err, ErrUserSpaceDisabled) {
		return "Those credentials were not accepted."
	}
	if support := s.platformConfig(r).Support(); support != "" {
		return "This account is disabled. Contact " + support + " to have it restored."
	}
	return "This account is disabled. Contact an administrator to have it restored."
}

func (s *Server) handleLocalLogin(w http.ResponseWriter, r *http.Request) {
	if !s.loginFlow.LocalAuthEnabled() {
		s.writeErrorPage(w, r, apiError{status: http.StatusNotFound, message: "local login is not enabled"})
		return
	}
	if err := r.ParseForm(); err != nil {
		s.writeErrorPage(w, r, errInvalidForm)
		return
	}

	sessionTTL := s.requestedSessionTTL(r.Form.Get(loginRememberParam))
	sessionID, err := s.loginFlow.CompleteLocalLogin(r.Context(), LocalLoginRequest{
		Username:   strings.TrimSpace(r.Form.Get("username")),
		Password:   r.Form.Get("password"),
		SessionTTL: sessionTTL,
	})
	if err != nil {
		s.renderAnonymousPage(w, r, http.StatusUnauthorized, "Sign in",
			LoginPicker(
				providerOptions(s.loginFlow.ConfiguredProviders()),
				strings.TrimSpace(r.Form.Get(loginRedirectParam)),
				true,
				s.signInProblem(r, err),
			))
		return
	}
	if _, err := s.csrfStore.Ensure(sessionID); err != nil {
		s.writeErrorPage(w, r, fmt.Errorf("create CSRF token: %w", err))
		return
	}
	http.SetCookie(w, s.cookie(sessionCookieName, sessionID, s.now().Add(sessionTTL)))

	redirectTo := s.path(s.landingPath(sessionID))
	if next := strings.TrimSpace(r.Form.Get(loginRedirectParam)); next != "" {
		if safe := safeRedirectPath(next); safe != "" {
			redirectTo = s.path(safe)
		}
	}
	http.Redirect(w, r, redirectTo, http.StatusFound)
}

func (s *Server) handleBeginLogin(w http.ResponseWriter, r *http.Request) {
	s.beginLogin(w, r, auth.Name(r.PathValue("provider")), strings.TrimSpace(r.URL.Query().Get(loginRedirectParam)))
}

// beginLogin starts the OAuth2 redirect for one provider. Both the drop-down
// and the /login/{provider} path route land here.
//
// An unconfigured provider is refused with 404 rather than the generic error
// page: the path route stays directly reachable whatever the drop-down offers,
// and "this provider does not exist here" is the honest answer.
func (s *Server) beginLogin(w http.ResponseWriter, r *http.Request, provider auth.Name, next string) {
	if !slices.Contains(s.loginFlow.ConfiguredProviders(), provider) {
		s.writeErrorPage(w, r, apiError{
			status:  http.StatusNotFound,
			message: "no such login provider is configured",
		})
		return
	}

	result, err := s.loginFlow.BeginLogin(provider)
	if err != nil {
		s.writeErrorPage(w, r, err)
		return
	}
	s.challengeStore.Put(provider, result.Challenge)
	http.SetCookie(w, s.cookie(loginStateCookieName, result.Challenge.State, result.Challenge.ExpiresAt))
	if next != "" {
		http.SetCookie(w, s.cookie(loginNextCookieName, next, result.Challenge.ExpiresAt))
	}
	// The provider round trip loses the form, so "remember me" rides a
	// short-lived cookie the same way the redirect target already does.
	if r.URL.Query().Get(loginRememberParam) != "" {
		http.SetCookie(w, s.cookie(loginRememberCookieName, boolTrue, result.Challenge.ExpiresAt))
	}
	http.Redirect(w, r, result.RedirectURL, http.StatusFound)
}

func (s *Server) handleCompleteLogin(w http.ResponseWriter, r *http.Request) {
	provider := auth.Name(r.PathValue("provider"))
	challengeCookie, err := r.Cookie(loginStateCookieName)
	if err != nil {
		s.writeErrorPage(w, r, apiError{status: http.StatusUnauthorized, message: "missing login state cookie"})
		return
	}
	state := strings.TrimSpace(r.URL.Query().Get("state"))
	if challengeCookie.Value != state {
		s.writeErrorPage(w, r, ErrStateMismatch)
		return
	}
	challenge, ok := s.challengeStore.Take(provider, state)
	if !ok {
		s.writeErrorPage(w, r, ErrLoginChallengeExpired)
		return
	}
	s.clearCookie(w, loginStateCookieName)

	sessionTTL := s.sessionTTL
	if remember, err := r.Cookie(loginRememberCookieName); err == nil && remember.Value == boolTrue {
		sessionTTL = rememberedSessionTTL
		s.clearCookie(w, loginRememberCookieName)
	}

	sessionID, err := s.loginFlow.CompleteLogin(r.Context(), CompleteLoginRequest{
		Provider:   provider,
		Code:       strings.TrimSpace(r.URL.Query().Get("code")),
		State:      state,
		Challenge:  challenge,
		SessionTTL: sessionTTL,
	})
	if errors.Is(err, ErrUserSpaceDisabled) {
		// Back to the sign-in page rather than the error page: this is an
		// outcome of signing in, and the person needs somewhere to go next.
		s.renderAnonymousPage(w, r, http.StatusForbidden, "Sign in",
			LoginPicker(
				providerOptions(s.loginFlow.ConfiguredProviders()), "",
				s.loginFlow.LocalAuthEnabled(), s.signInProblem(r, err),
			))
		return
	}
	if err != nil {
		s.writeErrorPage(w, r, err)
		return
	}
	if _, err := s.csrfStore.Ensure(sessionID); err != nil {
		s.writeErrorPage(w, r, fmt.Errorf("create CSRF token: %w", err))
		return
	}
	http.SetCookie(w, s.cookie(sessionCookieName, sessionID, s.now().Add(sessionTTL)))

	redirectTo := s.path(s.landingPath(sessionID))
	if nextCookie, err := r.Cookie(loginNextCookieName); err == nil {
		if safe := safeRedirectPath(strings.TrimSpace(nextCookie.Value)); safe != "" {
			redirectTo = s.path(safe)
		}
		s.clearCookie(w, loginNextCookieName)
	} else if next := strings.TrimSpace(r.URL.Query().Get(loginRedirectParam)); next != "" {
		if safe := safeRedirectPath(next); safe != "" {
			redirectTo = s.path(safe)
		}
	}
	http.Redirect(w, r, redirectTo, http.StatusFound)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	session, ok := requestSessionFrom(r.Context())
	if !ok {
		s.redirectToLogin(w, r)
		return
	}
	if err := s.loginFlow.Logout(session.SessionID); err != nil && !errors.Is(err, ErrSessionNotFound) {
		s.writeErrorPage(w, r, err)
		return
	}
	s.csrfStore.Delete(session.SessionID)
	s.clearCookie(w, sessionCookieName)
	http.Redirect(w, r, s.path("/login"), http.StatusFound)
}

func safeRedirectPath(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.IsAbs() || strings.HasPrefix(raw, "//") {
		return ""
	}
	if !strings.HasPrefix(parsed.Path, "/") {
		return ""
	}
	return raw
}

// requestedSessionTTL turns a "remember me" form value into a session window.
// Any non-empty value counts: an unticked checkbox submits nothing at all.
func (s *Server) requestedSessionTTL(remember string) time.Duration {
	if strings.TrimSpace(remember) == "" {
		return s.sessionTTL
	}
	return rememberedSessionTTL
}

func (s *Server) cookie(name, value string, expiresAt time.Time) *http.Cookie {
	return &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     s.cookiePath(),
		Expires:  expiresAt,
		HttpOnly: true,
		Secure:   s.cookieSecure,
		SameSite: http.SameSiteLaxMode,
	}
}

// landingPath is where a sign-in with nowhere particular to go ends up. Someone
// who has never finished the first-login wizard is shown it, because a product
// they have not seen before needs introducing.
//
// This is the whole of the wizard's hold over navigation. It was once a
// middleware that redirected every request until the wizard was finished, which
// made the wizard's own links — "Add an SSH key", "Browse the catalog" — bounce
// straight back to it. An identity that cannot be read is not worth failing a
// login over: Overview is the safe answer.
func (s *Server) landingPath(sessionID string) string {
	identity, err := s.loginFlow.SessionIdentity(sessionID)
	if err != nil || !identity.OnboardingPending {
		return "/"
	}
	return onboardingPath
}
