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

package webhook_test

import (
	"time"

	"context"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	dwpkv1alpha1 "github.com/devops-ia/dwpk/api/v1alpha1"
)

// Ordered, because the last spec takes the webhook server down and nothing
// after it would mean anything.
var _ = Describe("Workspace admission", Ordered, func() {
	const (
		userNS        = "dwpk-alice"
		systemNS      = "dwpk-system"
		imageName     = "python-3-12"
		rootImageName = "root-image"
	)

	// A real key, generated with ssh-keygen. The placeholder that used to live
	// here was never valid - it passed only while the platform checked the
	// prefix and never parsed the blob, which is the gap this change closes.
	//
	// Spelled out rather than shared with sshkeys_fixtures_test.go because this
	// suite is package webhook_test and that file is package webhook.
	testKey := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIBzqLX1fugjIfLRPWOBujokgUYuEODsP4SjjOgSqP4cc alice@laptop"

	ctx := context.Background()

	// One name per spec: a Workspace left over from the previous one changes
	// the quota count and would make the next assertion a coincidence.
	var (
		specCount int
		name      string
	)

	newWorkspace := func(namespace string) *dwpkv1alpha1.Workspace {
		return &dwpkv1alpha1.Workspace{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec: dwpkv1alpha1.WorkspaceSpec{
				ImageRef:          dwpkv1alpha1.WorkspaceImageReference{Name: imageName},
				SSHAuthorizedKeys: []string{testKey},
				Running:           true,
			},
		}
	}

	BeforeAll(func() {
		for _, ns := range []string{userNS, systemNS} {
			Expect(client.IgnoreAlreadyExists(k8sClient.Create(ctx, &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{Name: ns},
			}))).To(Succeed())
		}

		img := &dwpkv1alpha1.WorkspaceImage{
			ObjectMeta: metav1.ObjectMeta{Name: imageName},
			Spec: dwpkv1alpha1.WorkspaceImageSpec{
				DisplayName: "Python 3.12",
				Image:       "example.com/python:3.12",
				HomePath:    "/home/dev",
				RunAsUser:   1000,
			},
		}
		Expect(k8sClient.Create(ctx, img)).To(Succeed())

		rootImg := &dwpkv1alpha1.WorkspaceImage{
			ObjectMeta: metav1.ObjectMeta{Name: rootImageName},
			Spec: dwpkv1alpha1.WorkspaceImageSpec{
				DisplayName: "Root image",
				Image:       "example.com/root:1",
				HomePath:    "/home/dev",
				AllowRoot:   true,
			},
		}
		Expect(k8sClient.Create(ctx, rootImg)).To(Succeed())

		us := &dwpkv1alpha1.UserSpace{
			ObjectMeta: metav1.ObjectMeta{Name: "alice"},
			Spec: dwpkv1alpha1.UserSpaceSpec{
				Owner:         requester,
				NetworkPolicy: dwpkv1alpha1.NetworkPolicyIsolated,
				Quota: dwpkv1alpha1.UserSpaceQuota{
					CPU:        resource.MustParse("8"),
					Memory:     resource.MustParse("32Gi"),
					Storage:    resource.MustParse("100Gi"),
					Workspaces: 1,
				},
			},
		}
		Expect(k8sClient.Create(ctx, us)).To(Succeed())

		// The webhook finds the UserSpace by the namespace it reconciled, which
		// only the controller writes.
		us.Status.Namespace = userNS
		us.Status.State = dwpkv1alpha1.UserSpaceStateReady
		Expect(k8sClient.Status().Update(ctx, us)).To(Succeed())
	})

	BeforeEach(func() {
		specCount++
		name = fmt.Sprintf("ws-%d", specCount)
	})

	It("defaults storage from the CRD", func() {
		ws := newWorkspace(userNS)
		Expect(k8sClient.Create(ctx, ws)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(ctx, ws)).To(Succeed()) })

		Expect(ws.Spec.Storage).NotTo(BeNil())
		Expect(ws.Spec.Storage.String()).To(Equal("10Gi"))
	})

	It("stamps the requester, which is visible only at admission", func() {
		ws := newWorkspace(userNS)
		Expect(k8sClient.Create(ctx, ws)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(ctx, ws)).To(Succeed()) })

		Expect(ws.Annotations).To(HaveKeyWithValue(dwpkv1alpha1.RequesterAnnotation, requester))
	})

	It("rejects a dangling imageRef rather than admitting a Workspace that will sit in Failed", func() {
		ws := newWorkspace(userNS)
		ws.Spec.ImageRef.Name = "no-such-image"

		err := k8sClient.Create(ctx, ws)
		Expect(err).To(HaveOccurred())
		GinkgoWriter.Println(err.Error())
		// The validator reports it as a field error now that the mutator no
		// longer reads the image: nothing was left for it to default from once
		// sizes and storage went, and a create should not fail because the
		// catalog was briefly unreadable.
		Expect(err.Error()).To(ContainSubstring("spec.imageRef.name"))
		Expect(err.Error()).To(ContainSubstring(`"no-such-image"`))
	})

	It("rejects a second RUNNING workspace past the quota", func() {
		first := newWorkspace(userNS)
		Expect(k8sClient.Create(ctx, first)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(ctx, first)).To(Succeed()) })

		name += "-second"
		err := k8sClient.Create(ctx, newWorkspace(userNS))
		Expect(err).To(HaveOccurred())
		GinkgoWriter.Println(err.Error())
		Expect(apierrors.IsForbidden(err)).To(BeTrue())
		Expect(err.Error()).To(ContainSubstring("allows 1 running workspace(s)"))
	})

	// The point of counting running ones: a stopped workspace is a directory,
	// not a machine, so having one must not stop you making another. Its PVC
	// still counts against storage, which is what bounds them.
	It("accepts a stopped workspace beyond the running quota", func() {
		first := newWorkspace(userNS)
		Expect(k8sClient.Create(ctx, first)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(ctx, first)).To(Succeed()) })

		name += "-stopped"
		stopped := newWorkspace(userNS)
		stopped.Spec.Running = false
		Expect(k8sClient.Create(ctx, stopped)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(ctx, stopped)).To(Succeed()) })
	})

	// A scheduled date is deprecation without anybody having to remember to
	// press the button, so it has to refuse creates the same way the flag does.
	It("rejects an entry whose deprecation date has passed", func() {
		expired := &dwpkv1alpha1.WorkspaceImage{
			ObjectMeta: metav1.ObjectMeta{Name: "expired-image"},
			Spec: dwpkv1alpha1.WorkspaceImageSpec{
				DisplayName: "Expired",
				Image:       "example.com/expired:1",
				HomePath:    "/home/dev",
				RunAsUser:   1000,
				DeprecateAt: &metav1.Time{Time: time.Now().Add(-24 * time.Hour)},
			},
		}
		Expect(k8sClient.Create(ctx, expired)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(ctx, expired)).To(Succeed()) })

		ws := newWorkspace(userNS)
		ws.Spec.ImageRef.Name = expired.Name
		err := k8sClient.Create(ctx, ws)
		Expect(err).To(HaveOccurred())
		Expect(apierrors.IsForbidden(err)).To(BeTrue())
		Expect(err.Error()).To(ContainSubstring("deprecated"))
	})

	// Kubernetes refuses this at the StatefulSet, which means the Workspace
	// admits and then sits Degraded with the reason on a condition. Refusing it
	// here puts the message on the form instead.
	It("refuses a request larger than its own limit", func() {
		ws := newWorkspace(userNS)
		ws.Spec.Resources = corev1.ResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4")},
			Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")},
		}
		err := k8sClient.Create(ctx, ws)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("more than the limit"))
	})

	// hostPath mounts the node's filesystem, and /var/lib/kubelet holds every
	// other pod's projected secrets. One mount is every credential on the node
	// - for every tenant sharing it, not just this one's own container - so
	// there is no catalog entry that makes it safe to allow.
	It("refuses a hostPath volume", func() {
		ws := newWorkspace(userNS)
		ws.Spec.Volumes = []corev1.Volume{{
			Name:         "node",
			VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/"}},
		}}
		err := k8sClient.Create(ctx, ws)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("hostPath"))
	})

	It("refuses a hostPath volume even when the entry allows root", func() {
		ws := newWorkspace(userNS)
		ws.Spec.ImageRef.Name = rootImageName
		ws.Spec.Volumes = []corev1.Volume{{
			Name:         "node",
			VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/var/lib/kubelet/pods"}},
		}}
		err := k8sClient.Create(ctx, ws)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("hostPath"))
	})

	It("accepts a secret volume, which lives in the user's own namespace", func() {
		ws := newWorkspace(userNS)
		ws.Spec.Volumes = []corev1.Volume{{
			Name:         "creds",
			VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "x"}},
		}}
		Expect(k8sClient.Create(ctx, ws)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(ctx, ws)).To(Succeed()) })
	})

	// Shadowing home would detach the home directory from the volume that makes
	// stopping non-destructive.
	It("refuses a volume named home", func() {
		ws := newWorkspace(userNS)
		ws.Spec.Volumes = []corev1.Volume{{
			Name:         "home",
			VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
		}}
		err := k8sClient.Create(ctx, ws)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("reserved"))
	})

	// SPEC §10: the webhook must not be able to wedge the namespace it runs in.
	// The one line that prevents it is the namespaceSelector, and it is the kind
	// of line that gets deleted during a cleanup because nothing appears to use it.
	Describe("with the webhook scaled to zero", Ordered, func() {
		BeforeAll(func() { webhookDown() })
		// Put it back. Ginkgo randomises the order of top-level containers, so
		// leaving the server down here fails whichever spec runs next - and
		// which one that is differs from run to run.
		AfterAll(func() { webhookUp() })

		It("still admits a pod in dwpk-system", func() {
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "deadlock-probe", Namespace: systemNS},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "c", Image: "example.com/pause:1"}},
				},
			}
			Expect(k8sClient.Create(ctx, pod)).To(Succeed())
		})

		It("still admits a Workspace in dwpk-system, which the selector excludes", func() {
			ws := newWorkspace(systemNS)
			storage := resource.MustParse("1Gi")
			ws.Spec.Storage = &storage
			Expect(k8sClient.Create(ctx, ws)).To(Succeed())
		})

		It("refuses writes in a governed namespace, because failurePolicy is Fail", func() {
			err := k8sClient.Create(ctx, newWorkspace(userNS))
			Expect(err).To(HaveOccurred())
			GinkgoWriter.Println(err.Error())
			Expect(err.Error()).To(ContainSubstring("failed to call webhook"))
		})
	})
})
