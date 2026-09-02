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
	"bytes"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
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

// testGitSSHPrivateKeyFixture is a throwaway ed25519 key, generated solely as
// test fixture data - never used for anything real. Its actual content is
// irrelevant here: this suite only checks that whatever ciphertext went in
// comes back out byte-identical after a real AES round trip.
const testGitSSHPrivateKeyFixture = `-----BEGIN OPENSSH PRIVATE KEY-----
b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAAMwAAAAtzc2gtZW
QyNTUxOQAAACA0+IXikeIJqEjmYudirdq1HUEXASDvI7tm1OdIkKbbYAAAAIhU6eGcVOnh
nAAAAAtzc2gtZWQyNTUxOQAAACA0+IXikeIJqEjmYudirdq1HUEXASDvI7tm1OdIkKbbYA
AAAEANvqJTeC2BJa0qhR4y2FqY76ooaBp9Go5ZzW5GvtTLHDT4heKR4gmoSOZi52Kt2rUd
QRcBIO8ju2bU50iQpttgAAAABHRlc3QB
-----END OPENSSH PRIVATE KEY-----
`

// sizeSmall is the first size every test image offers, so it is also the one
// the mutating webhook would default to.

var _ = Describe("Workspace Controller", func() {
	const (
		gatewayHost = "dwpk.example.com"
		testNS      = "default"
		testKey     = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExampleKeyForTests0000000000 test@envtest"
	)

	// One fresh name per spec: envtest keeps every object created by the last
	// one, and a leftover StatefulSet would make the next assertion a lie.
	var (
		specCount  int
		name       string
		imageName  string
		key        types.NamespacedName
		reconciler *WorkspaceReconciler
	)

	newImage := func() *dwpkv1alpha1.WorkspaceImage {
		return &dwpkv1alpha1.WorkspaceImage{
			ObjectMeta: metav1.ObjectMeta{Name: imageName},
			Spec: dwpkv1alpha1.WorkspaceImageSpec{
				DisplayName: "Python 3.12",
				Image:       "example.com/python:3.12",
				HomePath:    "/home/dev",
				RunAsUser:   1000,
				Placement: &dwpkv1alpha1.WorkspacePlacement{
					NodeSelector: map[string]string{"workload": "worker"},
				},
			},
		}
	}

	newWorkspace := func() *dwpkv1alpha1.Workspace {
		storage := resource.MustParse("5Gi")
		return &dwpkv1alpha1.Workspace{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNS},
			Spec: dwpkv1alpha1.WorkspaceSpec{
				ImageRef: dwpkv1alpha1.WorkspaceImageReference{Name: imageName},
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m")},
				},
				Storage:           &storage,
				SSHAuthorizedKeys: []string{testKey},
				Running:           true,
			},
		}
	}

	reconcileIt := func() {
		GinkgoHelper()
		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
	}

	getWorkspace := func() *dwpkv1alpha1.Workspace {
		GinkgoHelper()
		ws := &dwpkv1alpha1.Workspace{}
		Expect(k8sClient.Get(ctx, key, ws)).To(Succeed())
		return ws
	}

	getStatefulSet := func() *appsv1.StatefulSet {
		GinkgoHelper()
		sts := &appsv1.StatefulSet{}
		Expect(k8sClient.Get(ctx, key, sts)).To(Succeed())
		return sts
	}

	condition := func(ws *dwpkv1alpha1.Workspace, kind string) *metav1.Condition {
		return apimeta.FindStatusCondition(ws.Status.Conditions, kind)
	}

	BeforeEach(func() {
		specCount++
		name = fmt.Sprintf("dev-%d", specCount)
		imageName = fmt.Sprintf("python-%d", specCount)
		key = types.NamespacedName{Name: name, Namespace: testNS}
		reconciler = &WorkspaceReconciler{
			Client:                       k8sClient,
			Scheme:                       k8sClient.Scheme(),
			GatewayHost:                  gatewayHost,
			GitSSHEncryptionKeyNamespace: testNS,
		}
	})

	Context("when the referenced WorkspaceImage does not exist", func() {
		It("reports ImageNotFound without requeueing, and recovers when the image appears", func() {
			Expect(k8sClient.Create(ctx, newWorkspace())).To(Succeed())

			result, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
			By("not asking for a timed requeue: the WorkspaceImage watch retriggers instead")
			Expect(result.RequeueAfter).To(BeZero())

			ws := getWorkspace()
			Expect(ws.Status.State).To(Equal(dwpkv1alpha1.WorkspaceStatePending))
			Expect(condition(ws, conditionReady).Status).To(Equal(metav1.ConditionFalse))
			Expect(condition(ws, conditionReady).Reason).To(Equal("ImageNotFound"))
			Expect(condition(ws, conditionImageResolved).Status).To(Equal(metav1.ConditionFalse))
			Expect(k8sClient.Get(ctx, key, &appsv1.StatefulSet{})).To(MatchError(apierrors.IsNotFound, "not found"))

			By("mapping a later WorkspaceImage create back to this Workspace")
			img := newImage()
			Expect(k8sClient.Create(ctx, img)).To(Succeed())
			Expect(reconciler.workspacesUsingImage(ctx, img)).To(ContainElement(reconcile.Request{NamespacedName: key}))

			reconcileIt()
			Expect(getStatefulSet().Spec.Template.Spec.Containers[0].Image).To(Equal(img.Spec.Image))
			Expect(condition(getWorkspace(), conditionImageResolved).Status).To(Equal(metav1.ConditionTrue))
		})
	})

	// There is no spec for "storage was never defaulted" any more. The CRD
	// defaults it, so the API server will not accept a Workspace without one and
	// the state is unreachable through admission. markStorageUnset stays as a
	// guard against a Go-constructed object reaching the builder, because a nil
	// dereference in a reconciler kills a process holding a lease.

	Context("when the image resolves", func() {
		BeforeEach(func() {
			Expect(k8sClient.Create(ctx, newImage())).To(Succeed())
			Expect(k8sClient.Create(ctx, newWorkspace())).To(Succeed())
			reconcileIt()
		})

		It("creates a headless Service the StatefulSet can use for DNS", func() {
			svc := &corev1.Service{}
			Expect(k8sClient.Get(ctx, key, svc)).To(Succeed())
			Expect(svc.Spec.ClusterIP).To(Equal(corev1.ClusterIPNone))
			Expect(svc.Spec.Selector).To(HaveKeyWithValue(dwpkv1alpha1.WorkspaceLabel, name))
			Expect(svc.OwnerReferences).To(HaveLen(1))
		})

		It("creates a StatefulSet running as the workspace ServiceAccount", func() {
			sts := getStatefulSet()
			Expect(sts.Labels).To(HaveKeyWithValue(dwpkv1alpha1.WorkspaceLabel, name))
			Expect(sts.Spec.ServiceName).To(Equal(name))
			Expect(*sts.Spec.Replicas).To(BeEquivalentTo(1))

			pod := sts.Spec.Template.Spec
			Expect(pod.ServiceAccountName).To(Equal(workspace.ServiceAccountName))
			Expect(*pod.SecurityContext.RunAsUser).To(BeEquivalentTo(1000))
			Expect(pod.NodeSelector).To(HaveKeyWithValue("workload", "worker"))
			Expect(pod.Containers[0].VolumeMounts[0].MountPath).To(Equal("/home/dev"))
			Expect(pod.Containers[0].Resources.Requests.Cpu().String()).To(Equal("500m"))
			By("taking the command from the image, defaulted by the CRD to an idle process")
			Expect(pod.Containers[0].Command).To(Equal([]string{"sleep", "infinity"}))

			vct := sts.Spec.VolumeClaimTemplates[0]
			Expect(vct.Name).To(Equal(workspace.HomeVolumeName))
			storage := vct.Spec.Resources.Requests[corev1.ResourceStorage]
			Expect(storage.String()).To(Equal("5Gi"))
		})

		It("reports Starting until the pod is ready, then Running", func() {
			ws := getWorkspace()
			Expect(ws.Status.State).To(Equal(dwpkv1alpha1.WorkspaceStateStarting))
			Expect(ws.Status.PodName).To(Equal(name + "-0"))
			// <user>-<workspace>, so two people can both call one "dev" and the
			// gateway can still tell them apart. testNS carries no dwpk- prefix,
			// so the user half is the namespace as it stands.
			Expect(ws.Status.Endpoint).To(Equal(testNS + "-" + name + "@" + gatewayHost))
			Expect(ws.Status.ObservedGeneration).To(Equal(ws.Generation))

			By("standing in for the StatefulSet controller, which envtest does not run")
			sts := getStatefulSet()
			sts.Status.Replicas = 1
			sts.Status.ReadyReplicas = 1
			Expect(k8sClient.Status().Update(ctx, sts)).To(Succeed())

			reconcileIt()
			ws = getWorkspace()
			Expect(ws.Status.State).To(Equal(dwpkv1alpha1.WorkspaceStateRunning))
			Expect(condition(ws, conditionReady).Status).To(Equal(metav1.ConditionTrue))
			Expect(condition(ws, conditionReady).Reason).To(Equal("PodRunning"))
			Expect(condition(ws, conditionDegraded).Status).To(Equal(metav1.ConditionFalse))
		})

		It("advances observedGeneration when the spec changes", func() {
			first := getWorkspace()
			first.Spec.Resources.Requests[corev1.ResourceCPU] = resource.MustParse("2")
			Expect(k8sClient.Update(ctx, first)).To(Succeed())
			reconcileIt()

			ws := getWorkspace()
			Expect(ws.Generation).To(BeNumerically(">", first.Status.ObservedGeneration))
			Expect(ws.Status.ObservedGeneration).To(Equal(ws.Generation))
			Expect(getStatefulSet().Spec.Template.Spec.Containers[0].Resources.Requests.Cpu().String()).To(Equal("2"))
		})

		It("suspends by scaling to zero, keeps the home PVC, and resumes", func() {
			By("standing in for the StatefulSet controller's volumeClaimTemplate PVC")
			pvc := &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("%s-%s-0", workspace.HomeVolumeName, name),
					Namespace: testNS,
				},
				Spec: corev1.PersistentVolumeClaimSpec{
					AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					Resources: corev1.VolumeResourceRequirements{
						Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("5Gi")},
					},
				},
			}
			Expect(k8sClient.Create(ctx, pvc)).To(Succeed())
			pvcKey := client.ObjectKeyFromObject(pvc)

			ws := getWorkspace()
			ws.Spec.Running = false
			Expect(k8sClient.Update(ctx, ws)).To(Succeed())
			reconcileIt()

			Expect(*getStatefulSet().Spec.Replicas).To(BeEquivalentTo(0))
			By("keeping the StatefulSet itself: suspend is a replica count, not a delete")
			Expect(k8sClient.Get(ctx, key, &appsv1.StatefulSet{})).To(Succeed())
			By("keeping the home PVC, which is what makes suspend non-destructive")
			Expect(k8sClient.Get(ctx, pvcKey, &corev1.PersistentVolumeClaim{})).To(Succeed())

			ws = getWorkspace()
			Expect(ws.Status.State).To(Equal(dwpkv1alpha1.WorkspaceStateSuspended))
			Expect(ws.Status.PodName).To(BeEmpty())
			Expect(condition(ws, conditionReady).Reason).To(Equal("Suspended"))

			By("resuming")
			ws.Spec.Running = true
			Expect(k8sClient.Update(ctx, ws)).To(Succeed())
			reconcileIt()

			Expect(*getStatefulSet().Spec.Replicas).To(BeEquivalentTo(1))
			Expect(k8sClient.Get(ctx, pvcKey, &corev1.PersistentVolumeClaim{})).To(Succeed())
			Expect(getWorkspace().Status.State).To(Equal(dwpkv1alpha1.WorkspaceStateStarting))
		})

		It("rejects a change to spec.storage, so the immutable volumeClaimTemplate stays valid", func() {
			ws := getWorkspace()
			bigger := resource.MustParse("10Gi")
			ws.Spec.Storage = &bigger
			Expect(k8sClient.Update(ctx, ws)).To(MatchError(ContainSubstring("storage is immutable")))
		})

		It("has no git-ssh mount when the user has configured no keys", func() {
			pod := getStatefulSet().Spec.Template.Spec
			for _, m := range pod.Containers[0].VolumeMounts {
				Expect(m.MountPath).NotTo(Equal(dwpkv1alpha1.GitSSHMountPath))
			}
			for _, v := range pod.Volumes {
				Expect(v.Name).NotTo(Equal("git-ssh-keys"))
			}
		})

		Context("with a git-ssh encryption key seeded", func() {
			var masterKey []byte

			BeforeEach(func() {
				masterKey = bytes.Repeat([]byte{0x37}, 32)
				// The Secret name is a fixed constant (the whole point: every
				// namespace's runtime Secret decrypts against the one same
				// key), so unlike a Workspace/Image name this cannot be made
				// unique per spec - get-or-create instead of create, since
				// envtest keeps every object a previous spec in this Context
				// already made.
				existing := &corev1.Secret{}
				err := k8sClient.Get(ctx, types.NamespacedName{Namespace: testNS, Name: dwpkv1alpha1.GitSSHEncryptionKeySecretName}, existing)
				if apierrors.IsNotFound(err) {
					Expect(k8sClient.Create(ctx, &corev1.Secret{
						ObjectMeta: metav1.ObjectMeta{Name: dwpkv1alpha1.GitSSHEncryptionKeySecretName, Namespace: testNS},
						Data:       map[string][]byte{dwpkv1alpha1.GitSSHEncryptionKeySecretDataKey: masterKey},
					})).To(Succeed())
				} else {
					Expect(err).NotTo(HaveOccurred())
					masterKey = existing.Data[dwpkv1alpha1.GitSSHEncryptionKeySecretDataKey]
				}
			})

			It("mounts the decrypted key, not the ciphertext, and unmounts once the source Secret is removed", func() {
				plaintext := []byte(testGitSSHPrivateKeyFixture)
				ciphertext, err := workspace.EncryptGitSSHKey(masterKey, plaintext)
				Expect(err).NotTo(HaveOccurred())

				secret := &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{Name: dwpkv1alpha1.GitSSHKeysSecretName, Namespace: testNS},
					Data: map[string][]byte{
						"key-github.com":  ciphertext,
						"meta-github.com": []byte("ssh-ed25519 SHA256:fake"),
						"config":          []byte("dummy"),
					},
				}
				Expect(k8sClient.Create(ctx, secret)).To(Succeed())

				By("mapping the Secret's creation back to every Workspace in its namespace")
				Expect(reconciler.workspacesInGitSSHKeysSecretNamespace(ctx, secret)).
					To(ContainElement(reconcile.Request{NamespacedName: key}))

				reconcileIt()
				pod := getStatefulSet().Spec.Template.Spec
				var mountedVolume string
				for _, m := range pod.Containers[0].VolumeMounts {
					if m.MountPath == dwpkv1alpha1.GitSSHMountPath {
						Expect(m.ReadOnly).To(BeTrue())
						mountedVolume = m.Name
					}
				}
				Expect(mountedVolume).NotTo(BeEmpty())
				var mountedSecretName string
				for _, v := range pod.Volumes {
					if v.Name == mountedVolume {
						mountedSecretName = v.Secret.SecretName
					}
				}
				Expect(mountedSecretName).To(Equal(dwpkv1alpha1.GitSSHKeysRuntimeSecretName),
					"the pod must mount the controller's decrypted runtime Secret, never the ciphertext source")

				runtime := &corev1.Secret{}
				Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: testNS, Name: dwpkv1alpha1.GitSSHKeysRuntimeSecretName}, runtime)).To(Succeed())
				Expect(runtime.Data["key-github.com"]).To(Equal(plaintext))

				var hasEnv bool
				for _, e := range pod.Containers[0].Env {
					if e.Name == "GIT_SSH_COMMAND" {
						hasEnv = true
					}
				}
				Expect(hasEnv).To(BeTrue())

				Expect(k8sClient.Delete(ctx, secret)).To(Succeed())
				reconcileIt()
				pod = getStatefulSet().Spec.Template.Spec
				for _, m := range pod.Containers[0].VolumeMounts {
					Expect(m.MountPath).NotTo(Equal(dwpkv1alpha1.GitSSHMountPath))
				}
				Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: testNS, Name: dwpkv1alpha1.GitSSHKeysRuntimeSecretName}, &corev1.Secret{})).
					To(MatchError(apierrors.IsNotFound, "not found"))
			})
		})

		It("degrades rather than silently mounting nothing when keys exist but the encryption key does not", func() {
			reconciler.GitSSHEncryptionKeyNamespace = "does-not-exist"
			Expect(k8sClient.Create(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: dwpkv1alpha1.GitSSHKeysSecretName, Namespace: testNS},
				Data:       map[string][]byte{"key-github.com": []byte("ciphertext"), "config": []byte("dummy")},
			})).To(Succeed())

			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).To(HaveOccurred())

			ws := getWorkspace()
			Expect(condition(ws, conditionDegraded).Status).To(Equal(metav1.ConditionTrue))
		})
	})
})
