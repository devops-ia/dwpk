package ui

import (
	"net/http"
	"strings"
)

func (s *Server) handleAdminCreateLocalUser(w http.ResponseWriter, r *http.Request) {
	session, ok := s.requireLocalUserAdmin(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		s.writeErrorPage(w, r, errInvalidForm)
		return
	}
	_, err := s.localUsers.Create(r.Context(),
		strings.TrimSpace(r.Form.Get("username")),
		r.Form.Get("password"),
		strings.TrimSpace(r.Form.Get("owner")),
	)
	if err != nil {
		// A duplicate username or a weak password is the operator's mistake to
		// fix on the same screen, not a reason to lose the page they were on.
		s.renderPeople(w, r, session, err.Error())
		return
	}
	http.Redirect(w, r, s.path("/admin/users"), http.StatusFound)
}

func (s *Server) handleAdminDeleteLocalUser(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireLocalUserAdmin(w, r); !ok {
		return
	}
	if err := s.localUsers.Delete(r.Context(), r.PathValue("name")); err != nil {
		s.writeErrorPage(w, r, err)
		return
	}
	http.Redirect(w, r, s.path("/admin/users"), http.StatusFound)
}

// requireLocalUserAdmin is requireUserSpaceAdmin plus the check that local
// auth is switched on at all: without a store there is nothing to write to.
func (s *Server) requireLocalUserAdmin(w http.ResponseWriter, r *http.Request) (RequestSession, bool) {
	if s.localUsers == nil {
		s.writeErrorPage(w, r, errLocalUsersDisabled)
		return RequestSession{}, false
	}
	return s.requireUserSpaceAdmin(w, r)
}
