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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	dwpkv1alpha1 "github.com/devops-ia/dwpk/api/v1alpha1"
)

// The lease the two managers contend for. Same values as cmd/manager (§5.3):
// the point of the test is that these settings hand over, not that some other
// settings would.
const (
	leaderElectionID = "dwpk-controller.dwpk.devops-ia.io"
	systemNamespace  = "dwpk-system"
)

var _ = Describe("Leader election", func() {
	const testNS = "default"

	// reconciled reports which manager acted, so "only one reconciles" is an
	// observation rather than an inference from the lease object.
	var reconciled chan string

	// startManager brings up one replica with leader election on. It returns
	// the cancel func because killing the leader is what the test is about.
	startManager := func(id string) context.CancelFunc {
		mgrCtx, cancel := context.WithCancel(context.Background())

		mgr, err := ctrl.NewManager(cfg, ctrl.Options{
			Scheme:                        k8sClient.Scheme(),
			Metrics:                       metricsserver.Options{BindAddress: "0"},
			LeaderElection:                true,
			LeaderElectionID:              leaderElectionID,
			LeaderElectionNamespace:       systemNamespace,
			LeaderElectionResourceLock:    resourcelock.LeasesResourceLock,
			LeaderElectionReleaseOnCancel: true,
			LeaseDuration:                 ptr.To(15 * time.Second),
			RenewDeadline:                 ptr.To(10 * time.Second),
			RetryPeriod:                   ptr.To(2 * time.Second),
		})
		Expect(err).NotTo(HaveOccurred())

		err = ctrl.NewControllerManagedBy(mgr).
			For(&dwpkv1alpha1.Workspace{}).
			Named("ha-" + id).
			Complete(reconcile.Func(func(context.Context, ctrl.Request) (ctrl.Result, error) {
				select {
				case reconciled <- id:
				default: // a full channel means the test already has its answer
				}
				return ctrl.Result{}, nil
			}))
		Expect(err).NotTo(HaveOccurred())

		go func() {
			defer GinkgoRecover()
			Expect(mgr.Start(mgrCtx)).To(Succeed())
		}()
		return cancel
	}

	// elected blocks until the manager holding the lease reports itself.
	elected := func() string {
		var got string
		Eventually(reconciled, 30*time.Second).Should(Receive(&got))
		return got
	}

	BeforeEach(func() {
		reconciled = make(chan string, 16)

		err := k8sClient.Create(ctx, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: systemNamespace},
		})
		if err != nil && !apierrors.IsAlreadyExists(err) {
			Expect(err).NotTo(HaveOccurred())
		}
	})

	It("runs one reconciler at a time and hands over when the leader dies", func() {
		By("creating a Workspace for the elected manager to reconcile")
		ws := &dwpkv1alpha1.Workspace{
			ObjectMeta: metav1.ObjectMeta{Name: "ha-probe", Namespace: testNS},
			Spec: dwpkv1alpha1.WorkspaceSpec{
				ImageRef:          dwpkv1alpha1.WorkspaceImageReference{Name: "any-image"},
				SSHAuthorizedKeys: []string{"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExampleKeyForTests0000000000 ha@envtest"},
				Running:           true,
			},
		}
		Expect(k8sClient.Create(ctx, ws)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(ctx, ws)).To(Succeed()) })

		cancelA := startManager("a")
		DeferCleanup(cancelA)
		cancelB := startManager("b")
		DeferCleanup(cancelB)

		leader := elected()

		By("asserting the standby stays silent while the leader holds the lease")
		Consistently(func() []string {
			var others []string
			for {
				select {
				case id := <-reconciled:
					if id != leader {
						others = append(others, id)
					}
				default:
					return others
				}
			}
		}, 5*time.Second, 500*time.Millisecond).Should(BeEmpty())

		By("killing the leader")
		if leader == "a" {
			cancelA()
		} else {
			cancelB()
		}
		killed := time.Now()

		By("asserting the standby takes over")
		var next string
		// Well inside the 15s lease: ReleaseOnCancel means the standby does not
		// wait it out. The bound is loose because CI is not a stopwatch; the
		// measured handover is logged below.
		Eventually(reconciled, 15*time.Second).Should(Receive(&next))
		for next == leader {
			Eventually(reconciled, 15*time.Second).Should(Receive(&next))
		}
		AddReportEntry("handover", time.Since(killed).String())
		GinkgoWriter.Printf("handover from %s to %s took %s\n", leader, next, time.Since(killed))
	})
})
