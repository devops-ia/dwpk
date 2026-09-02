package auth

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"sync"
	"time"
)

const (
	DefaultSessionTTL = 15 * time.Minute
	sessionIDBytes    = 32
)

var (
	errInvalidTokenExpiry = errors.New("invalid token expiry")
	errMissingEmail       = errors.New("missing email")
	errMissingToken       = errors.New("missing token")
	errSessionNotFound    = errors.New("session not found")
)

// Session keeps identity and the current Kubernetes credential together so callers only need one lookup.
type Session struct {
	Claims         Claims
	Token          string
	TokenExpiresAt time.Time
	CreatedAt      time.Time
	ExpiresAt      time.Time
	// TTL is how far each request pushes ExpiresAt forward. It lives on the
	// session rather than the store so a remembered login can outlast an
	// ordinary one without needing a second store.
	TTL time.Duration
}

// SessionStore keeps bearer tokens out of cookies and lets the backend revoke them with one delete.
type SessionStore struct {
	mu       sync.Mutex
	sessions map[string]Session
	ttl      time.Duration
	now      func() time.Time
}

func NewSessionStore(ttl time.Duration) *SessionStore {
	if ttl <= 0 {
		ttl = DefaultSessionTTL
	}

	return &SessionStore{
		sessions: make(map[string]Session),
		ttl:      ttl,
		now:      time.Now,
	}
}

func GenerateSessionID() (string, error) {
	buf := make([]byte, sessionIDBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate session id: %w", err)
	}

	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// Create starts a session with the store's default idle window.
func (s *SessionStore) Create(claims Claims) (string, error) {
	return s.CreateWithTTL(claims, s.ttl)
}

// CreateWithTTL starts a session with an explicit idle window, which is how a
// remembered login gets a longer one than the default.
func (s *SessionStore) CreateWithTTL(claims Claims, ttl time.Duration) (string, error) {
	if claims.Email == "" {
		return "", fmt.Errorf("create session: %w", errMissingEmail)
	}
	if ttl <= 0 {
		ttl = s.ttl
	}

	sessionID, err := GenerateSessionID()
	if err != nil {
		return "", fmt.Errorf("create session: %w", err)
	}

	now := s.now()
	session := Session{
		Claims:    claims,
		CreatedAt: now,
		ExpiresAt: now.Add(ttl),
		TTL:       ttl,
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.sessions[sessionID] = session

	return sessionID, nil
}

func (s *SessionStore) Get(sessionID string) (*Session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok := s.sessions[sessionID]
	if !ok {
		return nil, false
	}

	if session.expiredAt(s.now()) {
		delete(s.sessions, sessionID)
		return nil, false
	}

	copy := session
	return &copy, true
}

func (s *SessionStore) Delete(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.sessions, sessionID)
}

func (s *SessionStore) Refresh(sessionID, token string, tokenExpiresAt time.Time) error {
	if token == "" {
		return fmt.Errorf("refresh session %q: %w", sessionID, errMissingToken)
	}

	now := s.now()
	if tokenExpiresAt.IsZero() || !tokenExpiresAt.After(now) {
		return fmt.Errorf("refresh session %q: %w", sessionID, errInvalidTokenExpiry)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok := s.sessions[sessionID]
	if !ok {
		return fmt.Errorf("refresh session %q: %w", sessionID, errSessionNotFound)
	}

	if session.expiredAt(now) {
		delete(s.sessions, sessionID)
		return fmt.Errorf("refresh session %q: %w", sessionID, errSessionNotFound)
	}

	ttl := session.TTL
	if ttl <= 0 {
		ttl = s.ttl
	}

	session.Token = token
	session.TokenExpiresAt = tokenExpiresAt
	// Deliberately not clamped to the token's expiry. Every authenticated
	// request mints a fresh ServiceAccount token before calling this, so the
	// stored token is never what runs out first. Clamping to it would cap a
	// seven-day remembered session at the token's one hour.
	session.ExpiresAt = now.Add(ttl)
	s.sessions[sessionID] = session

	return nil
}

func (s Session) expiredAt(now time.Time) bool {
	return s.ExpiresAt.IsZero() || !s.ExpiresAt.After(now)
}
