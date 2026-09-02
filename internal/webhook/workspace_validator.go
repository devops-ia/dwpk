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
	"reflect"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	dwpkv1alpha1 "github.com/devops-ia/dwpk/api/v1alpha1"
)

// CREATE and UPDATE: a user can retarget imageRef or size on an existing
// Workspace, and both rules have to hold afterwards.
// +kubebuilder:webhook:path=/validate-dwpk-devops-ia-io-v1alpha1-workspace,mutating=false,failurePolicy=fail,matchPolicy=Equivalent,sideEffects=None,timeoutSeconds=5,groups=dwpk.devops-ia.io,resources=workspaces,verbs=create;update,versions=v1alpha1,name=vworkspace-v1alpha1.dwpk.devops-ia.io,admissionReviewVersions=v1

// WorkspaceValidator holds the two rules that need a second object. It is
// deliberately thin: immutability and intra-object rules are CEL on the CRD,
// where they cost nothing to run and cannot take the cluster down (§7.4).
type WorkspaceValidator struct {
	client client.Client
}

var (
	workspaceGK = dwpkv1alpha1.SchemeGroupVersion.WithKind("Workspace").GroupKind()
	workspaceGR = dwpkv1alpha1.SchemeGroupVersion.WithResource("workspaces").GroupResource()
)

// ValidateCreate checks everything that needs a second object.
func (v *WorkspaceValidator) ValidateCreate(
	ctx context.Context,
	ws *dwpkv1alpha1.Workspace,
) (admission.Warnings, error) {
	if err := v.catalogEntry(ctx, ws, true); err != nil {
		return nil, err
	}
	if err := validateWorkspaceKeys(ws); err != nil {
		return nil, err
	}
	if err := validateStorageMounts(ws); err != nil {
		return nil, err
	}
	if err := validateResources(ws); err != nil {
		return nil, err
	}
	return nil, v.validateQuota(ctx, ws)
}

// ValidateUpdate rechecks everything create checks, quota included.
//
// Quota used to be skipped here, on the reasoning that "the count cannot change
// on an update". That stopped being true when resources became free-form: a
// resize is an update, and skipping the check made it the one way to exceed
// a quota without ever being told.

func (v *WorkspaceValidator) ValidateUpdate(
	ctx context.Context,
	_, ws *dwpkv1alpha1.Workspace,
) (admission.Warnings, error) {
	if err := v.catalogEntry(ctx, ws, false); err != nil {
		return nil, err
	}
	// Keys are rechecked on update, not only on create: an update can introduce
	// a key that never existed at creation, and a workspace whose authorized_keys
	// no longer parse is one nobody can reach.
	if err := validateWorkspaceKeys(ws); err != nil {
		return nil, err
	}
	if err := validateStorageMounts(ws); err != nil {
		return nil, err
	}
	if err := validateResources(ws); err != nil {
		return nil, err
	}
	return nil, v.validateQuota(ctx, ws)
}

// validateWorkspaceKeys parses the keys properly.
//
// The CRD's CEL rule can only check the prefix - it cannot decode base64 or
// read a key blob. A key of the right shape and the wrong content passes CEL
// and then fails at the gateway, which is the worst place to find out.
func validateWorkspaceKeys(ws *dwpkv1alpha1.Workspace) error {
	errs := validateAuthorizedKeys(
		field.NewPath("spec", "sshAuthorizedKeys"), ws.Spec.SSHAuthorizedKeys)
	if len(errs) == 0 {
		return nil
	}
	return apierrors.NewInvalid(workspaceGK, ws.Name, errs)
}

// ValidateDelete is not registered for the delete verb; it exists to satisfy
// the interface.
func (v *WorkspaceValidator) ValidateDelete(
	_ context.Context,
	_ *dwpkv1alpha1.Workspace,
) (admission.Warnings, error) {
	return nil, nil
}

// catalogEntry refuses a Workspace whose referenced entry is not on offer.
//
// Whether an entry may be used is only asked on create. A running workspace
// whose entry has since been disabled or deprecated keeps working - withdrawing
// an entry is a decision about new workspaces, and stopping somebody's running
// machine is a different and much ruder decision.
func (v *WorkspaceValidator) catalogEntry(
	ctx context.Context, ws *dwpkv1alpha1.Workspace, isCreate bool,
) error {
	imagePath := field.NewPath("spec", "imageRef", "name")

	img := &dwpkv1alpha1.WorkspaceImage{}
	if err := v.client.Get(ctx, client.ObjectKey{Name: ws.Spec.ImageRef.Name}, img); err != nil {
		if apierrors.IsNotFound(err) {
			return apierrors.NewInvalid(workspaceGK, ws.Name, field.ErrorList{
				field.NotFound(imagePath, ws.Spec.ImageRef.Name),
			})
		}
		return fmt.Errorf("get WorkspaceImage %q: %w", ws.Spec.ImageRef.Name, err)
	}
	if !isCreate {
		return nil
	}

	if img.IsDeprecated(time.Now()) {
		return apierrors.NewForbidden(workspaceGR, ws.Name, fmt.Errorf(
			"catalog entry %q is deprecated and accepts no new workspaces", img.Name))
	}
	return nil
}

