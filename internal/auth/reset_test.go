package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func newResetStore(t *testing.T, now func() time.Time) *ResetStore {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core types: %v", err)
	}
	store := NewResetStore(fake.NewClientBuilder().WithScheme(scheme).Build(), "dwpk-system")
	if now != nil {
		store.now = now
	}
	return store
}

func TestResetTokenRoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newResetStore(t, nil)

	token, expires, err := store.Issue(ctx, "alice")
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	if token == "" || !expires.After(time.Now()) {
		t.Fatalf("token = %q, expires = %v", token, expires)
	}

	username, err := store.Redeem(ctx, token)
	if err != nil {
		t.Fatalf("Redeem() error = %v", err)
	}
	if username != "alice" {
		t.Fatalf("redeemed for %q, want alice", username)
	}
}

// Single use is the whole point: a link that keeps working is a password that
// anyone who ever saw the message can change.
func TestResetTokenCannotBeUsedTwice(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newResetStore(t, nil)

	token, _, err := store.Issue(ctx, "alice")
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	if _, err := store.Redeem(ctx, token); err != nil {
		t.Fatalf("first Redeem() error = %v", err)
	}
	if _, err := store.Redeem(ctx, token); !errors.Is(err, ErrResetTokenInvalid) {
		t.Fatalf("second Redeem() error = %v, want ErrResetTokenInvalid", err)
	}
}

func TestResetTokenExpires(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	frozen := time.Now()
	store := newResetStore(t, func() time.Time { return frozen })

	token, _, err := store.Issue(ctx, "alice")
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}

	store.now = func() time.Time { return frozen.Add(resetTokenTTL + time.Minute) }
	if _, err := store.Redeem(ctx, token); !errors.Is(err, ErrResetTokenInvalid) {
		t.Fatalf("an expired token was accepted: %v", err)
	}

	// And it is consumed on the way out, so a clock that goes backwards does not
	// bring it back to life.
	store.now = func() time.Time { return frozen }
	if _, err := store.Redeem(ctx, token); !errors.Is(err, ErrResetTokenInvalid) {
		t.Fatalf("an expired token survived being redeemed: %v", err)
	}
}

// Issuing again must invalidate the previous link. Two live links means the
// older one still works after the newer has been used, which is not what
// "reset the password" is taken to mean.
func TestIssuingAgainRevokesTheOldToken(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newResetStore(t, nil)

	first, _, err := store.Issue(ctx, "alice")
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	second, _, err := store.Issue(ctx, "alice")
	if err != nil {
		t.Fatalf("second Issue() error = %v", err)
	}

	if _, err := store.Redeem(ctx, first); !errors.Is(err, ErrResetTokenInvalid) {
		t.Fatalf("the superseded token still works: %v", err)
	}
	if _, err := store.Redeem(ctx, second); err != nil {
		t.Fatalf("the current token does not work: %v", err)
	}
}

// One token must not reset another person's password.
func TestResetTokenIsScopedToItsOwner(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newResetStore(t, nil)

	aliceToken, _, err := store.Issue(ctx, "alice")
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	if _, _, err := store.Issue(ctx, "bob"); err != nil {
		t.Fatalf("Issue() error = %v", err)
	}

	username, err := store.Redeem(ctx, aliceToken)
	if err != nil {
		t.Fatalf("Redeem() error = %v", err)
	}
	if username != "alice" {
		t.Fatalf("alice's token redeemed for %q", username)
	}
}

// Unknown, expired and already-spent tokens are one error. Distinguishing them
// tells whoever is guessing which guesses were close.
func TestUnknownTokenIsRefusedWithoutDetail(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newResetStore(t, nil)

	for _, token := range []string{"", "not-a-token", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"} {
		if _, err := store.Redeem(ctx, token); !errors.Is(err, ErrResetTokenInvalid) {
			t.Fatalf("Redeem(%q) error = %v, want ErrResetTokenInvalid", token, err)
		}
	}
}

// The plaintext must never be recoverable from the cluster.
func TestOnlyTheHashIsStored(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newResetStore(t, nil)

	token, _, err := store.Issue(ctx, "alice")
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}

	var list corev1.SecretList
	if err := store.client.List(ctx, &list); err != nil {
		t.Fatalf("List() error = %v", err)
	}
	for _, secret := range list.Items {
		for key, value := range secret.Data {
			if string(value) == token {
				t.Fatalf("the plaintext token is stored in %s under %q", secret.Name, key)
			}
		}
	}
}

// Expired records are refused whichever way you come at them, so leaving them
// behind is a tidiness problem rather than a security one. Issuing a link is the
// moment the whole list is already in hand, so that is where they go - including
// somebody else's, which no other code path ever revisits.
func TestIssuingClearsExpiredLinksBelongingToOtherPeople(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	frozen := time.Now()
	store := newResetStore(t, func() time.Time { return frozen })
	if _, _, err := store.Issue(ctx, "bob"); err != nil {
		t.Fatalf("Issue() error = %v", err)
	}

	store.now = func() time.Time { return frozen.Add(resetTokenTTL + time.Minute) }
	if _, _, err := store.Issue(ctx, "alice"); err != nil {
		t.Fatalf("Issue() error = %v", err)
	}

	var list corev1.SecretList
	if err := store.client.List(ctx, &list); err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("%d reset records remain, want 1 - bob's expired link was not swept", len(list.Items))
	}
	if got := string(list.Items[0].Data[resetDataUsername]); got != "alice" {
		t.Fatalf("the surviving link belongs to %q, want alice", got)
	}
}
