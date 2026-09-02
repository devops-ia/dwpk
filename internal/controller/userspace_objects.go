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
	"slices"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	dwpkv1alpha1 "github.com/devops-ia/dwpk/api/v1alpha1"
	"github.com/devops-ia/dwpk/internal/workspace"
)

// Names of the objects a UserSpace owns. Fixed rather than derived: there is
// exactly one of each per namespace, and a stable name makes them findable.
const (
	quotaName               = "dwpk-quota"
	limitRangeName          = "dwpk-limits"
	networkPolicyName       = "dwpk-isolation"
	ownerRoleName           = "dwpk-workspace-user"
	ownerBindingName        = "dwpk-owner"
	readOnlyRoleName        = "dwpk-workspace-reader"
	readOnlyBindingName     = "dwpk-reader"
	clusterRolePrefix       = "dwpk-userspace-"
	editBindingName         = "dwpk-workspace-edit"
	managerBindingName      = "dwpk-project-managers"
	editClusterRole         = "edit"
	rbacAPIVersion          = "rbac.authorization.k8s.io/v1"
	clusterRoleKind         = "ClusterRole"
	roleKind                = "Role"
	roleBindingKind         = "RoleBinding"
	serviceAccountKind      = "ServiceAccount"
	clusterRoleBindingKind  = "ClusterRoleBinding"
	getVerb                 = "get"
	createVerb              = "create"
	patchVerb               = "patch"
	watchVerb               = "watch"
	useVerb                 = "use"
	workspaceImagesResource = "workspaceimages"
	userSpaceLabel          = "dwpk.devops-ia.io/userspace"
	labelValueTrue          = "true"
)

// Verbs the owner gets over their own Workspaces. Kept within the verbs the
// manager itself holds: RBAC escalation prevention rejects a Role granting more.
var (
	workspaceOwnerVerbs = []string{createVerb, "delete", getVerb, "list", patchVerb, "update", watchVerb}
	readVerbs           = []string{getVerb, "list", watchVerb}
)

// dwpkGroup is the API group these CRs live in.
var dwpkGroup = dwpkv1alpha1.SchemeGroupVersion.Group

// namespaceFor is the naming convention from §4.2: UserSpace "alice" lives in "dwpk-alice".
func namespaceFor(us *dwpkv1alpha1.UserSpace) string {
	return us.NamespaceName()
}

func childMeta(us *dwpkv1alpha1.UserSpace, name string) metav1.ObjectMeta {
	return metav1.ObjectMeta{
		Name:      name,
		Namespace: namespaceFor(us),
		Labels:    map[string]string{userSpaceLabel: us.Name},
	}
}

func buildNamespace(us *dwpkv1alpha1.UserSpace) *corev1.Namespace {
	return &corev1.Namespace{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Namespace"},
		ObjectMeta: metav1.ObjectMeta{
			Name:   namespaceFor(us),
			Labels: map[string]string{userSpaceLabel: us.Name},
		},
	}
}

// buildResourceQuota is the hard limit behind the webhook's nicer message.
//
// There is no count/workspaces entry any more. The workspace limit counts
// RUNNING workspaces, and Kubernetes counts objects - the two are different
// numbers, and a hard limit that disagrees with what the screen says is worse
// than no hard limit. CPU, memory and storage are unaffected: those are pod and
// PVC quantities, and a stopped workspace has no pod.
//
// The GPU entry is one more Hard key, enforced natively at pod admission. The
// resource name is the platform's, because the vendor decides what a GPU is
// called.
func buildResourceQuota(us *dwpkv1alpha1.UserSpace, gpuResource corev1.ResourceName) *corev1.ResourceQuota {
	q := us.Spec.Quota
	hard := corev1.ResourceList{
		corev1.ResourceRequestsCPU:     q.CPU,
		corev1.ResourceRequestsMemory:  q.Memory,
		corev1.ResourceRequestsStorage: q.Storage,
	}
	// Only when there is an allowance. A hard zero would refuse every pod that
	// mentions a GPU, which is right; but writing it for every namespace makes
	// the object noisier than the setting it reflects.
	if q.GPU > 0 {
		hard[corev1.ResourceName("requests."+string(gpuResource))] =
			*resource.NewQuantity(int64(q.GPU), resource.DecimalSI)
	}
	return &corev1.ResourceQuota{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ResourceQuota"},
		ObjectMeta: childMeta(us, quotaName),
		Spec:       corev1.ResourceQuotaSpec{Hard: hard},
	}
}

