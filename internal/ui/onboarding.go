package ui

import "net/http"

// OnboardingData backs /onboarding: just the session, since the wizard
// explains rather than reads any state back from the cluster.
type OnboardingData struct {
	Session RequestSession
}

// handleOnboarding renders the four-step first-login wizard. A first sign-in
// lands here and the sidebar keeps offering it until it is finished, but
// nothing forces anyone through it and it stays reachable afterwards.
func (s *Server) handleOnboarding(w http.ResponseWriter, r *http.Request) {
	session, ok := requestSessionFrom(r.Context())
	if !ok {
		s.redirectToLogin(w, r)
		return
	}
	s.renderAuthedPage(w, r, http.StatusOK, session, "Getting started", OnboardingPage(OnboardingData{Session: session}))
}

// handleOnboardingComplete stamps the caller's own UserSpace as onboarded and
// sends them to the Overview. It does not check that they actually performed
// any of the three steps — the wizard explains, it does not enforce, so
// pressing "Finish" is always enough.
func (s *Server) handleOnboardingComplete(w http.ResponseWriter, r *http.Request) {
	session, api, ok := s.sessionAPI(w, r)
	if !ok {
		return
	}
	if err := api.PatchUserSpaceOnboardingCompleted(r.Context(), session.Identity.UserSpaceName); err != nil {
		s.writeErrorPage(w, r, err)
		return
	}
	// Refresh the cached session identity so the very next request (the
	// redirect target below) is not immediately bounced back here — the
	// identity cache is only otherwise populated at login.
	s.loginFlow.MarkOnboardingCompleted(session.SessionID)
	http.Redirect(w, r, s.path("/"), http.StatusSeeOther)
}
