package bootstrap

import (
	"context"
	"errors"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	dwpkv1alpha1 "github.com/devops-ia/dwpk/api/v1alpha1"
	"github.com/devops-ia/dwpk/internal/auth"
)

// InitialAdminPasswordSecretName holds the generated admin password in
// plaintext exactly once, for an operator to read and then delete. It mirrors
// InitialAdminSecretName, which does the same for the API token.
// Deprecated: superseded by BootstrapSecretName, which carries both the
// password and the API token. Kept so an operator upgrading from an older
// install can find and delete what the previous version wrote.
const InitialAdminPasswordSecretName = "dwpk-admin-initial-password"

// AdminAccountOptions describes the login the bootstrap should end up with.
// Password is optional: an empty one is generated.
type AdminAccountOptions struct {
	Namespace     string
	UserSpaceName string
	Username      string
	Email         string
	Password      string
}

// AdminAccountResult reports what a bootstrap run actually did, so the caller
// can log the right thing without re-reading the cluster.
type AdminAccountResult struct {
	Created bool
	// PasswordGenerated is false when the password came from configuration,
	// in which case it is never written to a Secret.
	PasswordGenerated bool
}

// AdminAccount creates the local admin login: a UserSpace to own the identity,
// a local user Secret to authenticate it, and - only when the password was
// generated - a Secret holding that password once.
//
// It is idempotent. An existing local user with the same username is left
// exactly as it is, so a helm upgrade never rotates a working password.
func AdminAccount(ctx context.Context, kubeClient client.Client, opts AdminAccountOptions) (AdminAccountResult, error) {
	if err := opts.validate(); err != nil {
		return AdminAccountResult{}, err
	}

	// The UserSpace is ensured first and unconditionally, because it and the
	// credential can go missing independently. The token and local-user records
	// are not Helm-managed, so they outlive a `helm uninstall` while the
	// UserSpace does not - and an admin whose credential still exists but whose
	// UserSpace does not cannot log in at all. Reconciling only when the
	// credential is absent would leave that state unrepairable.
	if err := ensureAdminUserSpace(ctx, kubeClient, opts); err != nil {
		return AdminAccountResult{}, err
	}

	users := auth.NewLocalUserStore(kubeClient, opts.Namespace)
	existing, err := users.List(ctx)
	if err != nil {
		return AdminAccountResult{}, fmt.Errorf("list local users: %w", err)
	}
	for _, user := range existing {
		if user.Username == opts.Username {
			return AdminAccountResult{Created: false}, nil
		}
	}

	password := opts.Password
	generated := password == ""
	if generated {
		password, err = auth.GeneratePassword()
		if err != nil {
			return AdminAccountResult{}, err
		}
	}

	if _, err := users.Create(ctx, opts.Username, password, opts.Email); err != nil {
		if errors.Is(err, auth.ErrLocalUserExists) {
			// Another replica of the hook won the race. Not an error: the
			// account exists, which is all this function promises.
			return AdminAccountResult{Created: false}, nil
		}
		return AdminAccountResult{}, fmt.Errorf("create admin local user: %w", err)
	}

	if generated {
		if err := writeInitialPassword(ctx, kubeClient, opts, password); err != nil {
			return AdminAccountResult{}, err
		}
	}
	return AdminAccountResult{Created: true, PasswordGenerated: generated}, nil
}

// ensureAdminUserSpace creates the UserSpace the admin login resolves to. A
// login needs one whose spec.owner matches the local user's owner, and whose
// status.namespace the controller has filled in.
//
// The role is administrator, and that field is the single source of this
// account's rights: the controller reconciles the ClusterRoleBindings from it.
// Binding them statically in the chart as well would mean demoting this account
// in the UI left the rights in place - an interface that lies.
//
// The administrator gets an ordinary quota. It used to be zero as a security
// control, because the session identity was also the account a workspace pod
// ran as, so a pod in this namespace would have inherited cluster admin. The
// session ServiceAccount is now separate and never mounted in any pod
// (internal/workspace.SessionServiceAccountName), so an administrator can hold
// a workspace like anyone else.
func ensureAdminUserSpace(ctx context.Context, kubeClient client.Client, opts AdminAccountOptions) error {
	userSpace := &dwpkv1alpha1.UserSpace{
		ObjectMeta: metav1.ObjectMeta{Name: opts.UserSpaceName},
		Spec: dwpkv1alpha1.UserSpaceSpec{
			Owner:         opts.Email,
			Role:          dwpkv1alpha1.UserSpaceRoleAdmin,
			NetworkPolicy: dwpkv1alpha1.NetworkPolicyIsolated,
			Quota: dwpkv1alpha1.UserSpaceQuota{
				CPU:        resource.MustParse("4"),
				Memory:     resource.MustParse("8Gi"),
				Storage:    resource.MustParse("50Gi"),
				Workspaces: 2,
			},
		},
	}
	if err := kubeClient.Create(ctx, userSpace); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create admin UserSpace %q: %w", opts.UserSpaceName, err)
	}
	return nil
}

func writeInitialPassword(ctx context.Context, kubeClient client.Client, opts AdminAccountOptions, password string) error {
	return writeInitialValues(ctx, kubeClient, opts.Namespace, map[string][]byte{
		BootstrapKeyUsername: []byte(opts.Username),
		BootstrapKeyPassword: []byte(password),
	})
}

func (o AdminAccountOptions) validate() error {
	switch {
	case o.Namespace == "":
		return errors.New("bootstrap namespace required")
	case o.UserSpaceName == "":
		return errors.New("admin userspace name required")
	case o.Username == "":
		return auth.ErrEmptyUsername
	case o.Email == "":
		return errors.New("admin email required")
	default:
		return nil
	}
}
