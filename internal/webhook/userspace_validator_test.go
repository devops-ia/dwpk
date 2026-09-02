package webhook

import (
	"context"
	"testing"

	admissionv1 "k8s.io/api/admission/v1"
	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	dwpkv1alpha1 "github.com/devops-ia/dwpk/api/v1alpha1"
)

// newValidatorWithAuthz builds a validator whose SubjectAccessReview always
// answers `allowed`. The fake client does not run authorization itself, so the
// answer has to be injected.
func newValidatorWithAuthz(t *testing.T, allowed bool, existing ...*dwpkv1alpha1.UserSpace) *UserSpaceValidator {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := dwpkv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add dwpk types: %v", err)
	}
	if err := authorizationv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add authorization types: %v", err)
	}

	objects := make([]client.Object, 0, len(existing))
	for _, us := range existing {
		objects = append(objects, us)
	}

	kubeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithInterceptorFuncs(interceptor.Funcs{
			Create: func(_ context.Context, _ client.WithWatch, obj client.Object, _ ...client.CreateOption) error {
				review, ok := obj.(*authorizationv1.SubjectAccessReview)
				if !ok {
					return nil
				}
				review.Status.Allowed = allowed
				return nil
			},
		}).
		Build()

	return &UserSpaceValidator{client: kubeClient}
}

func adminRequestContext(username string) context.Context {
	return admission.NewContextWithRequest(context.Background(), admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			UserInfo: authenticationv1.UserInfo{Username: username},
		},
	})
}

func userSpaceWithRole(role string) *dwpkv1alpha1.UserSpace {
	return &dwpkv1alpha1.UserSpace{
		ObjectMeta: metav1.ObjectMeta{Name: "alice"},
		Spec: dwpkv1alpha1.UserSpaceSpec{
			Owner:         "alice@example.com",
			Role:          role,
			NetworkPolicy: dwpkv1alpha1.NetworkPolicyIsolated,
		},
	}
}

// The role field grants cluster-wide rights, so it is a privilege escalation
// unless admission refuses it to anyone who is not already an administrator.
func TestUserSpaceValidatorRefusesAdministratorFromNonAdmin(t *testing.T) {
	t.Parallel()

	validator := newValidatorWithAuthz(t, false)
	_, err := validator.ValidateCreate(adminRequestContext("mallory@example.com"), userSpaceWithRole(dwpkv1alpha1.UserSpaceRoleAdmin))

	if err == nil {
		t.Fatal("a non-administrator was allowed to create an administrator")
	}
	if !apierrors.IsForbidden(err) {
		t.Fatalf("error = %v, want Forbidden", err)
	}
}

func TestUserSpaceValidatorAllowsAdministratorFromAdmin(t *testing.T) {
	t.Parallel()

	validator := newValidatorWithAuthz(t, true)
	if _, err := validator.ValidateCreate(adminRequestContext("root@example.com"), userSpaceWithRole(dwpkv1alpha1.UserSpaceRoleAdmin)); err != nil {
		t.Fatalf("an administrator was refused: %v", err)
	}
}

// Only the transition is guarded. An administrator editing their own quota
// must not be blocked by the role they already have.
func TestUserSpaceValidatorAllowsEditingAnExistingAdministrator(t *testing.T) {
	t.Parallel()

	validator := newValidatorWithAuthz(t, false)
	existing := userSpaceWithRole(dwpkv1alpha1.UserSpaceRoleAdmin)
	updated := userSpaceWithRole(dwpkv1alpha1.UserSpaceRoleAdmin)

	if _, err := validator.ValidateUpdate(adminRequestContext("alice@example.com"), existing, updated); err != nil {
		t.Fatalf("editing an existing administrator was refused: %v", err)
	}
}

// Promoting an ordinary user is the transition that matters, and it must be
// refused even though the object already existed.
func TestUserSpaceValidatorRefusesPromotionByNonAdmin(t *testing.T) {
	t.Parallel()

	validator := newValidatorWithAuthz(t, false)
	existing := userSpaceWithRole(dwpkv1alpha1.UserSpaceRoleUser)
	updated := userSpaceWithRole(dwpkv1alpha1.UserSpaceRoleAdmin)

	_, err := validator.ValidateUpdate(adminRequestContext("mallory@example.com"), existing, updated)
	if err == nil {
		t.Fatal("a non-administrator promoted a user to administrator")
	}
	if !apierrors.IsForbidden(err) {
		t.Fatalf("error = %v, want Forbidden", err)
	}
}

