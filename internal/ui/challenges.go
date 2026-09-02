package ui

import (
	"sync"
	"time"

	"github.com/devops-ia/dwpk/internal/auth"
)

type storedChallenge struct {
	Provider auth.Name
	Value    LoginChallenge
}

type ChallengeStore struct {
	mu         sync.Mutex
	challenges map[string]storedChallenge
	now        func() time.Time
}

func NewChallengeStore(now func() time.Time) *ChallengeStore {
	if now == nil {
		now = time.Now
	}
	return &ChallengeStore{
		challenges: map[string]storedChallenge{},
		now:        now,
	}
}

func (s *ChallengeStore) Put(provider auth.Name, challenge LoginChallenge) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.challenges[challenge.State] = storedChallenge{Provider: provider, Value: challenge}
}

func (s *ChallengeStore) Take(provider auth.Name, state string) (LoginChallenge, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, ok := s.challenges[state]
	if !ok {
		return LoginChallenge{}, false
	}
	delete(s.challenges, state)
	if stored.Provider != provider || !stored.Value.ExpiresAt.After(s.now()) {
		return LoginChallenge{}, false
	}
	return stored.Value, true
}
