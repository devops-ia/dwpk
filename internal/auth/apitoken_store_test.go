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

func newTestTokenStore(t *testing.T) *TokenStore {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 to scheme: %v", err)
	}
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	return NewTokenStore(kubeClient, "dwpk-system")
}

func TestTokenStoreIssueAndLookup(t *testing.T) {
	store := newTestTokenStore(t)
	ctx := context.Background()

	record, err := store.Issue(ctx, TokenGrant{Kind: TokenKindApplication, SubjectNamespace: "user-alice", SubjectServiceAccount: "workspace-access"})
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	if record.Plaintext == "" {
		t.Fatal("Issue() returned empty plaintext")
	}
	if record.SecretName == "" {
		t.Fatal("Issue() returned empty secret name")
	}

	found, err := store.Lookup(ctx, record.Plaintext)
	if err != nil {
		t.Fatalf("Lookup() error = %v", err)
	}
	if found.SubjectNamespace != "user-alice" || found.SubjectServiceAccount != "workspace-access" {
		t.Fatalf("Lookup() = %+v, want subject user-alice/workspace-access", found)
	}
	if found.Kind != TokenKindApplication {
		t.Fatalf("Lookup() kind = %q, want %q", found.Kind, TokenKindApplication)
	}
	if found.Plaintext != "" {
		t.Fatalf("Lookup() must never return plaintext, got %q", found.Plaintext)
	}
}

func TestTokenStoreLookupUnknownToken(t *testing.T) {
	store := newTestTokenStore(t)
	ctx := context.Background()

	if _, err := store.Issue(ctx, TokenGrant{Kind: TokenKindApplication, SubjectNamespace: "user-alice", SubjectServiceAccount: "workspace-access"}); err != nil {
		t.Fatalf("Issue() error = %v", err)
	}

	if _, err := store.Lookup(ctx, "dwpk_not-a-real-token"); err != ErrTokenNotFound {
		t.Fatalf("Lookup() error = %v, want ErrTokenNotFound", err)
	}
}

func TestTokenStoreListScopesByKindAndSubject(t *testing.T) {
	store := newTestTokenStore(t)
	ctx := context.Background()

	if _, err := store.Issue(ctx, TokenGrant{Kind: TokenKindAdmin, SubjectNamespace: "dwpk-system", SubjectServiceAccount: "dwpk-admin"}); err != nil {
		t.Fatalf("Issue(admin) error = %v", err)
	}
	if _, err := store.Issue(ctx, TokenGrant{Kind: TokenKindApplication, SubjectNamespace: "user-alice", SubjectServiceAccount: "workspace-access"}); err != nil {
		t.Fatalf("Issue(alice) error = %v", err)
	}
	if _, err := store.Issue(ctx, TokenGrant{Kind: TokenKindApplication, SubjectNamespace: "user-bob", SubjectServiceAccount: "workspace-access"}); err != nil {
		t.Fatalf("Issue(bob) error = %v", err)
	}

	aliceTokens, err := store.List(ctx, TokenKindApplication, "user-alice")
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(aliceTokens) != 1 || aliceTokens[0].SubjectNamespace != "user-alice" {
		t.Fatalf("List(application, user-alice) = %+v, want exactly alice's token", aliceTokens)
	}

	allApplication, err := store.List(ctx, TokenKindApplication, "")
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(allApplication) != 2 {
		t.Fatalf("List(application, \"\") returned %d records, want 2", len(allApplication))
	}
}

func TestTokenStoreHasAny(t *testing.T) {
	store := newTestTokenStore(t)
	ctx := context.Background()

	has, err := store.HasAny(ctx, TokenKindAdmin)
	if err != nil {
		t.Fatalf("HasAny() error = %v", err)
	}
	if has {
		t.Fatal("HasAny() = true before any admin token was issued")
	}

	if _, err := store.Issue(ctx, TokenGrant{Kind: TokenKindAdmin, SubjectNamespace: "dwpk-system", SubjectServiceAccount: "dwpk-admin"}); err != nil {
		t.Fatalf("Issue() error = %v", err)
	}

	has, err = store.HasAny(ctx, TokenKindAdmin)
	if err != nil {
		t.Fatalf("HasAny() error = %v", err)
	}
	if !has {
		t.Fatal("HasAny() = false after issuing an admin token")
	}
}

func TestTokenStoreRevoke(t *testing.T) {
	store := newTestTokenStore(t)
	ctx := context.Background()

	record, err := store.Issue(ctx, TokenGrant{Kind: TokenKindApplication, SubjectNamespace: "user-alice", SubjectServiceAccount: "workspace-access"})
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}

	if err := store.Revoke(ctx, record.SecretName); err != nil {
		t.Fatalf("Revoke() error = %v", err)
	}

	if _, err := store.Lookup(ctx, record.Plaintext); err != ErrTokenNotFound {
		t.Fatalf("Lookup() after Revoke() error = %v, want ErrTokenNotFound", err)
	}

	// Revoking an already-absent secret must not be an error.
	if err := store.Revoke(ctx, record.SecretName); err != nil {
		t.Fatalf("Revoke() of an already-absent secret error = %v, want nil", err)
	}
}

// An expired token must be refused at Lookup, not merely swept up later. A
// cleanup that does not run - a crashed pod, a paused controller - must never
// leave a token working past the date its owner was told it would stop.
func TestExpiredTokenIsRefusedAtLookup(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newTestTokenStore(t)

	expired, err := store.Issue(ctx, TokenGrant{
		Kind:                  TokenKindApplication,
		SubjectNamespace:      "dwpk-alice",
		SubjectServiceAccount: "session",
		ExpiresAt:             time.Now().Add(-time.Minute),
	})
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	if _, err := store.Lookup(ctx, expired.Plaintext); !errors.Is(err, ErrTokenNotFound) {
		t.Fatalf("an expired token was accepted: %v", err)
	}

	live, err := store.Issue(ctx, TokenGrant{
		Kind:                  TokenKindApplication,
		SubjectNamespace:      "dwpk-alice",
		SubjectServiceAccount: "session",
		ExpiresAt:             time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	if _, err := store.Lookup(ctx, live.Plaintext); err != nil {
		t.Fatalf("a live token was refused: %v", err)
	}
}

// A zero expiry means never, and must not be mistaken for "expired in 1970".
func TestTokenWithNoExpiryNeverExpires(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newTestTokenStore(t)

	record, err := store.Issue(ctx, TokenGrant{
		Kind:                  TokenKindApplication,
		SubjectNamespace:      "dwpk-alice",
		SubjectServiceAccount: "session",
	})
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	if record.Expired(time.Now().Add(100 * 365 * 24 * time.Hour)) {
		t.Fatal("a token with no expiry reported itself expired")
	}
	if _, err := store.Lookup(ctx, record.Plaintext); err != nil {
		t.Fatalf("Lookup() refused a token that never expires: %v", err)
	}
}