// Everything below administrator is unrestricted; the webhook must not get in
// the way of ordinary membership edits.
func TestUserSpaceValidatorIgnoresLesserRoles(t *testing.T) {
	t.Parallel()

	validator := newValidatorWithAuthz(t, false)
	for _, role := range []string{"", dwpkv1alpha1.UserSpaceRoleUser} {
		if _, err := validator.ValidateCreate(adminRequestContext("bob@example.com"), userSpaceWithRole(role)); err != nil {
			t.Fatalf("role %q was refused: %v", role, err)
		}
	}
}

func userSpaceNamed(name, username string) *dwpkv1alpha1.UserSpace {
	return &dwpkv1alpha1.UserSpace{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: dwpkv1alpha1.UserSpaceSpec{
			Owner:         name + "@example.com",
			Username:      username,
			NetworkPolicy: dwpkv1alpha1.NetworkPolicyIsolated,
		},
	}
}

// Two people answering to one login is an authentication bug, not a naming
// preference. metadata.name is already unique, so this only bites once someone
// sets an explicit username - including one that collides with another
// UserSpace's *name*, since an unset username falls back to it.
func TestUserSpaceValidatorRefusesADuplicateLogin(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		existing *dwpkv1alpha1.UserSpace
		created  *dwpkv1alpha1.UserSpace
		wantErr  bool
	}{
		{
			name:     "two explicit usernames collide",
			existing: userSpaceNamed("alice", "shared"),
			created:  userSpaceNamed("bob", "shared"),
			wantErr:  true,
		},
		{
			name:     "an explicit username collides with another object's name",
			existing: userSpaceNamed("alice", ""),
			created:  userSpaceNamed("bob", "alice"),
			wantErr:  true,
		},
		{
			name:     "distinct usernames are fine",
			existing: userSpaceNamed("alice", "a.moreno"),
			created:  userSpaceNamed("bob", "b.ruiz"),
		},
		{
			name:     "no username at all falls back to the name, which is already unique",
			existing: userSpaceNamed("alice", ""),
			created:  userSpaceNamed("bob", ""),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			validator := newValidatorWithAuthz(t, true, test.existing)

			_, err := validator.ValidateCreate(adminRequestContext("root@example.com"), test.created)

			if test.wantErr {
				if err == nil {
					t.Fatal("a duplicate login was admitted")
				}
				if !apierrors.IsForbidden(err) {
					t.Fatalf("error = %v, want Forbidden", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("a distinct login was refused: %v", err)
			}
		})
	}
}

// Editing a UserSpace must not trip over its own username.
func TestUserSpaceValidatorAllowsAnObjectToKeepItsOwnLogin(t *testing.T) {
	t.Parallel()

	existing := userSpaceNamed("alice", "a.moreno")
	validator := newValidatorWithAuthz(t, true, existing)

	updated := userSpaceNamed("alice", "a.moreno")
	updated.Spec.Role = dwpkv1alpha1.UserSpaceRoleUser

	if _, err := validator.ValidateUpdate(adminRequestContext("root@example.com"), existing, updated); err != nil {
		t.Fatalf("a UserSpace was refused its own username: %v", err)
	}
}