// buildLimitRange gives containers a request when their author omitted one.
// Without it every pod created by hand in the namespace is rejected, because a
// quota on requests.cpu makes a request mandatory.
func buildLimitRange(us *dwpkv1alpha1.UserSpace) *corev1.LimitRange {
	return &corev1.LimitRange{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "LimitRange"},
		ObjectMeta: childMeta(us, limitRangeName),
		Spec: corev1.LimitRangeSpec{
			Limits: []corev1.LimitRangeItem{{
				Type: corev1.LimitTypeContainer,
				DefaultRequest: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("100m"),
					corev1.ResourceMemory: resource.MustParse("128Mi"),
				},
			}},
		},
	}
}

// buildNetworkPolicy applies spec.networkPolicy. Ingress is always restricted to
// the user's own namespace; only egress differs between the two postures.
//
// apiServer is the resolved kube-apiserver rule (§4.2): requirement 7 needs the
// API reachable from inside the workspace, and a pod-selector peer cannot name
// an endpoint that is not a pod. It is nil when the address could not be read,
// in which case the policy is still written - a missing rule denies API access,
// which is the safe direction to fail.
func buildNetworkPolicy(
	us *dwpkv1alpha1.UserSpace,
	apiServer *networkingv1.NetworkPolicyEgressRule,
) *networkingv1.NetworkPolicy {
	// Isolated: the user's own namespace, plus DNS so names resolve at all, plus
	// the API server and everything outside the cluster.
	egress := []networkingv1.NetworkPolicyEgressRule{
		{To: []networkingv1.NetworkPolicyPeer{{PodSelector: &metav1.LabelSelector{}}}},
		dnsEgressRule(),
		offClusterEgressRule(),
	}
	if apiServer != nil {
		egress = append(egress, *apiServer)
	}
	if us.Spec.NetworkPolicy == dwpkv1alpha1.NetworkPolicyClusterEgress {
		// An egress rule with no peers and no ports permits everything.
		egress = []networkingv1.NetworkPolicyEgressRule{{}}
	}

	return &networkingv1.NetworkPolicy{
		TypeMeta:   metav1.TypeMeta{APIVersion: "networking.k8s.io/v1", Kind: "NetworkPolicy"},
		ObjectMeta: childMeta(us, networkPolicyName),
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress},
			Ingress: []networkingv1.NetworkPolicyIngressRule{
				{From: []networkingv1.NetworkPolicyPeer{{PodSelector: &metav1.LabelSelector{}}}},
			},
			Egress: egress,
		},
	}
}

// offClusterEgressRule permits everything outside the cluster, minus the
// private ranges that carry other namespaces' pods and services. VS Code
// downloads its server into the pod over HTTPS (§6.2), so a posture that blocks
// this breaks the primary access path.
func offClusterEgressRule() networkingv1.NetworkPolicyEgressRule {
	return networkingv1.NetworkPolicyEgressRule{
		To: []networkingv1.NetworkPolicyPeer{{
			IPBlock: &networkingv1.IPBlock{
				CIDR:   "0.0.0.0/0",
				Except: []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"},
			},
		}},
	}
}

func dnsEgressRule() networkingv1.NetworkPolicyEgressRule {
	udp, tcp := corev1.ProtocolUDP, corev1.ProtocolTCP
	port := intstr.FromInt32(53)
	return networkingv1.NetworkPolicyEgressRule{
		To: []networkingv1.NetworkPolicyPeer{{
			NamespaceSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{corev1.LabelMetadataName: metav1.NamespaceSystem},
			},
			PodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"k8s-app": "kube-dns"}},
		}},
		Ports: []networkingv1.NetworkPolicyPort{{Protocol: &udp, Port: &port}, {Protocol: &tcp, Port: &port}},
	}
}

