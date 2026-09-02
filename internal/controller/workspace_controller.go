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
	"maps"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	dwpkv1alpha1 "github.com/devops-ia/dwpk/api/v1alpha1"
	"github.com/devops-ia/dwpk/internal/workspace"
)

// Condition types on a Workspace (§5.2). Reused by the UserSpace and
// ImageRegistry reconcilers too - conditions across every reconciler in this
// package share the same Ready/Degraded shape.
const (
	conditionReady         = "Ready"
	conditionDegraded      = "Degraded"
	conditionImageResolved = "ImageResolved"
	noReconcileErrors      = "no reconcile errors"
)

// WorkspaceReconciler reconciles a Workspace object
type WorkspaceReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// GatewayHost is the SSH gateway users connect through. It is deployment
	// configuration, not per-workspace state, so it arrives as a manager flag.
	GatewayHost string

	// GatewayService optionally names the gateway's own Service as
	// "namespace/name". When it is set and that Service has a LoadBalancer
	// address, endpoints use the address instead of GatewayHost.
	//
	// This matters because §6 notes the VPN reaches only LoadBalancer
	// addresses: a cluster-internal DNS name is not something a user can put
	// into ssh. §4.2 prefers self-configuring over a flag, but the gateway
	// Service name embeds the Helm release name, so unlike the `kubernetes`
	// Service it cannot be hardcoded. Empty leaves GatewayHost alone, which is
	// what a deployment with no LoadBalancer wants.
	GatewayService string

	// GitSSHEncryptionKeyNamespace is where
	// dwpkv1alpha1.GitSSHEncryptionKeySecretName lives - the operator's own
	// namespace, not any user's. A manager flag rather than rediscovered per
	// reconcile, matching GatewayHost.
	GitSSHEncryptionKeyNamespace string
}

// +kubebuilder:rbac:groups=dwpk.devops-ia.io,resources=workspaces,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=dwpk.devops-ia.io,resources=workspaces/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=dwpk.devops-ia.io,resources=workspaces/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get
// Also reads dwpkv1alpha1.GitSSHEncryptionKeySecretName in
// GitSSHEncryptionKeyNamespace, and creates/updates/deletes
// dwpkv1alpha1.GitSSHKeysRuntimeSecretName per user namespace - both already
// covered by the manager ClusterRole's wider secrets grant from
// UserSpaceReconciler's own marker, so this stays documentation of intent
// rather than a distinct enforced grant.

// Reconcile turns one Workspace into a headless Service and a StatefulSet, and
// reports what became of them (§5.1).
func (r *WorkspaceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	ws := &dwpkv1alpha1.Workspace{}
	if err := r.Get(ctx, req.NamespacedName, ws); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !ws.DeletionTimestamp.IsZero() {
		// The StatefulSet and Service are owned, so garbage collection removes
		// them. The home PVC is deliberately not owned by the Workspace: a
		// volumeClaimTemplate PVC outlives its StatefulSet, which is what makes
		// deletion recoverable rather than final.
		return ctrl.Result{}, nil
	}

	img := &dwpkv1alpha1.WorkspaceImage{}
	if err := r.Get(ctx, types.NamespacedName{Name: ws.Spec.ImageRef.Name}, img); err != nil {
		if apierrors.IsNotFound(err) {
			// No requeue: the watch on WorkspaceImage retriggers this when the
			// catalog entry appears. A timer would only add load to waiting.
			return ctrl.Result{}, r.markImageNotFound(ctx, ws)
		}
		return ctrl.Result{}, fmt.Errorf("get WorkspaceImage %s: %w", ws.Spec.ImageRef.Name, err)
	}

	if ws.Spec.Storage == nil {
		// The CRD defaults storage, so an unset one means an object written
		// before that default existed. Reporting it beats dereferencing nil in
		// a builder.
		return ctrl.Result{}, r.markStorageUnset(ctx, ws)
	}

	gitSSHKeysRef, err := r.ensureGitSSHRuntimeSecret(ctx, ws.Namespace)
	if err != nil {
		return ctrl.Result{}, errors.Join(err, r.markDegraded(ctx, ws, err))
	}

	if err := r.applyWorkload(ctx, ws, img, gitSSHKeysRef); err != nil {
		return ctrl.Result{}, errors.Join(err, r.markDegraded(ctx, ws, err))
	}

	// Read back through the cache, which may not have caught up with the apply
	// above. A miss is not an error: a zero-valued StatefulSet reports Starting,
	// and the watch retriggers this the moment the cache sees it.
	sts := &appsv1.StatefulSet{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(ws), sts); client.IgnoreNotFound(err) != nil {
		return ctrl.Result{}, fmt.Errorf("get StatefulSet %s: %w", ws.Name, err)
	}
	return ctrl.Result{}, r.markObserved(ctx, ws, sts)
}

