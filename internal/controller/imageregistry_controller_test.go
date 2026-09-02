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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	dwpkv1alpha1 "github.com/devops-ia/dwpk/api/v1alpha1"
	"github.com/devops-ia/dwpk/internal/registry"
)

// fakeProvider is the registry.Provider stand-in every spec below injects via
// ProviderFactory - no AWS credentials, no network, matching how
// TestServerResolvePodTargetSessionIsolation-style envtest specs fake
// everything outside the Kubernetes API.
type fakeProvider struct {
	images []registry.RemoteImage
	err    error
}

func (f fakeProvider) List(context.Context) ([]registry.RemoteImage, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.images, nil
}

var _ = Describe("ImageRegistry Controller", func() {
	const testKind = "ImageRegistry"

	var (
		specCount  int
		name       string
		key        types.NamespacedName
		reconciler *ImageRegistryReconciler
	)

	newRegistry := func() *dwpkv1alpha1.ImageRegistry {
		return &dwpkv1alpha1.ImageRegistry{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: dwpkv1alpha1.ImageRegistrySpec{
				Provider: dwpkv1alpha1.ImageRegistryProviderAWSECR,
				AWS:      &dwpkv1alpha1.AWSRegistry{Region: "eu-west-1"},
			},
		}
	}

	withProvider := func(p registry.Provider) {
		reconciler.ProviderFactory = func(context.Context, *dwpkv1alpha1.ImageRegistry) (registry.Provider, error) {
			return p, nil
		}
	}

	reconcileIt := func() reconcile.Result {
		GinkgoHelper()
		result, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		return result
	}

	getRegistry := func() *dwpkv1alpha1.ImageRegistry {
		GinkgoHelper()
		reg := &dwpkv1alpha1.ImageRegistry{}
		Expect(k8sClient.Get(ctx, key, reg)).To(Succeed())
		return reg
	}

	condition := func(reg *dwpkv1alpha1.ImageRegistry, kind string) *metav1.Condition {
		return apimeta.FindStatusCondition(reg.Status.Conditions, kind)
	}

	listSyncedImages := func() []dwpkv1alpha1.WorkspaceImage {
		GinkgoHelper()
		list := &dwpkv1alpha1.WorkspaceImageList{}
		Expect(k8sClient.List(ctx, list, client.MatchingLabels{dwpkv1alpha1.ImageRegistryLabel: name})).To(Succeed())
		return list.Items
	}

	BeforeEach(func() {
		specCount++
		name = fmt.Sprintf("ecr-%d", specCount)
		key = types.NamespacedName{Name: name}
		reconciler = &ImageRegistryReconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
		}
	})

	It("creates a WorkspaceImage per selected image, labelled and owned", func() {
		Expect(k8sClient.Create(ctx, newRegistry())).To(Succeed())
		withProvider(fakeProvider{images: []registry.RemoteImage{
			{Repository: "dwpk/python", Tag: "3.12", Reference: "example.com/dwpk/python:3.12", PushedAt: time.Now()},
		}})

		result := reconcileIt()
		Expect(result.RequeueAfter).To(Equal(time.Duration(dwpkv1alpha1.DefaultSyncIntervalSeconds) * time.Second))

		images := listSyncedImages()
		Expect(images).To(HaveLen(1))
		Expect(images[0].Spec.Image).To(Equal("example.com/dwpk/python:3.12"))
		Expect(images[0].OwnerReferences).To(HaveLen(1))
		Expect(images[0].OwnerReferences[0].Kind).To(Equal(testKind))

		reg := getRegistry()
		Expect(reg.Status.Images).To(Equal(int32(1)))
		Expect(reg.Status.LastSyncTime).NotTo(BeNil())
		Expect(condition(reg, "Ready").Status).To(Equal(metav1.ConditionTrue))
	})

	It("does not delete a vanished image unless prune is enabled", func() {
		reg := newRegistry()
		Expect(k8sClient.Create(ctx, reg)).To(Succeed())
		withProvider(fakeProvider{images: []registry.RemoteImage{
			{Repository: "dwpk/python", Tag: "3.12", Reference: "example.com/dwpk/python:3.12", PushedAt: time.Now()},
		}})
		reconcileIt()
		Expect(listSyncedImages()).To(HaveLen(1))

		By("the remote image disappearing, with prune left off")
		withProvider(fakeProvider{images: nil})
		reconcileIt()
		Expect(listSyncedImages()).To(HaveLen(1), "prune is off; a disappeared image must not be deleted")
	})

	It("prunes a vanished image once prune is enabled", func() {
		reg := newRegistry()
		reg.Spec.Sync.Prune = true
		Expect(k8sClient.Create(ctx, reg)).To(Succeed())
		withProvider(fakeProvider{images: []registry.RemoteImage{
			{Repository: "dwpk/python", Tag: "3.12", Reference: "example.com/dwpk/python:3.12", PushedAt: time.Now()},
		}})
		reconcileIt()
		Expect(listSyncedImages()).To(HaveLen(1))

		withProvider(fakeProvider{images: nil})
		reconcileIt()
		Expect(listSyncedImages()).To(BeEmpty())
	})

	It("reports Degraded on a provider error without wiping the existing count", func() {
		Expect(k8sClient.Create(ctx, newRegistry())).To(Succeed())
		withProvider(fakeProvider{images: []registry.RemoteImage{
			{Repository: "dwpk/python", Tag: "3.12", Reference: "example.com/dwpk/python:3.12", PushedAt: time.Now()},
		}})
		reconcileIt()
		Expect(getRegistry().Status.Images).To(Equal(int32(1)))

		withProvider(fakeProvider{err: errors.New("connection refused")})
		// Not reconcileIt(): a provider error is expected to come back from
		// Reconcile itself too, so controller-runtime's rate limiter backs off -
		// see the Reconcile doc comment on why this controller returns the error
		// rather than swallowing it into the requeue delay.
		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).To(HaveOccurred())

		reg := getRegistry()
		Expect(condition(reg, "Ready").Status).To(Equal(metav1.ConditionFalse))
		Expect(condition(reg, "Degraded").Status).To(Equal(metav1.ConditionTrue))
		Expect(reg.Status.Images).To(Equal(int32(1)), "a failed sync must not erase the last known good count")
		Expect(listSyncedImages()).To(HaveLen(1), "a failed sync must not touch existing entries")
	})

	It("requeues after the configured interval, not the default", func() {
		reg := newRegistry()
		reg.Spec.Sync.IntervalSeconds = 120
		Expect(k8sClient.Create(ctx, reg)).To(Succeed())
		withProvider(fakeProvider{})

		result := reconcileIt()
		Expect(result.RequeueAfter).To(Equal(2 * time.Minute))
	})
})