// buildPullSecret mirrors one pull Secret from the manager's own namespace
// into the user's namespace, so a private catalog image can actually be
// pulled by the kubelet there. Type and Data only: the source Secret's own
// name, namespace and any other metadata do not belong on the copy.
func buildPullSecret(us *dwpkv1alpha1.UserSpace, source *corev1.Secret) *corev1.Secret {
	return &corev1.Secret{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: childMeta(us, source.Name),
		Type:       source.Type,
		Data:       source.Data,
	}
}

func buildServiceAccount(us *dwpkv1alpha1.UserSpace) *corev1.ServiceAccount {
	return &corev1.ServiceAccount{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: serviceAccountKind},
		ObjectMeta: childMeta(us, workspace.ServiceAccountName),
	}
}

// buildSessionServiceAccount is the identity the UI mints browser-session
// tokens for. It is deliberately separate from the workspace ServiceAccount
// and is never named by any pod, so whatever a session is allowed to do is not
// also handed to anyone with a shell in the user's workspace.
func buildSessionServiceAccount(us *dwpkv1alpha1.UserSpace) *corev1.ServiceAccount {
	return &corev1.ServiceAccount{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: serviceAccountKind},
		ObjectMeta: childMeta(us, workspace.SessionServiceAccountName),
	}
}

// buildReadOnlySessionServiceAccount is the identity a read-scoped API token
// mints for.
func buildReadOnlySessionServiceAccount(us *dwpkv1alpha1.UserSpace) *corev1.ServiceAccount {
	return &corev1.ServiceAccount{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: serviceAccountKind},
		ObjectMeta: childMeta(us, workspace.ReadOnlySessionServiceAccountName),
	}
}

// buildReadOnlyRole is the owner's rights with every write removed.
//
// It is written as its own rule set rather than as "the owner role minus
// something": a verb added to the owner role later must not silently appear
// here, and a subtraction would have made that the default.
func buildReadOnlyRole(us *dwpkv1alpha1.UserSpace) *rbacv1.Role {
	return &rbacv1.Role{
		TypeMeta:   metav1.TypeMeta{APIVersion: rbacAPIVersion, Kind: roleKind},
		ObjectMeta: childMeta(us, readOnlyRoleName),
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{dwpkGroup},
				Resources: []string{"workspaces"},
				Verbs:     readVerbs,
			},
			{
				// get only, matching the owner Role. A status subresource cannot
				// be listed, and the manager holds only get/update/patch on it -
				// RBAC escalation prevention refuses to grant a verb the granter
				// does not itself hold, so asking for list here fails the whole
				// reconcile rather than quietly producing a lesser Role.
				APIGroups: []string{dwpkGroup},
				Resources: []string{"workspaces/status"},
				Verbs:     []string{getVerb},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"pods", "pods/log"},
				Verbs:     readVerbs,
			},
			// The browser terminal. It runs the gateway's exec code with the
			// caller's own token (SPEC §8.1), so without this the terminal fails
			// with exit 255 - which is what execWithIO returns when it cannot
			// build the executor, and a 403 is exactly that.
			//
			// It grants nothing new in substance: the same person already reaches
			// the same container over SSH through the gateway, which is the
			// interface this one shares.
			{
				APIGroups: []string{""},
				Resources: []string{"pods/exec"},
				Verbs:     []string{createVerb},
			},
			{
				APIGroups: []string{"", "events.k8s.io"},
				Resources: []string{"events"},
				Verbs:     readVerbs,
			},
		},
	}
}

func buildReadOnlyRoleBinding(us *dwpkv1alpha1.UserSpace) *rbacv1.RoleBinding {
	return &rbacv1.RoleBinding{
		TypeMeta:   metav1.TypeMeta{APIVersion: rbacAPIVersion, Kind: roleBindingKind},
		ObjectMeta: childMeta(us, readOnlyBindingName),
		Subjects: []rbacv1.Subject{{
			Kind:      rbacv1.ServiceAccountKind,
			Name:      workspace.ReadOnlySessionServiceAccountName,
			Namespace: namespaceFor(us),
		}},
		RoleRef: rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: roleKind, Name: readOnlyRoleName},
	}
}

