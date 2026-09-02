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
	"strings"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	dwpkv1alpha1 "github.com/devops-ia/dwpk/api/v1alpha1"
)

// UserSpaceReconciler reconciles a UserSpace object
type UserSpaceReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	// AdminClusterRoles are the ClusterRoles bound to a UserSpace whose role is
	// "administrator". They are configuration because their names carry the
	// Helm release prefix, and the manager is granted `bind` on exactly these.
	AdminClusterRoles []string
	// PullSecretNamespace is where a registry's imagePullSecretRef Secret is
	// expected to live. Every Secret there labelled dwpk.devops-ia.io/pull-secret
	// is mirrored into every user namespace, so a private catalog image can be
	// pulled from any of them.
	PullSecretNamespace string
}

// The reconcile reads Projects to learn who manages them, so the manager
// RoleBindings can be built. Read-only: the UserSpace controller never writes a
// Project.
// The GPU resource name comes from the platform settings, so the quota this
// controller writes names the resource the cluster actually has.
// +kubebuilder:rbac:groups=dwpk.devops-ia.io,resources=platformconfigs,verbs=get;list;watch
// +kubebuilder:rbac:groups=dwpk.devops-ia.io,resources=userspaces,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=dwpk.devops-ia.io,resources=userspaces/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=dwpk.devops-ia.io,resources=userspaces/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=namespaces;resourcequotas;limitranges;serviceaccounts,verbs=get;list;watch;create;update;patch;delete
// Secrets: read in PullSecretNamespace to find what to mirror, write in every
// user namespace to place the copy.
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete
// RBAC escalation prevention refuses to grant a verb the granter does not hold,
// so the owner Role's claim deletion needs this even though the controller
// never deletes one itself.
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch;delete
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch
// +kubebuilder:rbac:groups=discovery.k8s.io,resources=endpointslices,verbs=get;list;watch
// +kubebuilder:rbac:groups=networking.k8s.io,resources=networkpolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles;rolebindings;clusterroles;clusterrolebindings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterroles,resourceNames=edit,verbs=bind

// RBAC escalation prevention refuses a Role or ClusterRole granting rights the
// creator lacks, so the manager must hold everything it hands to a user: reads
// on pods, logs, events and the image catalog. It never uses them itself.
// pods/exec so the manager can grant it: escalation prevention refuses to hand
// out a verb the granter does not itself hold.
// +kubebuilder:rbac:groups="",resources=pods;pods/log,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods/exec,verbs=create
// +kubebuilder:rbac:groups="",resources=events,verbs=get;list;watch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=get;list;watch
// The manager needs `use` itself, not because it ever uses an image, but
// because RBAC escalation prevention refuses to grant a verb the granter does
// not hold - and the owner ClusterRole grants exactly this one.
// +kubebuilder:rbac:groups=dwpk.devops-ia.io,resources=workspaceimages,verbs=get;list;watch;use;patch;update

// Reconcile turns one UserSpace into the namespace, quota, limits, network
// policy, service account and role bindings that a user needs to exist (§5.1).
func (r *UserSpaceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	us := &dwpkv1alpha1.UserSpace{}
	if err := r.Get(ctx, req.NamespacedName, us); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !us.DeletionTimestamp.IsZero() {
		// Cluster-scoped owner, so garbage collection removes the namespace and
		// everything in it. No finalizer to get stuck on.
		return ctrl.Result{}, nil
	}

	if us.Status.State == "" {
		// So `kubectl get userspace` shows Pending rather than a blank column
		// while the namespace is being created.
		if err := r.patchStatus(ctx, us, dwpkv1alpha1.UserSpaceStatePending); err != nil {
			return ctrl.Result{}, err
		}
	}

	apiServer, err := r.apiServerEgress(ctx)
	if err != nil {
		return ctrl.Result{}, errors.Join(err, r.markDegraded(ctx, us, err))
	}

	pullSecrets, err := r.pullSecrets(ctx)
	if err != nil {
		return ctrl.Result{}, errors.Join(err, r.markDegraded(ctx, us, err))
	}

	if err := r.applyChildren(ctx, us, apiServer, pullSecrets); err != nil {
		return ctrl.Result{}, errors.Join(err, r.markDegraded(ctx, us, err))
	}
	if err := r.reconcileRoleGrants(ctx, us); err != nil {
		return ctrl.Result{}, errors.Join(err, r.markDegraded(ctx, us, err))
	}
	return ctrl.Result{}, r.markReady(ctx, us)
}

