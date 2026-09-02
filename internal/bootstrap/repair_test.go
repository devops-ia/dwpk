package bootstrap

import (
	"context"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client"

	dwpkv1alpha1 "github.com/devops-ia/dwpk/api/v1alpha1"
)

// The login records are not Helm-managed and outlive a `helm uninstall`, while
// the UserSpace does not. An admin whose password still exists but whose
// UserSpace does not cannot log in at all, so the bootstrap has to repair that
// state rather than treating an existing user as proof everything is present.
func TestAdminAccountRecreatesAMissingUserSpace(t *testing.T) {
	kubeClient := newAccountTestClient(t)
	ctx := context.Background()

	if _, err := AdminAccount(ctx, kubeClient, defaultOptions()); err != nil {
		t.Fatalf("first AdminAccount() error = %v", err)
	}

	userSpace := &dwpkv1alpha1.UserSpace{}
	key := client.ObjectKey{Name: testAdminName}
	if err := kubeClient.Get(ctx, key, userSpace); err != nil {
		t.Fatalf("get admin UserSpace: %v", err)
	}
	if err := kubeClient.Delete(ctx, userSpace); err != nil {
		t.Fatalf("delete admin UserSpace: %v", err)
	}

	result, err := AdminAccount(ctx, kubeClient, defaultOptions())
	if err != nil {
		t.Fatalf("second AdminAccount() error = %v", err)
	}
	// The login already existed, so nothing was newly created - but the
	// UserSpace must be back.
	if result.Created {
		t.Fatal("second run re-created the login")
	}
	if err := kubeClient.Get(ctx, key, userSpace); err != nil {
		t.Fatalf("admin UserSpace was not recreated: %v", err)
	}
}