// readOnlySessionSubject is the read-only session ServiceAccount as a subject.
func readOnlySessionSubject(us *dwpkv1alpha1.UserSpace) rbacv1.Subject {
	return rbacv1.Subject{
		Kind:      rbacv1.ServiceAccountKind,
		Name:      workspace.ReadOnlySessionServiceAccountName,
		Namespace: namespaceFor(us),
	}
}

// sessionSubject is the session ServiceAccount as an RBAC subject.
func sessionSubject(us *dwpkv1alpha1.UserSpace) rbacv1.Subject {
	return rbacv1.Subject{
		Kind:      rbacv1.ServiceAccountKind,
		Name:      workspace.SessionServiceAccountName,
		Namespace: namespaceFor(us),
	}
}

// buildOwnerRole is the "users access only their own session" boundary (§7): a
// Role lives in one namespace, so it can only ever grant rights there. Nothing
// the owner is given is cluster-scoped, which is why they cannot list anyone
// else's pods - or their own at cluster scope.
func buildOwnerRole(us *dwpkv1alpha1.UserSpace) *rbacv1.Role {
	return &rbacv1.Role{
		TypeMeta:   metav1.TypeMeta{APIVersion: rbacAPIVersion, Kind: "Role"},
		ObjectMeta: childMeta(us, ownerRoleName),
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{dwpkGroup},
				Resources: []string{"workspaces"},
				Verbs:     workspaceOwnerVerbs,
			},
			{
				APIGroups: []string{dwpkGroup},
				Resources: []string{"workspaces/status"},
				Verbs:     []string{getVerb},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"pods", "pods/log"},
				Verbs:     readVerbs,
			},
			// The browser terminal. It runs the gateway's exec code with the
			// caller's own token (SPEC §8.1), so without this the terminal fails
			// with exit 255 - which is what execWithIO returns when it cannot
			// build the executor, and a 403 is exactly that.
			//
			// It grants nothing new in substance: the same person already reaches
			// the same container over SSH through the gateway, which is the
			// interface this one shares.
			{
				APIGroups: []string{""},
				Resources: []string{"pods/exec"},
				Verbs:     []string{createVerb},
			},
			// Deleting a workspace can take its home volume with it, which the
			// StatefulSet will not do: a volumeClaimTemplate's PVC deliberately
			// outlives its StatefulSet so that stopping is never destructive.
			// Removing it is therefore a separate, explicit act, and this is the
			// grant that allows it.
			//
			// No create: the StatefulSet makes the claim. Being able to delete
			// your own volume is not being able to conjure storage.
			{
				APIGroups: []string{""},
				Resources: []string{"persistentvolumeclaims"},
				Verbs:     append(slices.Clone(readVerbs), "delete"),
			},
			{
				// kubectl reads events from events.k8s.io; controllers still
				// write the core ones. The user wants to see both.
				APIGroups: []string{"", "events.k8s.io"},
				Resources: []string{"events"},
				Verbs:     readVerbs,
			},
			// Self-service private SSH keys for git access to the owner's own
			// repositories (dwpkv1alpha1.GitSSHKeysSecretName) - written by the
			// UI with the caller's own token, so the caller needs the rights to
			// write it. No list/watch: the UI always addresses this Secret by
			// its one fixed name. create cannot be name-restricted in RBAC
			// (resourceNames does not apply to create), so this technically
			// permits creating any Secret name in the owner's own namespace -
			// acceptable, since they already functionally own everything there.
			{
				APIGroups: []string{""},
				Resources: []string{"secrets"},
				Verbs:     []string{createVerb, getVerb, "update", patchVerb, "delete"},
			},
		},
	}
}

