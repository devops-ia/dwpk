package auth

import (
	"context"
	"errors"
	"testing"

	dwpkv1alpha1 "github.com/devops-ia/dwpk/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestOwnerResolverResolveByEmailReturnsMatchingUserSpace(t *testing.T) {
	t.Parallel()

	resolver := newTestOwnerResolver(t,
		newTestUserSpace("alice", "alice@example.com"),
		newTestUserSpace("bob", "bob@example.com"),
	)

	userSpace, err := resolver.ResolveByEmail(context.Background(), "alice@example.com")
	if err != nil {
		t.Fatalf("ResolveByEmail() error = %v", err)
	}
	if userSpace == nil {
		t.Fatal("ResolveByEmail() returned nil UserSpace")
	}
	if userSpace.Name != "alice" {
		t.Fatalf("ResolveByEmail() name = %q, want %q", userSpace.Name, "alice")
	}
	if userSpace.Spec.Owner != "alice@example.com" {
		t.Fatalf("ResolveByEmail() owner = %q, want %q", userSpace.Spec.Owner, "alice@example.com")
	}
}

func TestOwnerResolverResolveByEmailReturnsNotFoundWhenMissing(t *testing.T) {
	t.Parallel()

	resolver := newTestOwnerResolver(t, newTestUserSpace("alice", "alice@example.com"))

	userSpace, err := resolver.ResolveByEmail(context.Background(), "carol@example.com")
	if !errors.Is(err, ErrNoUserSpace) {
		t.Fatalf("ResolveByEmail() error = %v, want ErrNoUserSpace", err)
	}
	if userSpace != nil {
		t.Fatalf("ResolveByEmail() returned %v, want nil", userSpace)
	}
}

func TestOwnerResolverResolveByEmailReturnsDuplicateError(t *testing.T) {
	t.Parallel()

	resolver := newTestOwnerResolver(t,
		newTestUserSpace("alice-one", "alice@example.com"),
		newTestUserSpace("alice-two", "alice@example.com"),
	)

	userSpace, err := resolver.ResolveByEmail(context.Background(), "alice@example.com")
	if !errors.Is(err, ErrMultipleUserSpaces) {
		t.Fatalf("ResolveByEmail() error = %v, want ErrMultipleUserSpaces", err)
	}
	if userSpace != nil {
		t.Fatalf("ResolveByEmail() returned %v, want nil", userSpace)
	}
}

func newTestOwnerResolver(t *testing.T, userSpaces ...*dwpkv1alpha1.UserSpace) *OwnerResolver {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := dwpkv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme() error = %v", err)
	}

	objects := make([]runtime.Object, 0, len(userSpaces))
	for _, userSpace := range userSpaces {
		objects = append(objects, userSpace)
	}

	client := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(objects...).Build()
	return NewOwnerResolver(client)
}

func newTestUserSpace(name, owner string) *dwpkv1alpha1.UserSpace {
	return &dwpkv1alpha1.UserSpace{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       dwpkv1alpha1.UserSpaceSpec{Owner: owner},
	}
}
