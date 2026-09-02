/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package webhook

import (
	"context"
	"fmt"

	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	dwpkv1alpha1 "github.com/devops-ia/dwpk/api/v1alpha1"
)

// CREATE and UPDATE: a UserSpace can be promoted after the fact, so the rule
// has to hold on both.
// +kubebuilder:webhook:path=/validate-dwpk-devops-ia-io-v1alpha1-userspace,mutating=false,failurePolicy=fail,matchPolicy=Equivalent,sideEffects=None,timeoutSeconds=5,groups=dwpk.devops-ia.io,resources=userspaces,verbs=create;update,versions=v1alpha1,name=vuserspace-v1alpha1.dwpk.devops-ia.io,admissionReviewVersions=v1
// +kubebuilder:rbac:groups=authorization.k8s.io,resources=subjectaccessreviews,verbs=create

// UserSpaceValidator stops the administrator role being handed out by anyone
// who is not already an administrator.
//
// Without this the role field is a privilege escalation: setting
// role: administrator makes the controller bind cluster-wide rights, so anyone
// able to edit a UserSpace - their own included - could promote themselves.
// This is the check that makes the role safe to expose in the API at all.
type UserSpaceValidator struct {
	client client.Client
}

var userSpaceGR = dwpkv1alpha1.SchemeGroupVersion.WithResource("userspaces").GroupResource()

func (v *UserSpaceValidator) ValidateCreate(
	ctx context.Context,
	us *dwpkv1alpha1.UserSpace,
) (admission.Warnings, error) {
	if err := v.validateRoleGrant(ctx, us, ""); err != nil {
		return nil, err
	}
	if err := validateUserSpaceKeys(us); err != nil {
		return nil, err
	}
	return nil, v.validateUniqueLogin(ctx, us)
}

func (v *UserSpaceValidator) ValidateUpdate(
	ctx context.Context,
	old, us *dwpkv1alpha1.UserSpace,
) (admission.Warnings, error) {
	previous := ""
	if old != nil {
		previous = old.Spec.EffectiveRole()
	}
	if err := v.validateRoleGrant(ctx, us, previous); err != nil {
		return nil, err
	}
	if err := validateNamespaceImmutable(old, us); err != nil {
		return nil, err
	}
	if err := v.validateSelfEdit(ctx, old, us); err != nil {
		return nil, err
	}
	if err := validateUserSpaceKeys(us); err != nil {
		return nil, err
	}
	return nil, v.validateUniqueLogin(ctx, us)
}

// validateNamespaceImmutable rejects any change to spec.namespace once a
// namespace has actually been provisioned.
//
// The CRD's own CEL rule ("self == oldSelf") only fires once spec.namespace
// has been explicitly set: a UserSpace left at its default (empty, meaning
// "dwpk-"+name) can transition unset -> set without tripping it, because CEL
// treats that as assigning an optional field for the first time rather than
// changing it. status.namespace is what the controller actually provisioned
// and cannot be blank once Ready, so it is what "already has a namespace"
// really means here - checking it (not spec.namespace) is what closes the
// unset -> set gap the CEL rule leaves open, for every requester, admins
// included: a rebind provisions a second namespace and abandons the person's
// existing workspaces and home volumes in the first one.
func validateNamespaceImmutable(old, us *dwpkv1alpha1.UserSpace) error {
	if old == nil || old.Status.Namespace == "" {
		return nil
	}
	if us.Spec.Namespace == old.Spec.Namespace {
		return nil
	}
	return apierrors.NewForbidden(userSpaceGR, us.Name, fmt.Errorf(
		"spec.namespace is immutable once %q has been provisioned", old.Status.Namespace))
}

// ValidateDelete is not registered for the delete verb; it exists to satisfy
// the interface.
func (v *UserSpaceValidator) ValidateDelete(
	_ context.Context,
	_ *dwpkv1alpha1.UserSpace,
) (admission.Warnings, error) {
	return nil, nil
}

// validateSelfEdit narrows what a non-administrator may change on a UserSpace.
//
// People can patch their own UserSpace so they can save SSH keys from My
// profile and dismiss the first-login wizard. RBAC scopes that to their own
// object but cannot scope it to a field, and everything else on the spec is
// something they should not be setting for themselves: the quota they are held
// to, the projects they belong to, the namespace their work lives in. The role
// is separately guarded, but the quota was not - patch without this is "raise
// your own limits".
//
// The two permitted fields are the ones that describe the person rather than
// their entitlements. A tutorial someone has already read is not a privilege.
//
// Whole-spec comparison with the permitted fields zeroed, so a field added to
// the CRD later is refused by default rather than silently becoming
// self-editable.
func (v *UserSpaceValidator) validateSelfEdit(ctx context.Context, old, updated *dwpkv1alpha1.UserSpace) error {
	if old == nil {
		return nil
	}

	req, err := admission.RequestFromContext(ctx)
	if err != nil {
		return fmt.Errorf("read admission request: %w", err)
	}
	admin, err := v.requesterIsAdministrator(ctx, req.UserInfo)
	if err != nil {
		return err
	}
	if admin {
		return nil
	}

	oldSpec := old.Spec.DeepCopy()
	newSpec := updated.Spec.DeepCopy()
	oldSpec.SSHAuthorizedKeys = nil
	newSpec.SSHAuthorizedKeys = nil
	oldSpec.OnboardingCompletedAt = nil
	newSpec.OnboardingCompletedAt = nil

	if !equality.Semantic.DeepEqual(oldSpec, newSpec) {
		return apierrors.NewForbidden(userSpaceGR, updated.Name, fmt.Errorf(
			"only spec.sshAuthorizedKeys and spec.onboardingCompletedAt may be changed "+
				"on your own UserSpace; quota, projects, role and namespace are an "+
				"administrator's to set"))
	}
	return nil
}

