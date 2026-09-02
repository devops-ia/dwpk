package ui

import (
	"net/http"
	"net/url"
	"strings"
	"time"
)

// maxRequestBodyBytes bounds every request body the UI accepts, comfortably
// above the largest legitimate one (the settings form's optional logo
// upload, csrfFormMaxMemory in settings.go). Without a cap here,
// ParseForm/ParseMultipartForm read an attacker-controlled body of any size
// before either ever looks at csrfFormMaxMemory - that constant only bounds
// what a multipart parse keeps in memory versus spills to a temp file, not
// how much it will read in the first place.
const maxRequestBodyBytes = 4 << 20

func (s *Server) withSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
		next.ServeHTTP(w, r)
	})
}

func (s *Server) withSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookieName)
		if err != nil || strings.TrimSpace(cookie.Value) == "" {
			s.redirectToLogin(w, r)
			return
		}

		identity, err := s.loginFlow.SessionIdentity(cookie.Value)
		if err != nil {
			s.csrfStore.Delete(cookie.Value)
			s.clearCookie(w, sessionCookieName)
			s.redirectToLogin(w, r)
			return
		}
		token, err := s.loginFlow.MintTokenForSession(r.Context(), cookie.Value)
		if err != nil {
			s.writeErrorPage(w, r, err)
			return
		}
		csrfToken, err := s.csrfStore.Ensure(cookie.Value)
		if err != nil {
			s.writeErrorPage(w, r, err)
			return
		}
		if requestNeedsCSRF(r.Method) && !s.csrfStore.Valid(cookie.Value, requestCSRFToken(r)) {
			s.writeErrorPage(w, r, apiError{status: http.StatusForbidden, message: "invalid CSRF token"})
			return
		}

		session := RequestSession{SessionID: cookie.Value, Identity: identity, Token: token, CSRFToken: csrfToken}
		next.ServeHTTP(w, r.WithContext(withRequestSession(r.Context(), session)))
	})
}

// onboardingPath and onboardingCompletePath are the wizard's own routes,
// shared between the route registration in server.go and the landing path a
// first sign-in is sent to, so the two can't drift.
const (
	onboardingPath         = "/onboarding"
	onboardingCompletePath = "/onboarding/complete"
)

func requestNeedsCSRF(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

func requestCSRFToken(r *http.Request) string {
	if token := strings.TrimSpace(r.Header.Get(csrfHeaderName)); token != "" {
		return token
	}
	// ParseForm does not read multipart/form-data bodies (only the URL query
	// for those) — the settings form is multipart because it carries an
	// optional logo file, so without this the token here is always empty and
	// every settings save is rejected before the handler's own
	// ParseMultipartForm ever runs. csrfFormMaxMemory matches the cap
	// settings.go itself uses so the two parses of the same body agree.
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		if err := r.ParseMultipartForm(csrfFormMaxMemory); err == nil {
			return strings.TrimSpace(r.Form.Get("csrf_token"))
		}
	}
	if err := r.ParseForm(); err == nil {
		return strings.TrimSpace(r.Form.Get("csrf_token"))
	}
	return ""
}

func (s *Server) redirectToLogin(w http.ResponseWriter, r *http.Request) {
	path := r.URL.RequestURI()
	target := s.path("/login")
	if path != "" && path != "/login" {
		target += "?" + loginRedirectParam + "=" + url.QueryEscape(path)
	}
	http.Redirect(w, r, target, http.StatusFound)
}

func (s *Server) clearCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Path:     s.cookiePath(),
		Value:    "",
		Expires:  s.now().Add(-time.Hour),
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   s.cookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
}