// applyChildren writes every owned object. Order matters only in that the
// namespace has to exist before anything can be created inside it.
func (r *UserSpaceReconciler) applyChildren(
	ctx context.Context,
	us *dwpkv1alpha1.UserSpace,
	apiServer *networkingv1.NetworkPolicyEgressRule,
	pullSecrets []corev1.Secret,
) error {
	fixed := []client.Object{
		buildNamespace(us),
		buildResourceQuota(us, r.gpuResource(ctx)),
		buildLimitRange(us),
		buildNetworkPolicy(us, apiServer),
		buildServiceAccount(us),
		buildSessionServiceAccount(us),
		buildReadOnlySessionServiceAccount(us),
		buildOwnerRole(us),
		buildOwnerRoleBinding(us),
		buildReadOnlyRole(us),
		buildReadOnlyRoleBinding(us),
		buildEditRoleBinding(us),
		buildOwnerClusterRole(us),
		buildOwnerClusterRoleBinding(us),
	}
	children := make([]client.Object, 0, len(fixed)+len(pullSecrets))
	children = append(children, fixed...)
	for i := range pullSecrets {
		children = append(children, buildPullSecret(us, &pullSecrets[i]))
	}
	for _, child := range children {
		if err := applyOwned(ctx, r.Client, r.Scheme, us, child); err != nil {
			return err
		}
	}
	return nil
}

// pullSecrets lists every Secret in PullSecretNamespace that opts in to
// replication, so applyChildren can mirror each into the user's namespace.
func (r *UserSpaceReconciler) pullSecrets(ctx context.Context) ([]corev1.Secret, error) {
	if r.PullSecretNamespace == "" {
		return nil, nil
	}
	list := &corev1.SecretList{}
	if err := r.List(ctx, list,
		client.InNamespace(r.PullSecretNamespace),
		client.MatchingLabels{dwpkv1alpha1.PullSecretLabel: "true"},
	); err != nil {
		return nil, fmt.Errorf("list pull Secrets in %s: %w", r.PullSecretNamespace, err)
	}
	return list.Items, nil
}

// userSpacesForPullSecretChange retriggers every UserSpace when a source pull
// Secret in PullSecretNamespace changes, so a rotated credential reaches
// every namespace's copy without waiting for something else to touch the
// UserSpace.
//
// ponytail: a full list, filtered in Go - same shape and the same reasoning
// as WorkspaceReconciler.workspacesUsingImage. There is roughly one UserSpace
// per person; add an index if that stops being true.
func (r *UserSpaceReconciler) userSpacesForPullSecretChange(ctx context.Context, obj client.Object) []reconcile.Request {
	secret, ok := obj.(*corev1.Secret)
	if !ok || secret.Namespace != r.PullSecretNamespace || secret.Labels[dwpkv1alpha1.PullSecretLabel] != "true" {
		return nil
	}
	list := &dwpkv1alpha1.UserSpaceList{}
	if err := r.List(ctx, list); err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "listing UserSpaces for pull Secret change", "secret", secret.Name)
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(list.Items))
	for _, us := range list.Items {
		reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&us)})
	}
	return reqs
}