func (r *WorkspaceReconciler) applyWorkload(
	ctx context.Context,
	ws *dwpkv1alpha1.Workspace,
	img *dwpkv1alpha1.WorkspaceImage,
	gitSSHKeysRef *corev1.LocalObjectReference,
) error {
	// The Service first: a StatefulSet without its governing Service has pods
	// with no DNS, and creating it second leaves a window where that is true.
	for _, child := range []client.Object{
		workspace.BuildService(ws),
		workspace.BuildStatefulSet(ws, img, gitSSHKeysRef),
	} {
		if err := applyOwned(ctx, r.Client, r.Scheme, ws, child); err != nil {
			return err
		}
	}
	return nil
}

// ensureGitSSHRuntimeSecret is what actually makes the git-ssh-keys feature
// work: dwpkv1alpha1.GitSSHKeysSecretName (written by the UI with the
// caller's own forwarded token) holds ciphertext, because kubelet copies a
// mounted Secret's bytes verbatim - nothing decrypts on the way into a pod.
// So the controller decrypts here, server-side, and writes a second Secret
// (dwpkv1alpha1.GitSSHKeysRuntimeSecretName) holding what a workspace
// actually mounts. The encryption key never reaches a pod.
//
// NotFound on the source Secret means the feature is off for this user, not
// an error - and if a runtime Secret is left over from before the user
// deleted their last key, it is cleaned up here too, the same "no keys left,
// no Secret" rule requestAPI.DeleteGitSSHKey already applies to the source.
// NotFound on the encryption key, unlike the source Secret, is a real error:
// keys exist but cannot be decrypted, and silently mounting nothing would
// hide that from whoever is debugging a failed git clone.
func (r *WorkspaceReconciler) ensureGitSSHRuntimeSecret(ctx context.Context, namespace string) (*corev1.LocalObjectReference, error) {
	runtimeKey := types.NamespacedName{Namespace: namespace, Name: dwpkv1alpha1.GitSSHKeysRuntimeSecretName}

	source := &corev1.Secret{}
	sourceKey := types.NamespacedName{Namespace: namespace, Name: dwpkv1alpha1.GitSSHKeysSecretName}
	if err := r.Get(ctx, sourceKey, source); err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("get git-ssh-keys Secret %s: %w", sourceKey, err)
		}
		staleRuntime := &corev1.Secret{}
		if err := r.Get(ctx, runtimeKey, staleRuntime); err == nil {
			if err := r.Delete(ctx, staleRuntime); err != nil && !apierrors.IsNotFound(err) {
				return nil, fmt.Errorf("delete stale git-ssh-keys-runtime Secret %s: %w", runtimeKey, err)
			}
		}
		return nil, nil
	}

	masterKeySecret := &corev1.Secret{}
	masterKeyRef := types.NamespacedName{Namespace: r.GitSSHEncryptionKeyNamespace, Name: dwpkv1alpha1.GitSSHEncryptionKeySecretName}
	if err := r.Get(ctx, masterKeyRef, masterKeySecret); err != nil {
		return nil, fmt.Errorf("get git-ssh encryption key %s: %w", masterKeyRef, err)
	}
	masterKey := masterKeySecret.Data[dwpkv1alpha1.GitSSHEncryptionKeySecretDataKey]

	data := map[string][]byte{}
	for key, ciphertext := range source.Data {
		host, ok := strings.CutPrefix(key, dwpkv1alpha1.GitSSHKeyDataPrefix)
		if !ok {
			continue
		}
		plaintext, err := workspace.DecryptGitSSHKey(masterKey, ciphertext)
		if err != nil {
			return nil, fmt.Errorf("decrypt git-ssh key for %s in %s: %w", host, namespace, err)
		}
		data[key] = plaintext
	}
	data["config"] = []byte(workspace.GitSSHConfig(workspace.GitSSHHostsFromData(source.Data)))

	existing := &corev1.Secret{}
	err := r.Get(ctx, runtimeKey, existing)
	switch {
	case apierrors.IsNotFound(err):
		created := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: runtimeKey.Name, Namespace: namespace},
			Type:       corev1.SecretTypeOpaque,
			Data:       data,
		}
		if err := r.Create(ctx, created); err != nil {
			return nil, fmt.Errorf("create git-ssh-keys-runtime Secret %s: %w", runtimeKey, err)
		}
	case err != nil:
		return nil, fmt.Errorf("get git-ssh-keys-runtime Secret %s: %w", runtimeKey, err)
	case !maps.EqualFunc(existing.Data, data, func(a, b []byte) bool { return string(a) == string(b) }):
		original := existing.DeepCopy()
		existing.Data = data
		if err := r.Patch(ctx, existing, client.MergeFrom(original)); err != nil {
			return nil, fmt.Errorf("patch git-ssh-keys-runtime Secret %s: %w", runtimeKey, err)
		}
	}

	return &corev1.LocalObjectReference{Name: dwpkv1alpha1.GitSSHKeysRuntimeSecretName}, nil
}

