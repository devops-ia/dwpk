package ui

import (
	"crypto/subtle"
	"sync"

	"github.com/devops-ia/dwpk/internal/auth"
)

const csrfHeaderName = "X-CSRF-Token"

type CSRFStore struct {
	mu     sync.Mutex
	tokens map[string]string
}

func NewCSRFStore() *CSRFStore {
	return &CSRFStore{tokens: map[string]string{}}
}

func (s *CSRFStore) Ensure(sessionID string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if token, ok := s.tokens[sessionID]; ok {
		return token, nil
	}
	token, err := auth.GenerateSessionID()
	if err != nil {
		return "", err
	}
	s.tokens[sessionID] = token
	return token, nil
}

func (s *CSRFStore) Valid(sessionID, token string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	expected, ok := s.tokens[sessionID]
	if !ok || len(expected) != len(token) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(expected), []byte(token)) == 1
}

func (s *CSRFStore) Delete(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tokens, sessionID)
}