// reconcileRoleGrants applies the cluster-scoped rights a role carries, and
// withdraws the ones it does not.
//
// Both directions matter equally. Garbage collection only fires when the
// UserSpace itself is deleted, so a demotion that merely stopped applying the
// binding would leave a former administrator with cluster-wide rights
// indefinitely.
func (r *UserSpaceReconciler) reconcileRoleGrants(ctx context.Context, us *dwpkv1alpha1.UserSpace) error {
	granted := map[string]bool{}
	if us.Spec.EffectiveRole() == dwpkv1alpha1.UserSpaceRoleAdmin {
		for _, clusterRole := range r.AdminClusterRoles {
			if err := applyOwned(ctx, r.Client, r.Scheme, us,
				buildAdministratorBinding(us, clusterRole)); err != nil {
				return err
			}
			granted[clusterRole] = true
		}
	}

	// Withdrawal sweeps what exists rather than re-deriving what should: a
	// ClusterRole dropped from AdminClusterRoles is no longer a name this loop
	// can construct, so a binding to it would otherwise be unreachable and
	// survive forever. A live cluster had exactly that - a binding to the
	// deleted dwpk-project-admin-role, which every `auth can-i` then reported as
	// a missing-role error.
	//
	// The prefix is the guard. buildOwnerClusterRoleBinding carries the same
	// label and must not be swept.
	bindings := &rbacv1.ClusterRoleBindingList{}
	if err := r.List(ctx, bindings, client.MatchingLabels{userSpaceLabel: us.Name}); err != nil {
		return fmt.Errorf("list administrator bindings for %s: %w", us.Name, err)
	}
	for i := range bindings.Items {
		binding := &bindings.Items[i]
		if !strings.HasPrefix(binding.Name, adminBindingPrefix) || granted[binding.RoleRef.Name] {
			continue
		}
		if err := r.Delete(ctx, binding); client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("withdraw ClusterRoleBinding %s: %w", binding.Name, err)
		}
	}
	return nil
}

// apiServerEgress resolves the kube-apiserver from the `kubernetes` Service and
// its EndpointSlices in the default namespace, per §4.2: self-configuring beats one more
// flag to set wrong. A pod dials the ClusterIP, but a NetworkPolicy is evaluated
// after DNAT on most CNIs, so the endpoint address is the one that has to be
// allowed; the ClusterIP is allowed too, for the CNIs that evaluate before.
func (r *UserSpaceReconciler) apiServerEgress(ctx context.Context) (*networkingv1.NetworkPolicyEgressRule, error) {
	key := types.NamespacedName{Name: "kubernetes", Namespace: metav1.NamespaceDefault}

	svc := &corev1.Service{}
	if err := r.Get(ctx, key, svc); err != nil {
		return nil, fmt.Errorf("get Service %s: %w", key, err)
	}
	// EndpointSlice rather than the Endpoints of §4.2: same fact, and Endpoints
	// is deprecated from v1.33.
	slices := &discoveryv1.EndpointSliceList{}
	if err := r.List(ctx, slices,
		client.InNamespace(metav1.NamespaceDefault),
		client.MatchingLabels{discoveryv1.LabelServiceName: key.Name},
	); err != nil {
		return nil, fmt.Errorf("list EndpointSlices for %s: %w", key, err)
	}

	rule := networkingv1.NetworkPolicyEgressRule{}
	addPort := func(port int32) {
		p := intstr.FromInt32(port)
		rule.Ports = append(rule.Ports, networkingv1.NetworkPolicyPort{Port: &p})
	}
	for _, port := range svc.Spec.Ports {
		addPort(port.Port)
	}
	if svc.Spec.ClusterIP != "" && svc.Spec.ClusterIP != corev1.ClusterIPNone {
		rule.To = append(rule.To, hostPeer(svc.Spec.ClusterIP))
	}
	for _, slice := range slices.Items {
		for _, ep := range slice.Endpoints {
			for _, addr := range ep.Addresses {
				rule.To = append(rule.To, hostPeer(addr))
			}
		}
		for _, port := range slice.Ports {
			if port.Port != nil {
				addPort(*port.Port)
			}
		}
	}
	if len(rule.To) == 0 {
		return nil, nil
	}
	return &rule, nil
}