// buildOwnerClusterRole grants the only two cluster-scoped reads the owner gets.
//
// UserSpace is cluster-scoped, and RBAC cannot filter a list - granting `list`
// would expose every other user's. `get` with resourceNames does not leak,
// because the name is deterministic and a client can always ask for it by name.
// WorkspaceImage is the catalog (§4.1) and is meant to be readable by everyone.
func buildOwnerClusterRole(us *dwpkv1alpha1.UserSpace) *rbacv1.ClusterRole {
	return &rbacv1.ClusterRole{
		TypeMeta: metav1.TypeMeta{APIVersion: rbacAPIVersion, Kind: clusterRoleKind},
		ObjectMeta: metav1.ObjectMeta{
			Name:   clusterRolePrefix + us.Name,
			Labels: map[string]string{userSpaceLabel: us.Name},
		},
		Rules: []rbacv1.PolicyRule{
			{
				// patch is here so a person can save their own SSH keys from
				// My profile. RBAC cannot say "only this field", so the
				// UserSpace validator refuses a non-administrator changing
				// anything but spec.sshAuthorizedKeys - without that, patch on
				// your own UserSpace is patch on your own quota.
				APIGroups:     []string{dwpkGroup},
				Resources:     []string{userSpacesResource},
				ResourceNames: []string{us.Name},
				Verbs:         []string{getVerb, watchVerb, patchVerb},
			},
			// `use` is the verb the create path and the catalog both check
			// (SPEC §7.6). Without it the catalog renders empty for everyone but
			// an admin, whose `*` covered it by accident.
			{
				APIGroups: []string{dwpkGroup},
				Resources: []string{workspaceImagesResource},
				Verbs:     append(slices.Clone(readVerbs), useVerb),
			},
		},
	}
}

func buildOwnerClusterRoleBinding(us *dwpkv1alpha1.UserSpace) *rbacv1.ClusterRoleBinding {
	return &rbacv1.ClusterRoleBinding{
		TypeMeta: metav1.TypeMeta{APIVersion: rbacAPIVersion, Kind: clusterRoleBindingKind},
		ObjectMeta: metav1.ObjectMeta{
			Name:   clusterRolePrefix + us.Name,
			Labels: map[string]string{userSpaceLabel: us.Name},
		},
		// The reader joins the owner and the session here: this ClusterRole
		// grants only reads - the catalog, and this UserSpace by name - so
		// there is nothing in it to narrow for a read-only token.
		Subjects: []rbacv1.Subject{ownerSubject(us), sessionSubject(us), readOnlySessionSubject(us)},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     clusterRoleKind,
			Name:     clusterRolePrefix + us.Name,
		},
	}
}

func ownerSubject(us *dwpkv1alpha1.UserSpace) rbacv1.Subject {
	return rbacv1.Subject{APIGroup: rbacv1.GroupName, Kind: rbacv1.UserKind, Name: us.Spec.Owner}
}

// projectManager is one manager of a namespace: the human, and the UserSpace

// buildOwnerRoleBinding gives the person their own namespace.
//
// Two subjects for one human. The User covers kubectl, where an OIDC identity
// is the Kubernetes username; the session ServiceAccount covers the browser,
// because the UI mints a token for that account rather than impersonating
// anyone (SPEC §8.1). Binding only the first left every screen 403ing while
// kubectl worked, which is a confusing way to find out.
func buildOwnerRoleBinding(us *dwpkv1alpha1.UserSpace) *rbacv1.RoleBinding {
	return &rbacv1.RoleBinding{
		TypeMeta:   metav1.TypeMeta{APIVersion: rbacAPIVersion, Kind: roleBindingKind},
		ObjectMeta: childMeta(us, ownerBindingName),
		Subjects:   []rbacv1.Subject{ownerSubject(us), sessionSubject(us)},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: roleKind, Name: ownerRoleName},
	}
}

// buildEditRoleBinding is requirement 7: kubectl from inside the workspace
// works against the user's own namespace and nowhere else.
//
// It binds the built-in `edit` ClusterRole to the workspace pod's own
// ServiceAccount, scoped to this namespace by being a RoleBinding rather than a
// ClusterRoleBinding. The subject is the pod's identity, not the person's - the
// container gets these rights, and the human's own access is the binding above.
func buildEditRoleBinding(us *dwpkv1alpha1.UserSpace) *rbacv1.RoleBinding {
	return &rbacv1.RoleBinding{
		TypeMeta:   metav1.TypeMeta{APIVersion: rbacAPIVersion, Kind: roleBindingKind},
		ObjectMeta: childMeta(us, editBindingName),
		Subjects: []rbacv1.Subject{{
			Kind:      rbacv1.ServiceAccountKind,
			Name:      workspace.ServiceAccountName,
			Namespace: namespaceFor(us),
		}},
		RoleRef: rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: clusterRoleKind, Name: editClusterRole},
	}
}
