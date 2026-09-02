package ui

import "context"

type requestContextKey string

const sessionContextKey requestContextKey = "dwpk-ui-session"

type RequestSession struct {
	SessionID string
	Identity  SessionIdentity
	Token     string
	CSRFToken string
}

func withRequestSession(ctx context.Context, session RequestSession) context.Context {
	return context.WithValue(ctx, sessionContextKey, session)
}

func requestSessionFrom(ctx context.Context) (RequestSession, bool) {
	session, ok := ctx.Value(sessionContextKey).(RequestSession)
	return session, ok
}
