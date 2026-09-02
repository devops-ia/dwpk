// Package bootstrap creates the platform's local initial admin credential
// the first time it starts, so a fresh cluster is usable before any OAuth2
// provider is configured (§7.7).
package bootstrap

import (
	"context"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/devops-ia/dwpk/internal/auth"
	"github.com/devops-ia/dwpk/internal/workspace"
)

// InitialAdminSecretName holds the admin token's plaintext exactly once, for
// an operator to read and delete after first use.
//
// DefaultAdminServiceAccountName is the identity the token authenticates as.
// It is the session ServiceAccount, the same one a browser login mints for, so
// the API token and the UI reach exactly the same authority - and neither is
// the account a workspace pod runs as.
const (
	// Deprecated: the token and the password now share BootstrapSecretName.
	// Kept only so an operator upgrading from an older install can still find
	// and delete the object the previous version left behind.
	InitialAdminSecretName         = "dwpk-admin-token-initial"
	DefaultAdminServiceAccountName = workspace.SessionServiceAccountName
)

// AdminTokenOptions separates where the token record is stored from the
// identity it authenticates as. Those are different namespaces: the records
// live alongside the release, while the identity is the session ServiceAccount
// in the admin's own UserSpace namespace, so an API token and a browser login
// reach exactly the same authority.
type AdminTokenOptions struct {
	// StoreNamespace holds the token records, normally the release namespace.
	StoreNamespace string
	// SubjectNamespace and SubjectServiceAccount are the identity the token
	// mints Kubernetes tokens for.
	SubjectNamespace      string
	SubjectServiceAccount string
}

// AdminToken idempotently issues the platform's one local admin API token.
// If an admin token already exists it does nothing and reports created=false,
// so it is safe to run on every helm upgrade.
func AdminToken(ctx context.Context, kubeClient client.Client, opts AdminTokenOptions) (created bool, err error) {
	store := auth.NewTokenStore(kubeClient, opts.StoreNamespace)

	has, err := store.HasAny(ctx, auth.TokenKindAdmin)
	if err != nil {
		return false, fmt.Errorf("check for existing admin token: %w", err)
	}
	if has {
		return false, nil
	}

	// No expiry: the bootstrap token is how an operator reaches a cluster they
	// have just installed, and one that quietly stopped working would be a
	// lockout with no obvious cause.
	record, err := store.Issue(ctx, auth.TokenGrant{
		Kind:                  auth.TokenKindAdmin,
		SubjectNamespace:      opts.SubjectNamespace,
		SubjectServiceAccount: opts.SubjectServiceAccount,
	})
	if err != nil {
		return false, fmt.Errorf("issue admin token: %w", err)
	}

	err = writeInitialValues(ctx, kubeClient, opts.StoreNamespace, map[string][]byte{
		BootstrapKeyToken: []byte(record.Plaintext),
	})
	if err != nil {
		return false, err
	}

	return true, nil
}
