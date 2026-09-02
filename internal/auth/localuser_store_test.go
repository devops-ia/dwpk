package auth

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func newTestLocalUserStore(t *testing.T) *LocalUserStore {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 to scheme: %v", err)
	}
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	return NewLocalUserStore(kubeClient, "dwpk-system")
}

func TestLocalUserStoreCreateAndVerify(t *testing.T) {
	store := newTestLocalUserStore(t)
	ctx := context.Background()

	user, err := store.Create(ctx, "alice", "s3cret!", "alice@example.com")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if user.SecretName == "" {
		t.Fatal("Create() returned empty secret name")
	}

	verified, err := store.Verify(ctx, "alice", "s3cret!")
	if err != nil {
		t.Fatalf("Verify() with the correct password error = %v", err)
	}
	if verified.Owner != "alice@example.com" {
		t.Fatalf("Verify().Owner = %q, want alice@example.com", verified.Owner)
	}

	if _, err := store.Verify(ctx, "alice", "wrong-password"); err != ErrInvalidCredentials {
		t.Fatalf("Verify() with the wrong password error = %v, want ErrInvalidCredentials", err)
	}
	if _, err := store.Verify(ctx, "bob", "s3cret!"); err != ErrLocalUserNotFound {
		t.Fatalf("Verify() for an unknown user error = %v, want ErrLocalUserNotFound", err)
	}
}

func TestLocalUserStoreCreateRejectsDuplicateUsername(t *testing.T) {
	store := newTestLocalUserStore(t)
	ctx := context.Background()

	if _, err := store.Create(ctx, "alice", "s3cret!", "alice@example.com"); err != nil {
		t.Fatalf("first Create() error = %v", err)
	}

	if _, err := store.Create(ctx, "alice", "different-password", "alice@example.com"); err == nil {
		t.Fatal("second Create() with the same username succeeded, want ErrLocalUserExists")
	}
}

func TestLocalUserStoreListAndDelete(t *testing.T) {
	store := newTestLocalUserStore(t)
	ctx := context.Background()

	alice, err := store.Create(ctx, "alice", "s3cret!", "alice@example.com")
	if err != nil {
		t.Fatalf("Create(alice) error = %v", err)
	}
	if _, err := store.Create(ctx, "bob", "s3cret!", "bob@example.com"); err != nil {
		t.Fatalf("Create(bob) error = %v", err)
	}

	users, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(users) != 2 {
		t.Fatalf("List() returned %d users, want 2", len(users))
	}

	if err := store.Delete(ctx, alice.SecretName); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	users, err = store.List(ctx)
	if err != nil {
		t.Fatalf("List() after Delete() error = %v", err)
	}
	if len(users) != 1 || users[0].Username != "bob" {
		t.Fatalf("List() after deleting alice = %+v, want only bob", users)
	}

	// Deleting an already-absent user must not be an error.
	if err := store.Delete(ctx, alice.SecretName); err != nil {
		t.Fatalf("Delete() of an already-absent user error = %v, want nil", err)
	}
}

func TestLocalUserStoreSetPassword(t *testing.T) {
	store := newTestLocalUserStore(t)
	ctx := context.Background()

	if _, err := store.Create(ctx, "alice", "old-password", "alice@example.com"); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	if err := store.SetPassword(ctx, "alice", "wrong-password", "new-password"); err == nil {
		t.Fatal("SetPassword() with the wrong current password error = nil, want an error")
	}
	if _, err := store.Verify(ctx, "alice", "old-password"); err != nil {
		t.Fatalf("a rejected change altered the password: %v", err)
	}

	if err := store.SetPassword(ctx, "alice", "old-password", "new-password"); err != nil {
		t.Fatalf("SetPassword() error = %v", err)
	}
	if _, err := store.Verify(ctx, "alice", "new-password"); err != nil {
		t.Fatalf("Verify() with the new password error = %v", err)
	}
	if _, err := store.Verify(ctx, "alice", "old-password"); err == nil {
		t.Fatal("Verify() with the old password error = nil, want an error")
	}

	if err := store.SetPassword(ctx, "nobody", "x", "y"); !errors.Is(err, ErrLocalUserNotFound) {
		t.Fatalf("SetPassword() for a missing user error = %v, want ErrLocalUserNotFound", err)
	}
}
