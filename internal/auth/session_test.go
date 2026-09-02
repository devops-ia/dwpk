package auth

import (
	"sync"
	"testing"
	"time"
)

func TestSessionStoreCreateAndGet(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	store := NewSessionStore(15 * time.Minute)
	store.now = func() time.Time { return now }

	sessionID, err := store.Create(Claims{Email: "user@example.com"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	session, ok := store.Get(sessionID)
	if !ok {
		t.Fatal("Get() ok = false, want true")
	}

	if session.Claims.Email != "user@example.com" {
		t.Fatalf("Get() claims email = %q, want %q", session.Claims.Email, "user@example.com")
	}
	if session.CreatedAt != now {
		t.Fatalf("Get() created at = %v, want %v", session.CreatedAt, now)
	}
	if session.ExpiresAt != now.Add(15*time.Minute) {
		t.Fatalf("Get() expires at = %v, want %v", session.ExpiresAt, now.Add(15*time.Minute))
	}
	if session.Token != "" {
		t.Fatalf("Get() token = %q, want empty", session.Token)
	}
	if !session.TokenExpiresAt.IsZero() {
		t.Fatalf("Get() token expiry = %v, want zero", session.TokenExpiresAt)
	}
}

func TestSessionStoreDeleteRemovesSession(t *testing.T) {
	t.Parallel()

	store := NewSessionStore(time.Minute)
	sessionID, err := store.Create(Claims{Email: "user@example.com"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	store.Delete(sessionID)

	if _, ok := store.Get(sessionID); ok {
		t.Fatal("Get() ok = true after Delete(), want false")
	}
}

func TestSessionStoreExpiredSessionIsAbsent(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	store := NewSessionStore(15 * time.Minute)
	store.now = func() time.Time { return now }

	sessionID, err := store.Create(Claims{Email: "user@example.com"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	expiry := now.Add(5 * time.Minute)
	if err := store.Refresh(sessionID, "minted-token", expiry); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	store.now = func() time.Time { return now.Add(15*time.Minute + time.Second) }

	if _, ok := store.Get(sessionID); ok {
		t.Fatal("Get() ok = true for expired session, want false")
	}
	if _, ok := store.Get(sessionID); ok {
		t.Fatal("Get() ok = true after lazy removal, want false")
	}
}

func TestSessionStoreConcurrentAccess(t *testing.T) {
	t.Parallel()

	store := NewSessionStore(time.Minute)

	const workers = 32

	var wg sync.WaitGroup
	errs := make(chan error, workers)

	for i := range workers {
		wg.Add(1)

		go func(i int) {
			defer wg.Done()

			sessionID, err := store.Create(Claims{Email: "user@example.com"})
			if err != nil {
				errs <- err
				return
			}

			if err := store.Refresh(sessionID, "minted-token", time.Now().Add(time.Minute)); err != nil {
				errs <- err
				return
			}

			session, ok := store.Get(sessionID)
			if !ok {
				errs <- errSessionNotFound
				return
			}
			if session.Claims.Email != "user@example.com" {
				errs <- errMissingEmail
				return
			}

			store.Delete(sessionID)
		}(i)
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		t.Fatalf("concurrent access error = %v", err)
	}
}

func TestSessionStoreRememberedSessionOutlivesItsToken(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	store := NewSessionStore(15 * time.Minute)
	store.now = func() time.Time { return now }

	sessionID, err := store.CreateWithTTL(Claims{Email: "user@example.com"}, 7*24*time.Hour)
	if err != nil {
		t.Fatalf("CreateWithTTL() error = %v", err)
	}
	if err := store.Refresh(sessionID, "minted-token", now.Add(time.Hour)); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	store.now = func() time.Time { return now.Add(6 * 24 * time.Hour) }
	if _, ok := store.Get(sessionID); !ok {
		t.Fatal("Get() ok = false a day before the remembered session ends, want true")
	}

	store.now = func() time.Time { return now.Add(7*24*time.Hour + time.Second) }
	if _, ok := store.Get(sessionID); ok {
		t.Fatal("Get() ok = true past the remembered session's end, want false")
	}
}