// People can patch their own UserSpace so they can save SSH keys. RBAC scopes
// that to their own object but not to a field, so without this check "patch
// your own UserSpace" is "raise your own quota".
func TestNonAdminMayOnlyChangeTheirOwnKeys(t *testing.T) {
	t.Parallel()

	withKeys := func(mutate func(*dwpkv1alpha1.UserSpace)) *dwpkv1alpha1.UserSpace {
		us := userSpaceNamed("alice", "")
		us.Spec.Quota = dwpkv1alpha1.UserSpaceQuota{Workspaces: 2}
		if mutate != nil {
			mutate(us)
		}
		return us
	}

	refused := map[string]func(*dwpkv1alpha1.UserSpace){
		"their own quota":     func(u *dwpkv1alpha1.UserSpace) { u.Spec.Quota.Workspaces = 99 },
		"their own role":      func(u *dwpkv1alpha1.UserSpace) { u.Spec.Role = dwpkv1alpha1.UserSpaceRoleAdmin },
		"their own email":     func(u *dwpkv1alpha1.UserSpace) { u.Spec.Email = "someone.else@example.com" },
		"their own namespace": func(u *dwpkv1alpha1.UserSpace) { u.Spec.Namespace = "kube-system" },
		"their own username":  func(u *dwpkv1alpha1.UserSpace) { u.Spec.Username = "someone-else" },
	}

	for name, mutate := range refused {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			// allowed=false: the requester is not an administrator.
			validator := newValidatorWithAuthz(t, false)

			_, err := validator.ValidateUpdate(
				adminRequestContext("alice@example.com"), withKeys(nil), withKeys(mutate))
			if err == nil {
				t.Fatalf("a non-administrator changed %s", name)
			}
			if !apierrors.IsForbidden(err) {
				t.Fatalf("error = %v, want Forbidden", err)
			}
		})
	}

	t.Run("but their keys are theirs to change", func(t *testing.T) {
		t.Parallel()
		validator := newValidatorWithAuthz(t, false)

		updated := withKeys(func(u *dwpkv1alpha1.UserSpace) {
			u.Spec.SSHAuthorizedKeys = []string{validPublicKeys["ssh-ed25519"]}
		})
		if _, err := validator.ValidateUpdate(
			adminRequestContext("alice@example.com"), withKeys(nil), updated); err != nil {
			t.Fatalf("saving an SSH key was refused: %v", err)
		}
	})

	// Finishing the first-login wizard is a user stamping their own UserSpace.
	// Without this the wizard can never be completed: the patch is refused,
	// the stamp never lands, and every subsequent login shows the tutorial
	// again. It is a note about which tutorial someone has read, not a
	// privilege, so it belongs on the same side of the line as their keys.
	t.Run("and so is finishing their own onboarding", func(t *testing.T) {
		t.Parallel()
		validator := newValidatorWithAuthz(t, false)

		now := metav1.Now()
		updated := withKeys(func(u *dwpkv1alpha1.UserSpace) { u.Spec.OnboardingCompletedAt = &now })
		if _, err := validator.ValidateUpdate(
			adminRequestContext("alice@example.com"), withKeys(nil), updated); err != nil {
			t.Fatalf("completing onboarding was refused: %v", err)
		}
	})

	// An administrator is editing somebody else's UserSpace, which is the whole
	// point of the Users screen.
	t.Run("an administrator may change anything", func(t *testing.T) {
		t.Parallel()
		validator := newValidatorWithAuthz(t, true)

		updated := withKeys(func(u *dwpkv1alpha1.UserSpace) { u.Spec.Quota.Workspaces = 99 })
		if _, err := validator.ValidateUpdate(
			adminRequestContext("root@example.com"), withKeys(nil), updated); err != nil {
			t.Fatalf("an administrator was refused: %v", err)
		}
	})
}

// The CRD's own CEL rule on spec.namespace ("self == oldSelf") only fires
// once the field has already been explicitly set. Most UserSpaces never set
// it at all, so the common unset -> set transition sails straight through
// CEL - checking status.namespace instead is what catches it: the controller
// cannot have provisioned a namespace without setting status, no matter what
// the spec field's own history looked like, and it is what "already has a
// namespace" really means. This must hold for an administrator too, not only
// a non-admin caught by validateSelfEdit: rebinding to a namespace of an
// existing UserSpace abandons that person's workspaces and home volumes in
// the one already provisioned.
func TestUserSpaceValidatorRefusesRebindingAProvisionedNamespace(t *testing.T) {
	t.Parallel()

	validator := newValidatorWithAuthz(t, true)
	existing := userSpaceNamed("alice", "")
	existing.Status.Namespace = "dwpk-alice"
	updated := existing.DeepCopy()
	updated.Spec.Namespace = "dwpk-hijacked"

	_, err := validator.ValidateUpdate(adminRequestContext("root@example.com"), existing, updated)
	if err == nil {
		t.Fatal("an administrator rebound a provisioned UserSpace's namespace")
	}
	if !apierrors.IsForbidden(err) {
		t.Fatalf("error = %v, want Forbidden", err)
	}
}

// Setting spec.namespace before anything has been provisioned is not a
// rebind - it is the one legitimate case the CEL rule already allows, and the
// webhook must not narrow it further.
func TestUserSpaceValidatorAllowsSettingNamespaceBeforeProvisioning(t *testing.T) {
	t.Parallel()

	validator := newValidatorWithAuthz(t, true)
	existing := userSpaceNamed("alice", "")
	updated := existing.DeepCopy()
	updated.Spec.Namespace = "dwpk-custom"

	if _, err := validator.ValidateUpdate(adminRequestContext("root@example.com"), existing, updated); err != nil {
		t.Fatalf("setting namespace before provisioning was refused: %v", err)
	}
}
