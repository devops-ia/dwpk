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
	"fmt"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	lbIP         = "192.168.139.2"
	lbHostname   = "nlb.internal.example.com"
	lbInternalIP = "10.0.0.7"
)

// loadBalancerHost is pure, so it gets a table rather than a cluster.
func TestLoadBalancerHost(t *testing.T) {
	t.Parallel()

	ingress := func(in ...corev1.LoadBalancerIngress) *corev1.Service {
		return &corev1.Service{
			Status: corev1.ServiceStatus{
				LoadBalancer: corev1.LoadBalancerStatus{Ingress: in},
			},
		}
	}

	for _, tc := range []struct {
		name    string
		service *corev1.Service
		want    string
	}{
		{
			name:    "no ingress at all is no address",
			service: ingress(),
			want:    "",
		},
		{
			name:    "an IP is used when that is all there is",
			service: ingress(corev1.LoadBalancerIngress{IP: lbIP}),
			want:    lbIP,
		},
		{
			name:    "a hostname is used when that is all there is",
			service: ingress(corev1.LoadBalancerIngress{Hostname: lbHostname}),
			want:    lbHostname,
		},
		{
			// An internal AWS NLB publishes both on some setups. The hostname
			// outlives the address behind it, so it is the one to publish.
			name: "a hostname beats an IP on the same ingress",
			service: ingress(corev1.LoadBalancerIngress{
				IP:       lbInternalIP,
				Hostname: lbHostname,
			}),
			want: lbHostname,
		},
		{
			// The hostname is preferred even when an IP-only entry is listed
			// first, so ordering in the slice cannot change the answer.
			name: "a later hostname still beats an earlier IP",
			service: ingress(
				corev1.LoadBalancerIngress{IP: lbInternalIP},
				corev1.LoadBalancerIngress{Hostname: lbHostname},
			),
			want: lbHostname,
		},
		{
			name: "an empty ingress entry is skipped rather than returned",
			service: ingress(
				corev1.LoadBalancerIngress{},
				corev1.LoadBalancerIngress{IP: lbIP},
			),
			want: lbIP,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := loadBalancerHost(tc.service); got != tc.want {
				t.Fatalf("loadBalancerHost() = %q, want %q", got, tc.want)
			}
		})
	}
}

// resolveGatewayHost reads a Service, so it is exercised against a real API
// server rather than a mock, per the project's testing rules.
var _ = Describe("Gateway host resolution", func() {
	const (
		fallbackHost = "dwpk-gateway.dwpk-system.svc.cluster.local"
		serviceNS    = "default"
	)

	var (
		ctx         context.Context
		svcCount    int
		serviceName string
		reconciler  *WorkspaceReconciler
	)

	BeforeEach(func() {
		ctx = context.Background()
		svcCount++
		serviceName = fmt.Sprintf("gw-resolve-%d", svcCount)
		reconciler = &WorkspaceReconciler{
			Client:      k8sClient,
			Scheme:      k8sClient.Scheme(),
			GatewayHost: fallbackHost,
		}
	})

	// envtest runs no cloud controller, so a LoadBalancer Service never gets an
	// address on its own. Writing status directly is the only way to represent
	// the state this function exists to read.
	createGatewayService := func(ingress ...corev1.LoadBalancerIngress) {
		GinkgoHelper()
		svc := &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: serviceName, Namespace: serviceNS},
			Spec: corev1.ServiceSpec{
				Type:  corev1.ServiceTypeLoadBalancer,
				Ports: []corev1.ServicePort{{Port: 22}},
			},
		}
		Expect(k8sClient.Create(ctx, svc)).To(Succeed())
		if len(ingress) > 0 {
			svc.Status.LoadBalancer.Ingress = ingress
			Expect(k8sClient.Status().Update(ctx, svc)).To(Succeed())
		}
	}

	Context("when no gateway Service is configured", func() {
		It("keeps the configured host, which is the behaviour of every existing deployment", func() {
			Expect(reconciler.resolveGatewayHost(ctx)).To(Equal(fallbackHost))
		})
	})

	Context("when the reference is malformed", func() {
		It("keeps the configured host rather than guessing a namespace", func() {
			reconciler.GatewayService = "no-slash-here"
			Expect(reconciler.resolveGatewayHost(ctx)).To(Equal(fallbackHost))

			reconciler.GatewayService = "/only-a-name"
			Expect(reconciler.resolveGatewayHost(ctx)).To(Equal(fallbackHost))

			reconciler.GatewayService = "only-a-namespace/"
			Expect(reconciler.resolveGatewayHost(ctx)).To(Equal(fallbackHost))
		})
	})

	Context("when the referenced Service does not exist", func() {
		It("keeps the configured host instead of failing the reconcile", func() {
			reconciler.GatewayService = serviceNS + "/does-not-exist"
			Expect(reconciler.resolveGatewayHost(ctx)).To(Equal(fallbackHost))
		})
	})

	Context("when the Service exists but has no address yet", func() {
		It("keeps the configured host until the LoadBalancer is provisioned", func() {
			createGatewayService()
			reconciler.GatewayService = serviceNS + "/" + serviceName
			Expect(reconciler.resolveGatewayHost(ctx)).To(Equal(fallbackHost))
		})
	})

	Context("when the LoadBalancer has an address", func() {
		It("publishes the address, because the internal DNS name is unreachable off-cluster", func() {
			createGatewayService(corev1.LoadBalancerIngress{IP: lbIP})
			reconciler.GatewayService = serviceNS + "/" + serviceName
			Expect(reconciler.resolveGatewayHost(ctx)).To(Equal(lbIP))
		})

		It("prefers a hostname, which is what an internal NLB publishes", func() {
			createGatewayService(corev1.LoadBalancerIngress{
				IP:       lbInternalIP,
				Hostname: lbHostname,
			})
			reconciler.GatewayService = serviceNS + "/" + serviceName
			Expect(reconciler.resolveGatewayHost(ctx)).To(Equal(lbHostname))
		})
	})
})
