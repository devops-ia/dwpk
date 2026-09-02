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
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	dwpkv1alpha1 "github.com/devops-ia/dwpk/api/v1alpha1"
	dwpkwebhook "github.com/devops-ia/dwpk/internal/webhook"
)

// requester is the user the tests write as, so the annotation the mutator
// stamps from request.userInfo has a value worth asserting on.
const requester = "alice@example.com"

var (
	testEnv   *envtest.Environment
	k8sClient client.Client

	// stopWebhook takes the webhook server down without touching the API
	// server. It is envtest's equivalent of scaling the Deployment to zero.
	stopWebhook context.CancelFunc
	webhookDown func()
	// webhookUp brings it back. Whichever spec takes the server down owes the
	// rest of the suite a restart: Ginkgo randomises top-level containers, so a
	// spec that leaves the server dead fails whatever happens to run next, and
	// which spec that is changes between runs.
	webhookUp func()

	// restConfig is kept so the server can be started more than once.
	restConfig *rest.Config
)

func TestWebhooks(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Webhook Suite")
}

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))
	Expect(dwpkv1alpha1.AddToScheme(scheme.Scheme)).To(Succeed())

	By("bootstrapping the API server with the webhook configuration we actually ship")
	testEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
		WebhookInstallOptions: envtest.WebhookInstallOptions{Paths: []string{renderWebhookConfig()}},
	}
	if dir := firstEnvTestBinaryDir(); dir != "" {
		testEnv.BinaryAssetsDirectory = dir
	}

	cfg, err := testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	restConfig = cfg

	// A named user rather than the admin cert: the requester stamp is only
	// meaningful if the test knows who it is writing as.
	userCfg, err := testEnv.AddUser(envtest.User{Name: requester, Groups: []string{"system:masters"}}, nil)
	Expect(err).NotTo(HaveOccurred())

	k8sClient, err = client.New(userCfg.Config(), client.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred())

	startWebhookServer(cfg)
})

var _ = AfterSuite(func() {
	if stopWebhook != nil {
		stopWebhook()
	}
	Eventually(testEnv.Stop, time.Minute, time.Second).Should(Succeed())
})

// startWebhookServer runs the real manager wiring against the certificates
// envtest generated, so the API server reaches these handlers over TLS exactly
// as it would in a cluster.
func startWebhookServer(cfg *rest.Config) {
	opts := testEnv.WebhookInstallOptions
	mgrCtx, cancel := context.WithCancel(context.Background())
	stopWebhook = cancel

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:  scheme.Scheme,
		Metrics: metricsserver.Options{BindAddress: "0"},
		WebhookServer: webhook.NewServer(webhook.Options{
			Host:    opts.LocalServingHost,
			Port:    opts.LocalServingPort,
			CertDir: opts.LocalServingCertDir,
		}),
	})
	Expect(err).NotTo(HaveOccurred())
	Expect(dwpkwebhook.SetupWorkspaceWebhookWithManager(mgr)).To(Succeed())
	Expect(dwpkwebhook.SetupUserSpaceWebhookWithManager(mgr)).To(Succeed())

	go func() {
		defer GinkgoRecover()
		Expect(mgr.Start(mgrCtx)).To(Succeed())
	}()

	addr := net.JoinHostPort(opts.LocalServingHost, fmt.Sprint(opts.LocalServingPort))
	Eventually(func() error {
		conn, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true}) // #nosec G402 -- test client
		if err != nil {
			return err
		}
		return conn.Close()
	}, 20*time.Second).Should(Succeed())

	webhookUp = func() { startWebhookServer(restConfig) }

	webhookDown = func() {
		cancel()
		Eventually(func() error {
			conn, err := net.DialTimeout("tcp", addr, time.Second)
			if err != nil {
				return nil
			}
			_ = conn.Close()
			return fmt.Errorf("webhook server still accepting connections")
		}, 20*time.Second).Should(Succeed())
	}
}

// renderWebhookConfig builds config/webhook so the test loads the same
// configuration the cluster gets - including the namespaceSelector, which is a
// kustomize patch and therefore absent from the controller-gen output.
func renderWebhookConfig() string {
	kustomize := os.Getenv("KUSTOMIZE")
	if kustomize == "" {
		kustomize = filepath.Join("..", "..", "bin", "kustomize")
	}
	out, err := exec.Command(kustomize, "build", filepath.Join("..", "..", "config", "webhook")).Output()
	Expect(err).NotTo(HaveOccurred(), "run `make kustomize` if this fails")

	dir := GinkgoT().TempDir()
	path := filepath.Join(dir, "webhooks.yaml")
	Expect(os.WriteFile(path, out, 0o600)).To(Succeed())
	return path
}

func firstEnvTestBinaryDir() string {
	base := filepath.Join("..", "..", "bin", "k8s")
	entries, err := os.ReadDir(base)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.IsDir() {
			return filepath.Join(base, e.Name())
		}
	}
	return ""
}
