package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"

	dwpkv1alpha1 "github.com/devops-ia/dwpk/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	ErrNoUserSpace        = errors.New("no UserSpace matches owner")
	ErrMultipleUserSpaces = errors.New("multiple UserSpaces match owner")
)

// OwnerResolver maps a verified identity email to the admin-provisioned UserSpace that owns it.
type OwnerResolver struct {
	client client.Client
}

func NewOwnerResolver(kubeClient client.Client) *OwnerResolver {
	return &OwnerResolver{client: kubeClient}
}

func (r *OwnerResolver) ResolveByEmail(ctx context.Context, email string) (*dwpkv1alpha1.UserSpace, error) {
	normalizedEmail := strings.TrimSpace(email)
	if normalizedEmail == "" {
		return nil, errors.New("resolve UserSpace: email must not be empty")
	}

	var userSpaces dwpkv1alpha1.UserSpaceList
	if err := r.client.List(ctx, &userSpaces); err != nil {
		return nil, fmt.Errorf("list UserSpaces for owner %q: %w", normalizedEmail, err)
	}

	matchIndexes := make([]int, 0, 2)
	for i := range userSpaces.Items {
		if userSpaces.Items[i].Spec.Owner == normalizedEmail {
			matchIndexes = append(matchIndexes, i)
		}
	}

	switch len(matchIndexes) {
	case 0:
		return nil, fmt.Errorf("resolve UserSpace for owner %q: %w", normalizedEmail, ErrNoUserSpace)
	case 1:
		return userSpaces.Items[matchIndexes[0]].DeepCopy(), nil
	default:
		matchNames := make([]string, 0, len(matchIndexes))
		for _, i := range matchIndexes {
			matchNames = append(matchNames, userSpaces.Items[i].Name)
		}

		return nil, fmt.Errorf("resolve UserSpace for owner %q: %w: %s", normalizedEmail, ErrMultipleUserSpaces, strings.Join(matchNames, ", "))
	}
}