func (r *WorkspaceReconciler) markImageNotFound(ctx context.Context, ws *dwpkv1alpha1.Workspace) error {
	msg := fmt.Sprintf("WorkspaceImage %q does not exist", ws.Spec.ImageRef.Name)
	return r.patchStatus(ctx, ws, dwpkv1alpha1.WorkspaceStatePending, "",
		metav1.Condition{
			Type:    conditionImageResolved,
			Status:  metav1.ConditionFalse,
			Reason:  "ImageNotFound",
			Message: msg,
		},
		metav1.Condition{
			Type:    conditionReady,
			Status:  metav1.ConditionFalse,
			Reason:  "ImageNotFound",
			Message: msg,
		},
	)
}

func (r *WorkspaceReconciler) markStorageUnset(ctx context.Context, ws *dwpkv1alpha1.Workspace) error {
	return r.patchStatus(ctx, ws, dwpkv1alpha1.WorkspaceStateFailed, "",
		metav1.Condition{
			Type:    conditionReady,
			Status:  metav1.ConditionFalse,
			Reason:  "StorageNotDefaulted",
			Message: "spec.storage is unset; set it to a quantity such as 10Gi",
		},
	)
}

func (r *WorkspaceReconciler) markDegraded(ctx context.Context, ws *dwpkv1alpha1.Workspace, cause error) error {
	return r.patchStatus(ctx, ws, dwpkv1alpha1.WorkspaceStateFailed, "",
		metav1.Condition{
			Type:    conditionReady,
			Status:  metav1.ConditionFalse,
			Reason:  "WorkloadApplyFailed",
			Message: cause.Error(),
		},
		metav1.Condition{
			Type:    conditionDegraded,
			Status:  metav1.ConditionTrue,
			Reason:  "WorkloadApplyFailed",
			Message: cause.Error(),
		},
	)
}

// markObserved reports the StatefulSet's state as the Workspace's own. Suspend
// is a replica count, so "no pod" is a healthy outcome here, not a failure.
func (r *WorkspaceReconciler) markObserved(
	ctx context.Context,
	ws *dwpkv1alpha1.Workspace,
	sts *appsv1.StatefulSet,
) error {
	phase, ready := dwpkv1alpha1.WorkspaceStateSuspended, metav1.Condition{
		Type:    conditionReady,
		Status:  metav1.ConditionFalse,
		Reason:  "Suspended",
		Message: "spec.running is false; the home volume is retained",
	}
	podName := ""

	if ws.Spec.Running {
		podName = workspace.PodName(ws)
		phase, ready = dwpkv1alpha1.WorkspaceStateStarting, metav1.Condition{
			Type:    conditionReady,
			Status:  metav1.ConditionFalse,
			Reason:  "PodNotReady",
			Message: fmt.Sprintf("pod %s is not ready yet", podName),
		}
		if sts.Status.ReadyReplicas > 0 {
			phase, ready = dwpkv1alpha1.WorkspaceStateRunning, metav1.Condition{
				Type:    conditionReady,
				Status:  metav1.ConditionTrue,
				Reason:  "PodRunning",
				Message: fmt.Sprintf("pod %s is ready", podName),
			}
		}
	}

	return r.patchStatus(ctx, ws, phase, podName, ready,
		metav1.Condition{
			Type:    conditionImageResolved,
			Status:  metav1.ConditionTrue,
			Reason:  "CatalogEntryFound",
			Message: fmt.Sprintf("WorkspaceImage %q resolved", ws.Spec.ImageRef.Name),
		},
		metav1.Condition{
			Type:    conditionDegraded,
			Status:  metav1.ConditionFalse,
			Reason:  "WorkloadApplied",
			Message: noReconcileErrors,
		},
	)
}

