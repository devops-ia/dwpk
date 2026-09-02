package bootstrap

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	dwpkv1alpha1 "github.com/devops-ia/dwpk/api/v1alpha1"
	"github.com/devops-ia/dwpk/internal/auth"
)

const (
	testNamespace      = "dwpk-system"
	testAdminName      = "admin"
	testAdminNamespace = "dwpk-admin"
)

func newAccountTestClient(t *testing.T) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 to scheme: %v", err)
	}
	if err := dwpkv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add dwpk types to scheme: %v", err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).Build()
}

func defaultOptions() AdminAccountOptions {
	return AdminAccountOptions{
		Namespace:     testNamespace,
		UserSpaceName: testAdminName,
		Username:      testAdminName,
		Email:         "admin@dwpk.local",
	}
}

func TestAdminAccountCreatesUserSpaceUserAndPassword(t *testing.T) {
	kubeClient := newAccountTestClient(t)
	ctx := context.Background()

	result, err := AdminAccount(ctx, kubeClient, defaultOptions())
	if err != nil {
		t.Fatalf("AdminAccount() error = %v", err)
	}
	if !result.Created || !result.PasswordGenerated {
		t.Fatalf("result = %+v, want created and generated", result)
	}

	var userSpace dwpkv1alpha1.UserSpace
	if err := kubeClient.Get(ctx, client.ObjectKey{Name: testAdminName}, &userSpace); err != nil {
		t.Fatalf("get admin UserSpace: %v", err)
	}
	if userSpace.Spec.Owner != "admin@dwpk.local" {
		t.Fatalf("owner = %q", userSpace.Spec.Owner)
	}
	// The administrator gets a usable quota. It was once zero as a security
	// control, because the session identity doubled as the workspace pod's
	// ServiceAccount; that is no longer true, and the safety property now lives
	// in the identity split rather than in the quota.
	if userSpace.Spec.Quota.Workspaces < 1 {
		t.Fatalf("quota.workspaces = %d, want the admin to be able to hold a workspace",
			userSpace.Spec.Quota.Workspaces)
	}

	var secret corev1.Secret
	key := client.ObjectKey{Namespace: testNamespace, Name: BootstrapSecretName}
	if err := kubeClient.Get(ctx, key, &secret); err != nil {
		t.Fatalf("get initial password secret: %v", err)
	}
	password := string(secret.Data["password"])
	if password == "" {
		t.Fatal("initial password secret is empty")
	}

	// The stored credential must actually authenticate the printed password.
	users := auth.NewLocalUserStore(kubeClient, testNamespace)
	user, err := users.Verify(ctx, testAdminName, password)
	if err != nil {
		t.Fatalf("Verify() with the generated password error = %v", err)
	}
	if user.Owner != userSpace.Spec.Owner {
		t.Fatalf("local user owner = %q, UserSpace owner = %q; login resolves on this match",
			user.Owner, userSpace.Spec.Owner)
	}
}

// A helm upgrade re-runs the hook. It must never rotate a working password.
func TestAdminAccountIsIdempotent(t *testing.T) {
	kubeClient := newAccountTestClient(t)
	ctx := context.Background()

	if _, err := AdminAccount(ctx, kubeClient, defaultOptions()); err != nil {
		t.Fatalf("first AdminAccount() error = %v", err)
	}
	var first corev1.Secret
	key := client.ObjectKey{Namespace: testNamespace, Name: BootstrapSecretName}
	if err := kubeClient.Get(ctx, key, &first); err != nil {
		t.Fatalf("get initial password secret: %v", err)
	}

	result, err := AdminAccount(ctx, kubeClient, defaultOptions())
	if err != nil {
		t.Fatalf("second AdminAccount() error = %v", err)
	}
	if result.Created {
		t.Fatal("second run reported created = true")
	}

	var second corev1.Secret
	if err := kubeClient.Get(ctx, key, &second); err != nil {
		t.Fatalf("get initial password secret after second run: %v", err)
	}
	if string(first.Data["password"]) != string(second.Data["password"]) {
		t.Fatal("second run rotated the admin password")
	}

	users := auth.NewLocalUserStore(kubeClient, testNamespace)
	all, err := users.List(ctx)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("local users = %d, want 1", len(all))
	}
}

// A configured password is the operator's to hold; writing it back into a
// Secret would spread a credential they already have.
func TestAdminAccountWithConfiguredPasswordWritesNoSecret(t *testing.T) {
	kubeClient := newAccountTestClient(t)
	ctx := context.Background()

	opts := defaultOptions()
	opts.Password = "chosen-by-the-operator"

	result, err := AdminAccount(ctx, kubeClient, opts)
	if err != nil {
		t.Fatalf("AdminAccount() error = %v", err)
	}
	if !result.Created || result.PasswordGenerated {
		t.Fatalf("result = %+v, want created without generation", result)
	}

	var secret corev1.Secret
	key := client.ObjectKey{Namespace: testNamespace, Name: BootstrapSecretName}
	if err := kubeClient.Get(ctx, key, &secret); err == nil {
		t.Fatal("wrote an initial password secret for a configured password")
	}

	users := auth.NewLocalUserStore(kubeClient, testNamespace)
	if _, err := users.Verify(ctx, testAdminName, "chosen-by-the-operator"); err != nil {
		t.Fatalf("Verify() with the configured password error = %v", err)
	}
}

func TestAdminAccountValidatesOptions(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*AdminAccountOptions)
	}{
		{name: "no namespace", mutate: func(o *AdminAccountOptions) { o.Namespace = "" }},
		{name: "no userspace name", mutate: func(o *AdminAccountOptions) { o.UserSpaceName = "" }},
		{name: "no username", mutate: func(o *AdminAccountOptions) { o.Username = "" }},
		{name: "no email", mutate: func(o *AdminAccountOptions) { o.Email = "" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			kubeClient := newAccountTestClient(t)
			opts := defaultOptions()
			test.mutate(&opts)
			if _, err := AdminAccount(context.Background(), kubeClient, opts); err == nil {
				t.Fatal("AdminAccount() accepted invalid options")
			}
		})
	}
}
