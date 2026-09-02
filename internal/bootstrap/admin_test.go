package bootstrap

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/devops-ia/dwpk/internal/auth"
	"github.com/devops-ia/dwpk/internal/workspace"
)

func newTestClient(t *testing.T) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 to scheme: %v", err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).Build()
}

func TestAdminTokenCreatesTokenAndInitialSecret(t *testing.T) {
	kubeClient := newTestClient(t)
	ctx := context.Background()

	created, err := AdminToken(ctx, kubeClient, AdminTokenOptions{
		StoreNamespace:        testNamespace,
		SubjectNamespace:      testAdminNamespace,
		SubjectServiceAccount: DefaultAdminServiceAccountName,
	})
	if err != nil {
		t.Fatalf("AdminToken() error = %v", err)
	}
	if !created {
		t.Fatal("AdminToken() created = false on first run, want true")
	}

	var secret corev1.Secret
	if err := kubeClient.Get(ctx, client.ObjectKey{Namespace: "dwpk-system", Name: BootstrapSecretName}, &secret); err != nil {
		t.Fatalf("get initial admin secret: %v", err)
	}
	plaintext := string(secret.Data["token"])
	if plaintext == "" {
		t.Fatal("initial admin secret has empty token")
	}

	store := auth.NewTokenStore(kubeClient, "dwpk-system")
	record, err := store.Lookup(ctx, plaintext)
	if err != nil {
		t.Fatalf("Lookup(plaintext) error = %v", err)
	}
	if record.Kind != auth.TokenKindAdmin {
		t.Fatalf("Lookup(plaintext).Kind = %q, want %q", record.Kind, auth.TokenKindAdmin)
	}
	// The token authenticates as the session ServiceAccount inside the admin's
	// own namespace - the identity a browser login also reaches - not as
	// anything in the release namespace where the record happens to be stored.
	if record.SubjectNamespace != testAdminNamespace || record.SubjectServiceAccount != DefaultAdminServiceAccountName {
		t.Fatalf("Lookup(plaintext) subject = %s/%s, want dwpk-admin/%s",
			record.SubjectNamespace, record.SubjectServiceAccount, DefaultAdminServiceAccountName)
	}
	if record.SubjectServiceAccount == workspace.ServiceAccountName {
		t.Fatal("admin token authenticates as the workspace pod ServiceAccount")
	}
}

func TestAdminTokenIsIdempotent(t *testing.T) {
	kubeClient := newTestClient(t)
	ctx := context.Background()

	if _, err := AdminToken(ctx, kubeClient, AdminTokenOptions{
		StoreNamespace:        testNamespace,
		SubjectNamespace:      testAdminNamespace,
		SubjectServiceAccount: DefaultAdminServiceAccountName,
	}); err != nil {
		t.Fatalf("first AdminToken() error = %v", err)
	}

	created, err := AdminToken(ctx, kubeClient, AdminTokenOptions{
		StoreNamespace:        testNamespace,
		SubjectNamespace:      testAdminNamespace,
		SubjectServiceAccount: DefaultAdminServiceAccountName,
	})
	if err != nil {
		t.Fatalf("second AdminToken() error = %v", err)
	}
	if created {
		t.Fatal("second AdminToken() created = true, want false (already bootstrapped)")
	}

	store := auth.NewTokenStore(kubeClient, "dwpk-system")
	tokens, err := store.List(ctx, auth.TokenKindAdmin, "")
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(tokens) != 1 {
		t.Fatalf("List() returned %d admin tokens after two runs, want 1", len(tokens))
	}
}
