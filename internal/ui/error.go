package ui

import (
	"errors"
	"fmt"
	"net/http"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

// errInvalidForm is the one reply for a body that will not parse as a form.
var errInvalidForm = apiError{status: http.StatusBadRequest, message: "invalid form"}

type apiError struct {
	status  int
	message string
}

func (e apiError) Error() string {
	return e.message
}

func (s *Server) writeErrorPage(w http.ResponseWriter, r *http.Request, err error) {
	status, message := statusAndMessage(err)
	if session, ok := requestSessionFrom(r.Context()); ok {
		s.renderAuthedPage(w, r, status, session, "Error", ErrorPage(ErrorData{Session: &session, Status: status, Message: message}))
		return
	}
	s.renderAnonymousPage(w, r, status, "Error", ErrorPage(ErrorData{Status: status, Message: message}))
}

func statusAndMessage(err error) (int, string) {
	if err == nil {
		return http.StatusOK, ""
	}
	var appErr apiError
	if errors.As(err, &appErr) {
		return appErr.status, appErr.message
	}
	var statusErr apierrors.APIStatus
	if errors.As(err, &statusErr) {
		status := statusErr.Status()
		return int(status.Code), status.Message
	}
	var denied *UserSpaceAccessDeniedError
	if errors.As(err, &denied) {
		return http.StatusForbidden, denied.Error()
	}
	switch {
	case errors.Is(err, ErrStateMismatch), errors.Is(err, ErrLoginChallengeExpired), errors.Is(err, ErrSessionNotFound):
		return http.StatusUnauthorized, err.Error()
	default:
		return http.StatusInternalServerError, fmt.Sprintf("%v", err)
	}
}
