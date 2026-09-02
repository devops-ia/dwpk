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

package controller

import (
	"context"
	"errors"
	"fmt"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	dwpkv1alpha1 "github.com/devops-ia/dwpk/api/v1alpha1"
	"github.com/devops-ia/dwpk/internal/registry"
)

// ImageRegistryReconciler reconciles an ImageRegistry object.
type ImageRegistryReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// ProviderFactory builds the registry.Provider a sync uses. Nil selects
	// providerFor's own dispatch on spec.provider (the only production path);
	// tests set this to inject a fake and cover the sync/select/apply/prune
	// logic with no AWS credentials and no network.
	ProviderFactory func(ctx context.Context, reg *dwpkv1alpha1.ImageRegistry) (registry.Provider, error)
}

// +kubebuilder:rbac:groups=dwpk.devops-ia.io,resources=imageregistries,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=dwpk.devops-ia.io,resources=imageregistries/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=dwpk.devops-ia.io,resources=imageregistries/finalizers,verbs=update
// +kubebuilder:rbac:groups=dwpk.devops-ia.io,resources=workspaceimages,verbs=get;list;watch;create;update;patch;delete

// Reconcile lists one external registry, applies the sync's include/exclude/
// tag selection, and writes the survivors as WorkspaceImages this
// ImageRegistry owns (§5.4).
//
// On success it requeues after the configured interval - there is nothing to
// watch on a remote registry the way WorkspaceReconciler watches
// WorkspaceImage, so unlike every other reconciler in this repository, a
// timer is the right tool here rather than a duplicate of a watch. On failure
// it returns the error instead and no RequeueAfter, so controller-runtime's
// own rate limiter decides the retry delay rather than the two disagreeing.
func (r *ImageRegistryReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	reg := &dwpkv1alpha1.ImageRegistry{}
	if err := r.Get(ctx, req.NamespacedName, reg); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !reg.DeletionTimestamp.IsZero() {
		// Cluster-scoped owner: garbage collection removes every WorkspaceImage
		// it owns. No finalizer to get stuck on.
		return ctrl.Result{}, nil
	}

	// ctrl.Result{} on every error return, not a Result carrying RequeueAfter:
	// controller-runtime ignores RequeueAfter whenever the error is non-nil and
	// logs a warning about the two disagreeing, since its own rate limiter
	// already decides the retry delay for an error. RequeueAfter is only
	// meaningful on the success path below.

	provider, err := r.providerFor(ctx, reg)
	if err != nil {
		return ctrl.Result{}, errors.Join(err, r.markDegraded(ctx, reg, err))
	}

	remote, err := provider.List(ctx)
	if err != nil {
		return ctrl.Result{}, errors.Join(fmt.Errorf("list registry: %w", err), r.markDegraded(ctx, reg, err))
	}

	sel, err := registry.CompileSelection(
		reg.Spec.Sync.Include, reg.Spec.Sync.Exclude,
		reg.Spec.Sync.Tags.Mode, reg.Spec.Sync.Tags.Patterns, reg.Spec.Sync.Tags.Limit,
	)
	if err != nil {
		return ctrl.Result{}, errors.Join(err, r.markDegraded(ctx, reg, err))
	}
	selected := registry.Select(remote, sel)

	desiredNames := make(map[string]bool, len(selected))
	for _, image := range selected {
		desired := buildSyncedWorkspaceImage(reg, image)
		desiredNames[desired.Name] = true
		if err := applyOwned(ctx, r.Client, r.Scheme, reg, desired); err != nil {
			return ctrl.Result{}, errors.Join(err, r.markDegraded(ctx, reg, err))
		}
	}

	if reg.Spec.Sync.Prune {
		if err := r.prune(ctx, reg, desiredNames); err != nil {
			return ctrl.Result{}, errors.Join(err, r.markDegraded(ctx, reg, err))
		}
	}

	if err := r.markReady(ctx, reg, int32(len(selected))); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: reg.SyncInterval()}, nil
}

// providerFor dispatches on spec.provider. "aws-ecr" is the only value the
// CEL enum on ImageRegistrySpec allows today, so the default case is
// unreachable in production - it exists for the day a second provider's enum
// value ships ahead of its registry.Provider implementation.
func (r *ImageRegistryReconciler) providerFor(ctx context.Context, reg *dwpkv1alpha1.ImageRegistry) (registry.Provider, error) {
	if r.ProviderFactory != nil {
		return r.ProviderFactory(ctx, reg)
	}
	switch reg.Spec.Provider {
	case dwpkv1alpha1.ImageRegistryProviderAWSECR:
		if reg.Spec.AWS == nil {
			return nil, fmt.Errorf("provider %s requires spec.aws", dwpkv1alpha1.ImageRegistryProviderAWSECR)
		}
		return registry.NewECRProvider(ctx, registry.ECRConfig{
			Region:     reg.Spec.AWS.Region,
			RegistryID: reg.Spec.AWS.RegistryID,
			RoleARN:    reg.Spec.AWS.RoleARN,
		})
	default:
		return nil, fmt.Errorf("unsupported provider %q", reg.Spec.Provider)
	}
}

