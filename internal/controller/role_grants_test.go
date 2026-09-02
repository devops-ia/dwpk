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

	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	dwpkv1alpha1 "github.com/devops-ia/dwpk/api/v1alpha1"
	"github.com/devops-ia/dwpk/internal/workspace"
)

var _ = Describe("UserSpace role grants", func() {
	const adminRole = "test-userspace-admin-role"

	var (
		grantCount int
		name       string
		reconciler *UserSpaceReconciler
	)

	newUser := func(role string) *dwpkv1alpha1.UserSpace {
		return &dwpkv1alpha1.UserSpace{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: dwpkv1alpha1.UserSpaceSpec{
				Owner:         name + "@example.com",
				Role:          role,
				NetworkPolicy: dwpkv1alpha1.NetworkPolicyIsolated,
				Quota: dwpkv1alpha1.UserSpaceQuota{
					CPU:        resource.MustParse("1"),
					Memory:     resource.MustParse("1Gi"),
					Storage:    resource.MustParse("1Gi"),
					Workspaces: 1,
				},
			},
		}
	}

	reconcileIt := func() {
		GinkgoHelper()
		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: name}})
		Expect(err).NotTo(HaveOccurred())
	}

	BeforeEach(func() {
		grantCount++
		name = fmt.Sprintf("role-%d", grantCount)
		reconciler = &UserSpaceReconciler{
			Client:            k8sClient,
			Scheme:            k8sClient.Scheme(),
			AdminClusterRoles: []string{adminRole},
		}
	})

	AfterEach(func() {
		us := &dwpkv1alpha1.UserSpace{ObjectMeta: metav1.ObjectMeta{Name: name}}
		Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, us))).To(Succeed())
		for _, obj := range []client.Object{
			&rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: clusterRolePrefix + name}},
			&rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: clusterRolePrefix + name}},
			&rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: managerRolePrefix + name}},
			&rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: managerRolePrefix + name}},
			&rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: adminBindingPrefix + adminRole + "-" + name}},
		} {
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, obj))).To(Succeed())
		}
	})

	It("binds the admin ClusterRoles to the session account, and withdraws them on demotion", func() {
		Expect(k8sClient.Create(ctx, newUser(dwpkv1alpha1.UserSpaceRoleAdmin))).To(Succeed())
		reconcileIt()

		binding := &rbacv1.ClusterRoleBinding{}
		key := types.NamespacedName{Name: adminBindingPrefix + adminRole + "-" + name}
		Expect(k8sClient.Get(ctx, key, binding)).To(Succeed())
		Expect(binding.RoleRef.Name).To(Equal(adminRole))

		// The session account, never the one a workspace pod runs as.
		Expect(binding.Subjects).To(ConsistOf(rbacv1.Subject{
			Kind:      serviceAccountKind,
			Name:      workspace.SessionServiceAccountName,
			Namespace: dwpkv1alpha1.NamespacePrefix + name,
		}))

		By("removing the binding when the role is taken away")
		us := &dwpkv1alpha1.UserSpace{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name}, us)).To(Succeed())
		us.Spec.Role = dwpkv1alpha1.UserSpaceRoleUser
		Expect(k8sClient.Update(ctx, us)).To(Succeed())

		reconcileIt()

		err := k8sClient.Get(ctx, key, binding)
		Expect(client.IgnoreNotFound(err)).To(Succeed())
		Expect(err).To(HaveOccurred(), "a demoted administrator kept cluster-wide rights")
	})

	// Withdrawal used to re-derive the names it would delete from the current
	// AdminClusterRoles, so a role dropped from that list left a binding nothing
	// could ever name again. One survived on a live cluster pointing at a
	// deleted ClusterRole, which turned every `kubectl auth can-i` for that
	// account into a missing-role error.
	It("withdraws a binding whose ClusterRole is no longer granted", func() {
		Expect(k8sClient.Create(ctx, newUser(dwpkv1alpha1.UserSpaceRoleAdmin))).To(Succeed())
		reconcileIt()

		stale := adminBindingPrefix + "test-retired-role-" + name
		DeferCleanup(func() {
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, &rbacv1.ClusterRoleBinding{
				ObjectMeta: metav1.ObjectMeta{Name: stale},
			}))).To(Succeed())
		})
		Expect(k8sClient.Create(ctx, &rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:   stale,
				Labels: map[string]string{userSpaceLabel: name},
			},
			Subjects: []rbacv1.Subject{{
				Kind:      serviceAccountKind,
				Name:      workspace.SessionServiceAccountName,
				Namespace: dwpkv1alpha1.NamespacePrefix + name,
			}},
			RoleRef: rbacv1.RoleRef{
				APIGroup: rbacv1.GroupName,
				Kind:     clusterRoleKind,
				Name:     "test-retired-role",
			},
		})).To(Succeed())

		reconcileIt()

		err := k8sClient.Get(ctx, types.NamespacedName{Name: stale}, &rbacv1.ClusterRoleBinding{})
		Expect(client.IgnoreNotFound(err)).To(Succeed())
		Expect(err).To(HaveOccurred(), "a binding to a retired ClusterRole survived")

		// The sweep is scoped by name prefix because the owner's own binding
		// carries the same label. Deleting that one would cost the user their
		// catalog and their own UserSpace.
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: clusterRolePrefix + name},
			&rbacv1.ClusterRoleBinding{})).To(Succeed(), "the sweep took the owner binding with it")
	})

})