// validateUserSpaceKeys rejects a default key that would never work.
func validateUserSpaceKeys(us *dwpkv1alpha1.UserSpace) error {
	errs := validateAuthorizedKeys(
		field.NewPath("spec", "sshAuthorizedKeys"), us.Spec.SSHAuthorizedKeys)
	if len(errs) == 0 {
		return nil
	}
	return apierrors.NewInvalid(dwpkv1alpha1.SchemeGroupVersion.WithKind("UserSpace").GroupKind(), us.Name, errs)
}

// validateUniqueLogin refuses two UserSpaces that answer to the same login.
//
// spec.username defaults to metadata.name, which the API server already keeps
// unique - so this only has work to do once someone sets an explicit username.
// It cannot be a CEL rule: uniqueness is a statement about every other object,
// and CEL on a CRD can only see the one being admitted. That cross-object read
// is exactly what SPEC §7.2 says justifies a webhook.
//
// There is an unavoidable race: two creates admitted concurrently can both pass.
// A UserSpace is created by an administrator, one at a time, so the window is
// theoretical; closing it properly would need a uniqueness key the API server
// enforces, which CRDs do not offer.
func (v *UserSpaceValidator) validateUniqueLogin(ctx context.Context, us *dwpkv1alpha1.UserSpace) error {
	login := us.LoginName()

	var all dwpkv1alpha1.UserSpaceList
	if err := v.client.List(ctx, &all); err != nil {
		return fmt.Errorf("list UserSpaces to check the login is free: %w", err)
	}
	for i := range all.Items {
		other := &all.Items[i]
		if other.Name == us.Name {
			continue
		}
		if other.LoginName() == login {
			return apierrors.NewForbidden(userSpaceGR, us.Name, fmt.Errorf(
				"username %q is already used by UserSpace %q", login, other.Name))
		}
	}
	return nil
}

// validateRoleGrant refuses to introduce the administrator role unless the
// requester already holds it.
//
// An object that was already an administrator passes unchanged: this guards the
// transition, not the state, so an administrator editing their own quota is not
// blocked by their own role.
func (v *UserSpaceValidator) validateRoleGrant(ctx context.Context, us *dwpkv1alpha1.UserSpace, previousRole string) error {
	if us.Spec.EffectiveRole() != dwpkv1alpha1.UserSpaceRoleAdmin {
		return nil
	}
	if previousRole == dwpkv1alpha1.UserSpaceRoleAdmin {
		return nil
	}

	req, err := admission.RequestFromContext(ctx)
	if err != nil {
		return fmt.Errorf("read admission request: %w", err)
	}

	allowed, err := v.requesterIsAdministrator(ctx, req.UserInfo)
	if err != nil {
		return err
	}
	if !allowed {
		return apierrors.NewForbidden(userSpaceGR, us.Name, fmt.Errorf(
			"only an administrator may grant the %q role", dwpkv1alpha1.UserSpaceRoleAdmin))
	}
	return nil
}

// requesterIsAdministrator asks the API server whether the requester could
// already do what an administrator does, rather than keeping a list of who is
// one. A SubjectAccessReview is the only way to ask about someone else's
// rights: SelfSubjectAccessReview would answer for the webhook itself.
func (v *UserSpaceValidator) requesterIsAdministrator(ctx context.Context, user authenticationv1.UserInfo) (bool, error) {
	// Deleting a UserSpace is the narrowest thing only an administrator can do,
	// and it is what the admin ClusterRoles grant. Checking a capability rather
	// than a role name keeps this working whatever the roles are called.
	review := &authorizationv1.SubjectAccessReview{
		Spec: authorizationv1.SubjectAccessReviewSpec{
			User:   user.Username,
			Groups: user.Groups,
			UID:    user.UID,
			ResourceAttributes: &authorizationv1.ResourceAttributes{
				Group:    dwpkv1alpha1.GroupVersion.Group,
				Resource: "userspaces",
				Verb:     "delete",
			},
		},
	}
	if err := v.client.Create(ctx, review); err != nil {
		return false, fmt.Errorf("check requester privileges: %w", err)
	}
	return review.Status.Allowed, nil
}

// SetupUserSpaceWebhookWithManager registers the UserSpace validating webhook.
func SetupUserSpaceWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &dwpkv1alpha1.UserSpace{}).
		WithValidator(&UserSpaceValidator{client: mgr.GetClient()}).
		Complete()
}
