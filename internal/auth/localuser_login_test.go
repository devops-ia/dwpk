package auth

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// storeWith builds a store holding accounts, bypassing Create so a test can set
// up combinations Create would refuse.
func storeWith(t *testing.T, accounts ...LocalUser) *LocalUserStore {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core types: %v", err)
	}

	objects := make([]runtime.Object, 0, len(accounts))
	for i, account := range accounts {
		hash, err := HashPassword("correct-horse")
		if err != nil {
			t.Fatalf("hash: %v", err)
		}
		objects = append(objects, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      LocalUserSecretName(account.Username),
				Namespace: "dwpk-system",
				Labels:    map[string]string{LocalUserLabel: "true"},
			},
			Data: map[string][]byte{
				localUserDataUsername: []byte(account.Username),
				localUserDataOwner:    []byte(account.Owner),
				localUserDataHash:     []byte(hash),
			},
		})
		_ = i
	}

	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(objects...).Build()
	return NewLocalUserStore(kubeClient, "dwpk-system")
}

// People remember one identifier or the other. Refusing the one they typed
// teaches them nothing, so both work.
func TestVerifyAcceptsUsernameOrEmail(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := storeWith(t, LocalUser{Username: "a.moreno", Owner: "alejandra@example.com"})

	for _, login := range []string{"a.moreno", "alejandra@example.com", "Alejandra@Example.com"} {
		got, err := store.Verify(ctx, login, "correct-horse")
		if err != nil {
			t.Fatalf("Verify(%q) error = %v", login, err)
		}
		if got.Username != "a.moreno" {
			t.Fatalf("Verify(%q) resolved to %q", login, got.Username)
		}
	}

	// The password still has to be right, whichever identifier was used.
	if _, err := store.Verify(ctx, "alejandra@example.com", "wrong"); err == nil {
		t.Fatal("a wrong password was accepted when logging in by email")
	}
}

// A username is the more specific identifier: somebody whose username happens
// to be another person's email must still get their own account.
func TestUsernameWinsOverSomebodyElsesEmail(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := storeWith(t,
		LocalUser{Username: "shared@example.com", Owner: "first@example.com"},
		LocalUser{Username: "second", Owner: "shared@example.com"},
	)

	got, err := store.Verify(ctx, "shared@example.com", "correct-horse")
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if got.Username != "shared@example.com" {
		t.Fatalf("resolved to %q, want the account whose username it is", got.Username)
	}
}

// Two accounts sharing an email is a configuration mistake. Choosing either
// would sign somebody in as a person they are not.
func TestAmbiguousLoginIsRefusedRatherThanGuessed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := storeWith(t,
		LocalUser{Username: "alice", Owner: "shared@example.com"},
		LocalUser{Username: "bob", Owner: "shared@example.com"},
	)

	if _, err := store.Verify(ctx, "shared@example.com", "correct-horse"); !errors.Is(err, ErrLocalUserAmbiguous) {
		t.Fatalf("error = %v, want ErrLocalUserAmbiguous", err)
	}

	// Each is still reachable by their own username.
	for _, username := range []string{"alice", "bob"} {
		if _, err := store.Verify(ctx, username, "correct-horse"); err != nil {
			t.Fatalf("Verify(%q) error = %v", username, err)
		}
	}
}

// Changing a password resolves by username only: it acts on one account, not on
// whoever answers to a string.
func TestPasswordChangeDoesNotAcceptAnEmail(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := storeWith(t, LocalUser{Username: "a.moreno", Owner: "alejandra@example.com"})

	err := store.SetPassword(ctx, "alejandra@example.com", "correct-horse", "new-password")
	if !errors.Is(err, ErrLocalUserNotFound) {
		t.Fatalf("error = %v, want ErrLocalUserNotFound", err)
	}
	if err := store.SetPassword(ctx, "a.moreno", "correct-horse", "new-password"); err != nil {
		t.Fatalf("changing by username was refused: %v", err)
	}
}