// reservedVolumeName is the home PVC. A workspace naming it would detach the
// home directory from the volume that persists it.
const reservedVolumeName = "home"

// allowedVolumeTypes are the volume kinds that reference an object in the
// user's own namespace, where they already have rights. Everything absent from
// this list reaches outside that namespace one way or another.
var allowedVolumeTypes = []string{
	"configMap", "secret", "persistentVolumeClaim", "emptyDir", "projected", "downwardAPI",
}

// validateStorageMounts narrows what a workspace may mount.
//
// RBAC cannot express this: `create workspaces` in your own namespace is one
// verb, and the volume list inside the object is not something it can see. So
// the narrowing happens here.
//
// hostPath is never permitted, allowRoot or not. allowRoot grants root inside
// the container; hostPath reaches the node's filesystem, and
// /var/lib/kubelet holds every other pod's projected secrets - one hostPath
// mount is every credential on that node, on every tenant sharing the node,
// not just this one's own container. The two are not the same trust decision.
func validateStorageMounts(ws *dwpkv1alpha1.Workspace) error {
	var errs field.ErrorList
	volumesPath := field.NewPath("spec", "volumes")

	for i := range ws.Spec.Volumes {
		volume := &ws.Spec.Volumes[i]
		path := volumesPath.Index(i)

		if volume.Name == reservedVolumeName {
			errs = append(errs, field.Invalid(path.Child("name"), volume.Name,
				"the home volume is reserved for the workspace's own storage"))
			continue
		}
		kind := volumeKind(volume)
		if !slices.Contains(allowedVolumeTypes, kind) {
			errs = append(errs, field.NotSupported(path, kind, allowedVolumeTypes))
		}
	}

	mountsPath := field.NewPath("spec", "volumeMounts")
	for i := range ws.Spec.VolumeMounts {
		if ws.Spec.VolumeMounts[i].Name == reservedVolumeName {
			errs = append(errs, field.Invalid(mountsPath.Index(i).Child("name"),
				reservedVolumeName, "the home mount is reserved for the workspace's own storage"))
		}
	}

	if len(errs) == 0 {
		return nil
	}
	return apierrors.NewInvalid(workspaceGK, ws.Name, errs)
}

// volumeKind names the one source a volume actually sets.
//
// Reflection over the VolumeSource union rather than a switch with twenty arms:
// a Kubernetes upgrade that adds a volume type must not silently become an
// allowed one, and a list of names we check against is the only shape where a
// new arrival fails closed.
func volumeKind(volume *corev1.Volume) string {
	source := reflect.ValueOf(volume.VolumeSource)
	for i := 0; i < source.NumField(); i++ {
		if source.Field(i).IsNil() {
			continue
		}
		name := source.Type().Field(i).Name
		return strings.ToLower(name[:1]) + name[1:]
	}
	return "none"
}

// validateQuota measures what this workspace would add against the owner's
// allowance.
//
// Only running workspaces count, for everything except storage. A stopped
// workspace holds no CPU, no memory and no GPU - but its PVC still exists, so
// storage is counted whether it runs or not. This is also why there is no
// count/workspaces ResourceQuota any more: Kubernetes counts objects, and
// "objects" and "running objects" are different numbers.
//
// A resize is measured as a change, not an addition: the loop skips the object
// being written, so its old values never sit alongside its new ones.
//
// The count is read from the manager's cache, so two simultaneous creates can
// both pass. The namespace ResourceQuota still catches CPU, memory and storage
// at pod admission; the workspace count has no such backstop, which is the
// price of counting running ones.
func (v *WorkspaceValidator) validateQuota(
	ctx context.Context, ws *dwpkv1alpha1.Workspace,
) error {
	us, err := v.userSpaceFor(ctx, ws.Namespace)
	if err != nil || us == nil {
		// No UserSpace claims this namespace, so there is no allowance to
		// enforce. RBAC is what keeps a user out of such a namespace.
		return err
	}

	existing := &dwpkv1alpha1.WorkspaceList{}
	if err := v.client.List(ctx, existing, client.InNamespace(ws.Namespace)); err != nil {
		return fmt.Errorf("list Workspaces in %q: %w", ws.Namespace, err)
	}

	usage := newQuotaUsage()
	for i := range existing.Items {
		other := &existing.Items[i]
		if other.Name == ws.Name {
			// The object being written. Its old values are replaced by the new
			// ones added below, not counted alongside them.
			continue
		}
		usage.add(other, v.gpuResource(ctx))
	}
	usage.add(ws, v.gpuResource(ctx))

	return usage.within(us, ws)
}

// userSpaceFor finds the UserSpace that owns a namespace. It matches on
// status.namespace - the namespace the controller actually reconciled -
// rather than reversing the naming convention, which is the controller's
// business and not the webhook's.
func (v *WorkspaceValidator) userSpaceFor(ctx context.Context, namespace string) (*dwpkv1alpha1.UserSpace, error) {
	list := &dwpkv1alpha1.UserSpaceList{}
	if err := v.client.List(ctx, list); err != nil {
		return nil, fmt.Errorf("list UserSpaces: %w", err)
	}
	for i := range list.Items {
		if list.Items[i].Status.Namespace == namespace {
			return &list.Items[i], nil
		}
	}
	return nil, nil
}