// prune deletes a WorkspaceImage this registry previously created but did not
// produce this time - only called when spec.sync.prune is true (Reconcile's
// caller), since an unreachable registry or a briefly-short list should not
// delete a catalog entry somebody may be using.
func (r *ImageRegistryReconciler) prune(ctx context.Context, reg *dwpkv1alpha1.ImageRegistry, desiredNames map[string]bool) error {
	owned := &dwpkv1alpha1.WorkspaceImageList{}
	if err := r.List(ctx, owned, client.MatchingLabels{dwpkv1alpha1.ImageRegistryLabel: reg.Name}); err != nil {
		return fmt.Errorf("list WorkspaceImages owned by %s: %w", reg.Name, err)
	}
	for i := range owned.Items {
		image := &owned.Items[i]
		if desiredNames[image.Name] {
			continue
		}
		if err := client.IgnoreNotFound(r.Delete(ctx, image)); err != nil {
			return fmt.Errorf("delete WorkspaceImage %s: %w", image.Name, err)
		}
	}
	return nil
}

// patchStatus captures the merge-patch base before any mutation, then applies
// the caller's changes and stamps observedGeneration and the given
// conditions. The base has to be captured first: computing it after the
// caller's fields are already set would diff the mutated object against
// itself and produce a patch with those fields silently missing.
func (r *ImageRegistryReconciler) patchStatus(
	ctx context.Context,
	reg *dwpkv1alpha1.ImageRegistry,
	mutate func(*dwpkv1alpha1.ImageRegistry),
	conditions ...metav1.Condition,
) error {
	patch := client.MergeFrom(reg.DeepCopy())
	mutate(reg)
	reg.Status.ObservedGeneration = reg.Generation
	for _, c := range conditions {
		c.ObservedGeneration = reg.Generation
		apimeta.SetStatusCondition(&reg.Status.Conditions, c)
	}
	if err := r.Status().Patch(ctx, reg, patch); err != nil {
		return fmt.Errorf("patch ImageRegistry status %s: %w", reg.Name, err)
	}
	return nil
}

func (r *ImageRegistryReconciler) markReady(ctx context.Context, reg *dwpkv1alpha1.ImageRegistry, images int32) error {
	return r.patchStatus(ctx, reg, func(reg *dwpkv1alpha1.ImageRegistry) {
		reg.Status.Images = images
		now := metav1.Now()
		reg.Status.LastSyncTime = &now
	}, metav1.Condition{
		Type:    conditionReady,
		Status:  metav1.ConditionTrue,
		Reason:  "Synced",
		Message: "registry synced",
	}, metav1.Condition{
		Type:    conditionDegraded,
		Status:  metav1.ConditionFalse,
		Reason:  "Synced",
		Message: noReconcileErrors,
	})
}

// markDegraded deliberately mutates nothing beyond conditions and
// observedGeneration: Images and LastSyncTime keep their last known good
// value, so a failed sync does not erase what a previous one found.
func (r *ImageRegistryReconciler) markDegraded(ctx context.Context, reg *dwpkv1alpha1.ImageRegistry, cause error) error {
	return r.patchStatus(ctx, reg, func(*dwpkv1alpha1.ImageRegistry) {}, metav1.Condition{
		Type:    conditionReady,
		Status:  metav1.ConditionFalse,
		Reason:  "SyncFailed",
		Message: cause.Error(),
	}, metav1.Condition{
		Type:    conditionDegraded,
		Status:  metav1.ConditionTrue,
		Reason:  "SyncFailed",
		Message: cause.Error(),
	})
}

// SetupWithManager sets up the controller with the Manager.
//
// No predicate filters out annotation-only updates: a force-sync bump
// (dwpk.devops-ia.io/force-sync) is exactly such an update, and the default
// watch already delivers it without needing an endpoint of its own.
func (r *ImageRegistryReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&dwpkv1alpha1.ImageRegistry{}).
		Owns(&dwpkv1alpha1.WorkspaceImage{}).
		Named("imageregistry").
		Complete(r)
}
