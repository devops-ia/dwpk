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
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	dwpkv1alpha1 "github.com/devops-ia/dwpk/api/v1alpha1"
	"github.com/devops-ia/dwpk/internal/workspace"
)

var _ = Describe("UserSpace Controller", func() {
	const owner = "alice@example.com"

	// A namespace deleted in envtest stays Terminating forever: there is no
	// namespace controller to finalize it. A fresh name per spec sidesteps that.
	var (
		specCount  int
		name       string
		ns         string
		reconciler *UserSpaceReconciler
	)

	newUserSpace := func() *dwpkv1alpha1.UserSpace {
		return &dwpkv1alpha1.UserSpace{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: dwpkv1alpha1.UserSpaceSpec{
				Owner: owner,
				Quota: dwpkv1alpha1.UserSpaceQuota{
					CPU:        resource.MustParse("8"),
					Memory:     resource.MustParse("32Gi"),
					Storage:    resource.MustParse("100Gi"),
					Workspaces: 1,
				},
				NetworkPolicy: "Isolated",
			},
		}
	}

	reconcileIt := func() {
		GinkgoHelper()
		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: name}})
		Expect(err).NotTo(HaveOccurred())
	}

	get := func(obj client.Object, key types.NamespacedName) error {
		return k8sClient.Get(ctx, key, obj)
	}

	BeforeEach(func() {
		specCount++
		name = fmt.Sprintf("alice-%d", specCount)
		ns = dwpkv1alpha1.NamespacePrefix + name
		reconciler = &UserSpaceReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
		Expect(k8sClient.Create(ctx, newUserSpace())).To(Succeed())
	})

	AfterEach(func() {
		us := &dwpkv1alpha1.UserSpace{}
		if err := get(us, types.NamespacedName{Name: name}); err == nil {
			Expect(k8sClient.Delete(ctx, us)).To(Succeed())
		}
		for _, obj := range []client.Object{
			&rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: clusterRolePrefix + name}},
			&rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: clusterRolePrefix + name}},
		} {
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, obj))).To(Succeed())
		}
	})

	It("creates every owned object", func() {
		reconcileIt()

		By("creating the namespace, owned by the UserSpace")
		nsObj := &corev1.Namespace{}
		Expect(get(nsObj, types.NamespacedName{Name: ns})).To(Succeed())
		Expect(nsObj.OwnerReferences).To(HaveLen(1))
		Expect(nsObj.OwnerReferences[0].Kind).To(Equal("UserSpace"))
		Expect(nsObj.OwnerReferences[0].Name).To(Equal(name))
		Expect(*nsObj.OwnerReferences[0].Controller).To(BeTrue())

		By("creating the quota from spec.quota")
		quota := &corev1.ResourceQuota{}
		Expect(get(quota, types.NamespacedName{Name: quotaName, Namespace: ns})).To(Succeed())
		Expect(quota.Spec.Hard).To(HaveKeyWithValue(corev1.ResourceRequestsCPU, resource.MustParse("8")))
		Expect(quota.Spec.Hard).To(HaveKeyWithValue(corev1.ResourceRequestsMemory, resource.MustParse("32Gi")))
		Expect(quota.Spec.Hard).To(HaveKeyWithValue(corev1.ResourceRequestsStorage, resource.MustParse("100Gi")))
		// No count/workspaces entry: the workspace limit counts RUNNING
		// workspaces, and a ResourceQuota counts objects. A hard limit that
		// disagrees with the message the user is shown is worse than none.
		Expect(quota.Spec.Hard).NotTo(HaveKey(corev1.ResourceName("count/workspaces.dwpk.devops-ia.io")))

		By("creating a LimitRange with default requests")
		lr := &corev1.LimitRange{}
		Expect(get(lr, types.NamespacedName{Name: limitRangeName, Namespace: ns})).To(Succeed())
		Expect(lr.Spec.Limits[0].DefaultRequest).To(HaveKey(corev1.ResourceCPU))

		By("creating the NetworkPolicy")
		np := &networkingv1.NetworkPolicy{}
		Expect(get(np, types.NamespacedName{Name: networkPolicyName, Namespace: ns})).To(Succeed())
		Expect(np.Spec.PolicyTypes).To(ConsistOf(networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress))
		Expect(np.Spec.Egress).To(HaveLen(4),
			"Isolated: own namespace, DNS, off-cluster, and the API server resolved from Endpoints")

		By("creating the workspace and session ServiceAccounts as separate identities")
		Expect(get(&corev1.ServiceAccount{}, types.NamespacedName{Name: workspace.ServiceAccountName, Namespace: ns})).To(Succeed())
		Expect(get(&corev1.ServiceAccount{}, types.NamespacedName{Name: workspace.SessionServiceAccountName, Namespace: ns})).To(Succeed())

		By("binding the owner and their browser session to a namespaced Role")
		rb := &rbacv1.RoleBinding{}
		Expect(get(rb, types.NamespacedName{Name: ownerBindingName, Namespace: ns})).To(Succeed())
		Expect(rb.Subjects).To(ConsistOf(
			rbacv1.Subject{APIGroup: rbacv1.GroupName, Kind: "User", Name: owner},
			rbacv1.Subject{Kind: "ServiceAccount", Name: workspace.SessionServiceAccountName, Namespace: ns},
		))
		Expect(rb.RoleRef.Kind).To(Equal("Role"))
		Expect(rb.RoleRef.Name).To(Equal(ownerRoleName))

		role := &rbacv1.Role{}
		Expect(get(role, types.NamespacedName{Name: ownerRoleName, Namespace: ns})).To(Succeed())
		Expect(role.Rules).To(ContainElement(HaveField("Resources", ConsistOf("pods", "pods/log"))))

		By("binding the workspace ServiceAccount to edit in this namespace only")
		editRB := &rbacv1.RoleBinding{}
		Expect(get(editRB, types.NamespacedName{Name: editBindingName, Namespace: ns})).To(Succeed())
		Expect(editRB.RoleRef).To(Equal(rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: clusterRoleKind, Name: editClusterRole}))
		// The session account must never appear here: `edit` is the pod's, and
		// a browser session sharing it would inherit whatever the pod can do.
		Expect(editRB.Subjects).To(ConsistOf(rbacv1.Subject{Kind: "ServiceAccount", Name: workspace.ServiceAccountName, Namespace: ns}))
	})

	It("grants the owner get-by-name on their UserSpace and never list", func() {
		reconcileIt()

		cr := &rbacv1.ClusterRole{}
		Expect(get(cr, types.NamespacedName{Name: clusterRolePrefix + name})).To(Succeed())

		var userspaceRule, imageRule *rbacv1.PolicyRule
		for i, rule := range cr.Rules {
			switch rule.Resources[0] {
			case "userspaces":
				userspaceRule = &cr.Rules[i]
			case "workspaceimages":
				imageRule = &cr.Rules[i]
			}
		}
		Expect(userspaceRule).NotTo(BeNil())
		Expect(userspaceRule.ResourceNames).To(ConsistOf(name), "RBAC cannot filter a list, so scope by name")
		Expect(userspaceRule.Verbs).NotTo(ContainElement("list"))
		Expect(imageRule).NotTo(BeNil())
		Expect(imageRule.Verbs).To(ContainElement("list"), "the catalog is readable by everyone")

		crb := &rbacv1.ClusterRoleBinding{}
		Expect(get(crb, types.NamespacedName{Name: clusterRolePrefix + name})).To(Succeed())
		By("binding the reader here too: this ClusterRole is read-only already")
		Expect(crb.Subjects).To(ConsistOf(
			rbacv1.Subject{APIGroup: rbacv1.GroupName, Kind: "User", Name: owner},
			rbacv1.Subject{Kind: "ServiceAccount", Name: workspace.SessionServiceAccountName, Namespace: ns},
			rbacv1.Subject{Kind: "ServiceAccount", Name: workspace.ReadOnlySessionServiceAccountName, Namespace: ns},
		))
	})

	It("reconciles a quota change onto the existing ResourceQuota", func() {
		reconcileIt()

		us := &dwpkv1alpha1.UserSpace{}
		Expect(get(us, types.NamespacedName{Name: name})).To(Succeed())
		us.Spec.Quota.CPU = resource.MustParse("16")
		us.Spec.Quota.Workspaces = 3
		Expect(k8sClient.Update(ctx, us)).To(Succeed())

		reconcileIt()

		quota := &corev1.ResourceQuota{}
		Expect(get(quota, types.NamespacedName{Name: quotaName, Namespace: ns})).To(Succeed())
		Expect(quota.Spec.Hard).To(HaveKeyWithValue(corev1.ResourceRequestsCPU, resource.MustParse("16")))
	})

	It("sets phase, namespace, observedGeneration and Ready", func() {
		reconcileIt()

		us := &dwpkv1alpha1.UserSpace{}
		Expect(get(us, types.NamespacedName{Name: name})).To(Succeed())
		Expect(us.Status.Namespace).To(Equal(ns))
		Expect(us.Status.State).To(Equal(dwpkv1alpha1.UserSpaceStateReady))
		Expect(us.Status.ObservedGeneration).To(Equal(us.Generation))

		ready := apimeta.FindStatusCondition(us.Status.Conditions, "Ready")
		Expect(ready).NotTo(BeNil())
		Expect(ready.Status).To(Equal(metav1.ConditionTrue))
		Expect(ready.ObservedGeneration).To(Equal(us.Generation))
		Expect(apimeta.IsStatusConditionTrue(us.Status.Conditions, "Degraded")).To(BeFalse())
	})

	It("moves observedGeneration forward when the spec changes", func() {
		reconcileIt()

		us := &dwpkv1alpha1.UserSpace{}
		Expect(get(us, types.NamespacedName{Name: name})).To(Succeed())
		firstGen := us.Generation

		us.Spec.Quota.Memory = resource.MustParse("64Gi")
		Expect(k8sClient.Update(ctx, us)).To(Succeed())
		reconcileIt()

		Expect(get(us, types.NamespacedName{Name: name})).To(Succeed())
		Expect(us.Generation).To(BeNumerically(">", firstGen))
		Expect(us.Status.ObservedGeneration).To(Equal(us.Generation))
		Expect(apimeta.FindStatusCondition(us.Status.Conditions, "Ready").ObservedGeneration).To(Equal(us.Generation))
	})

	It("restores a child object someone deleted", func() {
		reconcileIt()

		np := &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: networkPolicyName, Namespace: ns}}
		Expect(k8sClient.Delete(ctx, np)).To(Succeed())
		Expect(apierrors.IsNotFound(get(np, types.NamespacedName{Name: networkPolicyName, Namespace: ns}))).To(BeTrue())

		reconcileIt()
		Expect(get(np, types.NamespacedName{Name: networkPolicyName, Namespace: ns})).To(Succeed())
	})

	It("marks the namespace for deletion when the UserSpace goes", func() {
		reconcileIt()

		us := &dwpkv1alpha1.UserSpace{}
		Expect(get(us, types.NamespacedName{Name: name})).To(Succeed())
		Expect(k8sClient.Delete(ctx, us)).To(Succeed())

		// envtest runs no garbage collector, so the ownerReference is the whole
		// of what this controller contributes. Deletion itself is verified on a
		// real cluster; here we assert the reference that drives it.
		nsObj := &corev1.Namespace{}
		Expect(get(nsObj, types.NamespacedName{Name: ns})).To(Succeed())
		Expect(nsObj.OwnerReferences[0].UID).To(Equal(us.UID))
	})

	Context("pull secret replication", func() {
		const pullSecretNamespace = "default"

		labelledSecret := func(secretName string) *corev1.Secret {
			return &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      secretName,
					Namespace: pullSecretNamespace,
					Labels:    map[string]string{dwpkv1alpha1.PullSecretLabel: "true"},
				},
				Type: corev1.SecretTypeDockerConfigJson,
				Data: map[string][]byte{corev1.DockerConfigJsonKey: []byte(`{}`)},
			}
		}

		BeforeEach(func() {
			reconciler.PullSecretNamespace = pullSecretNamespace
		})

		AfterEach(func() {
			secrets := &corev1.SecretList{}
			Expect(k8sClient.List(ctx, secrets, client.InNamespace(pullSecretNamespace),
				client.MatchingLabels{dwpkv1alpha1.PullSecretLabel: "true"})).To(Succeed())
			for i := range secrets.Items {
				Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, &secrets.Items[i]))).To(Succeed())
			}
		})

		It("mirrors a labelled source Secret into the user namespace", func() {
			secretName := "ecr-pull-" + name
			Expect(k8sClient.Create(ctx, labelledSecret(secretName))).To(Succeed())

			reconcileIt()

			mirrored := &corev1.Secret{}
			Expect(get(mirrored, types.NamespacedName{Name: secretName, Namespace: ns})).To(Succeed())
			Expect(mirrored.Type).To(Equal(corev1.SecretTypeDockerConfigJson))
			Expect(mirrored.Data).To(Equal(map[string][]byte{corev1.DockerConfigJsonKey: []byte(`{}`)}))
		})

		It("does not mirror an unlabelled Secret", func() {
			secretName := "plain-" + name
			plain := labelledSecret(secretName)
			plain.Labels = nil
			Expect(k8sClient.Create(ctx, plain)).To(Succeed())

			reconcileIt()

			mirrored := &corev1.Secret{}
			Expect(apierrors.IsNotFound(get(mirrored, types.NamespacedName{Name: secretName, Namespace: ns}))).To(BeTrue())
		})
	})
})