// hostPeer is one address as an ipBlock, the only peer kind that can name
// something which is not a pod.
func hostPeer(ip string) networkingv1.NetworkPolicyPeer {
	bits := "/32"
	if strings.Contains(ip, ":") {
		bits = "/128"
	}
	return networkingv1.NetworkPolicyPeer{IPBlock: &networkingv1.IPBlock{CIDR: ip + bits}}
}

func (r *UserSpaceReconciler) markReady(ctx context.Context, us *dwpkv1alpha1.UserSpace) error {
	return r.patchStatus(ctx, us, dwpkv1alpha1.UserSpaceStateReady, metav1.Condition{
		Type:    "Ready",
		Status:  metav1.ConditionTrue,
		Reason:  "NamespaceProvisioned",
		Message: "namespace, quota and role bindings are in place",
	}, metav1.Condition{
		Type:    "Degraded",
		Status:  metav1.ConditionFalse,
		Reason:  "NamespaceProvisioned",
		Message: "no reconcile errors",
	})
}

func (r *UserSpaceReconciler) markDegraded(ctx context.Context, us *dwpkv1alpha1.UserSpace, cause error) error {
	return r.patchStatus(ctx, us, dwpkv1alpha1.UserSpaceStateFailed, metav1.Condition{
		Type:    "Ready",
		Status:  metav1.ConditionFalse,
		Reason:  "ProvisioningFailed",
		Message: cause.Error(),
	}, metav1.Condition{
		Type:    "Degraded",
		Status:  metav1.ConditionTrue,
		Reason:  "ProvisioningFailed",
		Message: cause.Error(),
	})
}

// patchStatus records what this reconcile observed. observedGeneration is set
// from the generation actually acted on, so a stale Ready is detectable.
func (r *UserSpaceReconciler) patchStatus(
	ctx context.Context,
	us *dwpkv1alpha1.UserSpace,
	phase string,
	conditions ...metav1.Condition,
) error {
	patch := client.MergeFrom(us.DeepCopy())
	us.Status.Namespace = namespaceFor(us)
	us.Status.State = phase
	us.Status.ObservedGeneration = us.Generation
	for _, c := range conditions {
		c.ObservedGeneration = us.Generation
		apimeta.SetStatusCondition(&us.Status.Conditions, c)
	}
	if err := r.Status().Patch(ctx, us, patch); err != nil {
		return fmt.Errorf("patch UserSpace status %s: %w", us.Name, err)
	}
	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *UserSpaceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&dwpkv1alpha1.UserSpace{}).
		Owns(&corev1.Namespace{}).
		Owns(&corev1.ResourceQuota{}).
		Owns(&corev1.LimitRange{}).
		Owns(&corev1.ServiceAccount{}).
		Owns(&networkingv1.NetworkPolicy{}).
		Owns(&rbacv1.Role{}).
		Owns(&rbacv1.RoleBinding{}).
		Owns(&rbacv1.ClusterRole{}).
		Owns(&rbacv1.ClusterRoleBinding{}).
		Owns(&corev1.Secret{}).
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(r.userSpacesForPullSecretChange)).
		Named("userspace").
		Complete(r)
}

// gpuResource is the extended resource a GPU is requested as, from the platform
// settings. A read failure falls back to the default: a namespace's whole quota
// should not wait on an optional settings object.
func (r *UserSpaceReconciler) gpuResource(ctx context.Context) corev1.ResourceName {
	config := &dwpkv1alpha1.PlatformConfig{}
	key := types.NamespacedName{Name: dwpkv1alpha1.PlatformConfigName}
	if err := r.Get(ctx, key, config); err != nil {
		return corev1.ResourceName(dwpkv1alpha1.DefaultGPUResourceName)
	}
	return corev1.ResourceName(config.GPUResource())
}