// patchStatus records what this reconcile observed. observedGeneration comes
// from the generation actually acted on, so a stale Ready is detectable.
func (r *WorkspaceReconciler) patchStatus(
	ctx context.Context,
	ws *dwpkv1alpha1.Workspace,
	phase, podName string,
	conditions ...metav1.Condition,
) error {
	patch := client.MergeFrom(ws.DeepCopy())
	ws.Status.State = phase
	ws.Status.PodName = podName
	ws.Status.Endpoint = workspace.SSHUser(ws) + "@" + r.resolveGatewayHost(ctx)
	ws.Status.ObservedGeneration = ws.Generation
	for _, c := range conditions {
		c.ObservedGeneration = ws.Generation
		apimeta.SetStatusCondition(&ws.Status.Conditions, c)
	}
	if err := r.Status().Patch(ctx, ws, patch); err != nil {
		return fmt.Errorf("patch Workspace status %s/%s: %w", ws.Namespace, ws.Name, err)
	}
	return nil
}

// resolveGatewayHost is the address a user actually types into ssh.
//
// §6: "the VPN reaches only LoadBalancer addresses", so the configured
// cluster-internal Service DNS name is unusable off-cluster. When the gateway
// Service is known and carries a LoadBalancer address, that address wins.
//
// Every failure falls back to GatewayHost rather than failing the reconcile: a
// workspace with a stale endpoint is still a working workspace, and refusing to
// publish status because a Service read failed would be a worse trade.
func (r *WorkspaceReconciler) resolveGatewayHost(ctx context.Context) string {
	if r.GatewayService == "" {
		return r.GatewayHost
	}
	namespace, name, ok := strings.Cut(r.GatewayService, "/")
	if !ok || namespace == "" || name == "" {
		ctrl.LoggerFrom(ctx).Error(nil, "Ignored malformed gateway Service reference, want namespace/name",
			"gatewayService", r.GatewayService)
		return r.GatewayHost
	}

	svc := &corev1.Service{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, svc); err != nil {
		ctrl.LoggerFrom(ctx).V(1).Info("Could not read gateway Service, used configured host",
			"gatewayService", r.GatewayService, "gatewayHost", r.GatewayHost)
		return r.GatewayHost
	}
	if host := loadBalancerHost(svc); host != "" {
		return host
	}
	return r.GatewayHost
}

// loadBalancerHost prefers a hostname over an address. An internal AWS NLB, the
// deployment §6 is written against, publishes a hostname and no IP, and a
// hostname keeps working when the address behind it is reassigned.
func loadBalancerHost(svc *corev1.Service) string {
	for _, ingress := range svc.Status.LoadBalancer.Ingress {
		if ingress.Hostname != "" {
			return ingress.Hostname
		}
	}
	for _, ingress := range svc.Status.LoadBalancer.Ingress {
		if ingress.IP != "" {
			return ingress.IP
		}
	}
	return ""
}

// workspacesUsingImage maps a WorkspaceImage back to the Workspaces that
// reference it, so creating a missing catalog entry retriggers them.
//
// ponytail: a full list, filtered in Go. There is roughly one Workspace per
// user; add a field index on spec.imageRef.name if that ever stops being true.
func (r *WorkspaceReconciler) workspacesUsingImage(ctx context.Context, img client.Object) []reconcile.Request {
	list := &dwpkv1alpha1.WorkspaceList{}
	if err := r.List(ctx, list); err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "listing Workspaces for WorkspaceImage", "image", img.GetName())
		return nil
	}
	var reqs []reconcile.Request
	for _, ws := range list.Items {
		if ws.Spec.ImageRef.Name == img.GetName() {
			reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&ws)})
		}
	}
	return reqs
}

// workspacesInGitSSHKeysSecretNamespace maps a change to the fixed-name
// git-ssh-keys Secret back to every Workspace in its namespace, so adding or
// removing a key retriggers all of that user's workspaces without waiting for
// something else to touch them.
func (r *WorkspaceReconciler) workspacesInGitSSHKeysSecretNamespace(ctx context.Context, secret client.Object) []reconcile.Request {
	if secret.GetName() != dwpkv1alpha1.GitSSHKeysSecretName {
		return nil
	}
	list := &dwpkv1alpha1.WorkspaceList{}
	if err := r.List(ctx, list, client.InNamespace(secret.GetNamespace())); err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "listing Workspaces for git-ssh-keys Secret", "namespace", secret.GetNamespace())
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(list.Items))
	for _, ws := range list.Items {
		reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&ws)})
	}
	return reqs
}

// SetupWithManager sets up the controller with the Manager.
func (r *WorkspaceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&dwpkv1alpha1.Workspace{}).
		Owns(&appsv1.StatefulSet{}).
		Owns(&corev1.Service{}).
		Watches(&dwpkv1alpha1.WorkspaceImage{}, handler.EnqueueRequestsFromMapFunc(r.workspacesUsingImage)).
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(r.workspacesInGitSSHKeysSecretNamespace)).
		Named("workspace").
		Complete(r)
}