var _ = Describe("UserSpace network policy postures", func() {
	apiServer := &networkingv1.NetworkPolicyEgressRule{
		To: []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{CIDR: "10.0.0.1/32"}}},
	}

	build := func(posture string) *networkingv1.NetworkPolicy {
		return buildNetworkPolicy(&dwpkv1alpha1.UserSpace{
			ObjectMeta: metav1.ObjectMeta{Name: "alice"},
			Spec:       dwpkv1alpha1.UserSpaceSpec{NetworkPolicy: posture},
		}, apiServer)
	}

	It("lets Isolated egress reach the namespace, DNS, the API server and off-cluster", func() {
		np := build(dwpkv1alpha1.NetworkPolicyIsolated)
		Expect(np.Spec.Egress).To(HaveLen(4))
		Expect(np.Spec.Egress[0].To[0].PodSelector).To(Equal(&metav1.LabelSelector{}))
		Expect(np.Spec.Egress[1].Ports).To(HaveLen(2))
		By("keeping other namespaces out of the off-cluster rule")
		Expect(np.Spec.Egress[2].To[0].IPBlock.Except).To(ContainElement("10.0.0.0/8"))
		By("requirement 7: kubectl from inside the pod needs the API server")
		Expect(np.Spec.Egress[3]).To(Equal(*apiServer))
	})

	It("still writes the policy when the API server address is unknown", func() {
		np := buildNetworkPolicy(&dwpkv1alpha1.UserSpace{
			ObjectMeta: metav1.ObjectMeta{Name: "alice"},
			Spec:       dwpkv1alpha1.UserSpaceSpec{NetworkPolicy: dwpkv1alpha1.NetworkPolicyIsolated},
		}, nil)
		Expect(np.Spec.Egress).To(HaveLen(3))
	})

	It("lets ClusterEgress out anywhere", func() {
		np := build(dwpkv1alpha1.NetworkPolicyClusterEgress)
		Expect(np.Spec.Egress).To(Equal([]networkingv1.NetworkPolicyEgressRule{{}}))
	})

	It("restricts ingress to the namespace either way", func() {
		for _, posture := range []string{
			dwpkv1alpha1.NetworkPolicyIsolated,
			dwpkv1alpha1.NetworkPolicyClusterEgress,
		} {
			np := build(posture)
			Expect(np.Spec.Ingress).To(HaveLen(1), posture)
			Expect(np.Spec.Ingress[0].From[0].NamespaceSelector).To(BeNil(), posture)
		}
	})
})
