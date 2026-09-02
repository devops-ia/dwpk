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

package main

import (
	"context"
	"crypto/tls"
	"flag"
	"os"
	"strings"
	"time"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"

	dwpkv1alpha1 "github.com/devops-ia/dwpk/api/v1alpha1"
	"github.com/devops-ia/dwpk/internal/bootstrap"
	"github.com/devops-ia/dwpk/internal/controller"
	dwpkwebhook "github.com/devops-ia/dwpk/internal/webhook"
	// +kubebuilder:scaffold:imports
)

// leaderElectionNamespace holds the coordination.k8s.io Lease the replicas
// contend for (§5.3). Fixed rather than inferred: the manager may run with a
// kubeconfig from outside the cluster, where there is no namespace to infer.
const leaderElectionNamespace = "dwpk-system"

// version is stamped at build time with -ldflags "-X main.version=...". It is
// logged at startup so a support question about a running pod can be answered
// without guessing from the image tag, which anyone can retag.
var version = "dev"

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(dwpkv1alpha1.AddToScheme(scheme))
	// +kubebuilder:scaffold:scheme
}

// nolint:gocyclo
func main() {
	var metricsAddr string
	var metricsCertPath, metricsCertName, metricsCertKey string
	var webhookCertPath, webhookCertName, webhookCertKey string
	var enableLeaderElection bool
	var gatewayHost string
	var gatewayService string
	var probeAddr string
	var secureMetrics bool
	var enableHTTP2 bool
	var bootstrapAdminToken bool
	var bootstrapNamespace string
	var bootstrapServiceAccount string
	var adminClusterRoles string
	var pullSecretNamespace string
	var gitSSHEncryptionKeyNamespace string
	var tlsOpts []func(*tls.Config)
	flag.StringVar(&metricsAddr, "metrics-bind-address", "0", "The address the metrics endpoint binds to. "+
		"Use :8443 for HTTPS or :8080 for HTTP, or leave as 0 to disable the metrics service.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.StringVar(&gatewayHost, "gateway-host", "dwpk-gateway.dwpk-system.svc.cluster.local",
		"The SSH gateway hostname published in Workspace status.endpoint as <workspace>@<host>. "+
			"Set it to the DNS name your users reach the gateway on. "+
			"Used when -gateway-service is unset or has no LoadBalancer address.")
	flag.StringVar(&gatewayService, "gateway-service", "",
		"The gateway's own Service as <namespace>/<name>. When it has a LoadBalancer address "+
			"that address is published instead of -gateway-host, because a cluster-internal "+
			"DNS name is not reachable from the VPN users connect over. Empty disables the lookup.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.BoolVar(&secureMetrics, "metrics-secure", true,
		"If set, the metrics endpoint is served securely via HTTPS. Use --metrics-secure=false to use HTTP instead.")
	flag.StringVar(&webhookCertPath, "webhook-cert-path", "", "The directory that contains the webhook certificate.")
	flag.StringVar(&webhookCertName, "webhook-cert-name", "tls.crt", "The name of the webhook certificate file.")
	flag.StringVar(&webhookCertKey, "webhook-cert-key", "tls.key", "The name of the webhook key file.")
	flag.StringVar(&metricsCertPath, "metrics-cert-path", "",
		"The directory that contains the metrics server certificate.")
	flag.StringVar(&metricsCertName, "metrics-cert-name", "tls.crt", "The name of the metrics server certificate file.")
	flag.StringVar(&metricsCertKey, "metrics-cert-key", "tls.key", "The name of the metrics server key file.")
	flag.BoolVar(&enableHTTP2, "enable-http2", false,
		"If set, HTTP/2 will be enabled for the metrics and webhook servers")
	flag.BoolVar(&bootstrapAdminToken, "bootstrap-admin-token", false,
		"Issue the platform's local admin API token if none exists yet, then exit. "+
			"Run as a Helm post-install/post-upgrade hook Job (§7.7); does not start the controller manager.")
	flag.StringVar(&bootstrapNamespace, "bootstrap-namespace", "dwpk-system",
		"The namespace to store the admin token Secret in when --bootstrap-admin-token is set.")
	flag.StringVar(&bootstrapServiceAccount, "bootstrap-service-account", bootstrap.DefaultAdminServiceAccountName,
		"The ServiceAccount name the admin token authenticates as. Must match the ServiceAccount "+
			"Helm binds to the admin ClusterRoles.")
	flag.StringVar(&adminClusterRoles, "admin-cluster-roles", "",
		"Comma-separated ClusterRoles bound to a UserSpace whose role is \"administrator\". "+
			"Their names carry the Helm release prefix, so they are configuration rather than "+
			"constants; the manager must hold bind on exactly these names.")
	flag.StringVar(&pullSecretNamespace, "pull-secret-namespace", "dwpk-system",
		"Where an ImageRegistry's imagePullSecretRef Secret is expected to live. Every Secret "+
			"there labelled dwpk.devops-ia.io/pull-secret=true is mirrored into every user "+
			"namespace, so a private catalog image can be pulled.")
	flag.StringVar(&gitSSHEncryptionKeyNamespace, "git-ssh-encryption-key-namespace", "dwpk-system",
		"Where the dwpkv1alpha1.GitSSHEncryptionKeySecretName Secret lives - the AES key that "+
			"decrypts every user's git-ssh-keys Secret into the runtime Secret their workspaces "+
			"actually mount.")
	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	if bootstrapAdminToken {
		runAdminBootstrap(bootstrapNamespace, bootstrapServiceAccount)
		return
	}

	// if the enable-http2 flag is false (the default), http/2 should be disabled
	// due to its vulnerabilities. More specifically, disabling http/2 will
	// prevent from being vulnerable to the HTTP/2 Stream Cancellation and
	// Rapid Reset CVEs. For more information see:
	// - https://github.com/advisories/GHSA-qppj-fm5r-hxr3
	// - https://github.com/advisories/GHSA-4374-p667-p6c8
	disableHTTP2 := func(c *tls.Config) {
		setupLog.Info("Disabling HTTP/2")
		c.NextProtos = []string{"http/1.1"}
	}

	if !enableHTTP2 {
		tlsOpts = append(tlsOpts, disableHTTP2)
	}

	// Initial webhook TLS options
	webhookTLSOpts := tlsOpts
	webhookServerOptions := webhook.Options{
		TLSOpts: webhookTLSOpts,
	}

	if len(webhookCertPath) > 0 {
		setupLog.Info("Initializing webhook certificate watcher using provided certificates",
			"webhook-cert-path", webhookCertPath, "webhook-cert-name", webhookCertName, "webhook-cert-key", webhookCertKey)

		webhookServerOptions.CertDir = webhookCertPath
		webhookServerOptions.CertName = webhookCertName
		webhookServerOptions.KeyName = webhookCertKey
	}

	webhookServer := webhook.NewServer(webhookServerOptions)

	// Metrics endpoint is enabled in 'config/default/kustomization.yaml'. The Metrics options configure the server.
	// More info:
	// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.24.1/pkg/metrics/server
	// - https://book.kubebuilder.io/reference/metrics.html
	metricsServerOptions := metricsserver.Options{
		BindAddress:   metricsAddr,
		SecureServing: secureMetrics,
		TLSOpts:       tlsOpts,
	}

	if secureMetrics {
		// FilterProvider is used to protect the metrics endpoint with authn/authz.
		// These configurations ensure that only authorized users and service accounts
		// can access the metrics endpoint. The RBAC are configured in 'config/rbac/kustomization.yaml'. More info:
		// https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.24.1/pkg/metrics/filters#WithAuthenticationAndAuthorization
		metricsServerOptions.FilterProvider = filters.WithAuthenticationAndAuthorization
	}

	// If the certificate is not specified, controller-runtime will automatically
	// generate self-signed certificates for the metrics server. While convenient for development and testing,
	// this setup is not recommended for production.
	//
	// TODO(user): If you enable certManager, uncomment the following lines:
	// - [METRICS-WITH-CERTS] at config/default/kustomization.yaml to generate and use certificates
	// managed by cert-manager for the metrics server.
	// - [PROMETHEUS-WITH-CERTS] at config/prometheus/kustomization.yaml for TLS certification.
	if len(metricsCertPath) > 0 {
		setupLog.Info("Initializing metrics certificate watcher using provided certificates",
			"metrics-cert-path", metricsCertPath, "metrics-cert-name", metricsCertName, "metrics-cert-key", metricsCertKey)

		metricsServerOptions.CertDir = metricsCertPath
		metricsServerOptions.CertName = metricsCertName
		metricsServerOptions.KeyName = metricsCertKey
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                     scheme,
		Metrics:                    metricsServerOptions,
		WebhookServer:              webhookServer,
		HealthProbeBindAddress:     probeAddr,
		LeaderElection:             enableLeaderElection,
		LeaderElectionID:           "dwpk-controller.dwpk.devops-ia.io",
		LeaderElectionNamespace:    leaderElectionNamespace,
		LeaderElectionResourceLock: resourcelock.LeasesResourceLock,
		LeaseDuration:              ptr.To(15 * time.Second),
		RenewDeadline:              ptr.To(10 * time.Second),
		RetryPeriod:                ptr.To(2 * time.Second),
		// The process exits as soon as Start returns, so releasing the lease on
		// shutdown is safe - and it turns a planned restart into a ~1s handover
		// instead of the standby waiting out the full 15s lease (§5.3).
		LeaderElectionReleaseOnCancel: true,
	})
	if err != nil {
		setupLog.Error(err, "Failed to start manager")
		os.Exit(1)
	}

	if err := (&controller.UserSpaceReconciler{
		Client:              mgr.GetClient(),
		Scheme:              mgr.GetScheme(),
		AdminClusterRoles:   splitAndTrim(adminClusterRoles),
		PullSecretNamespace: pullSecretNamespace,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create controller", "controller", "userspace")
		os.Exit(1)
	}
	if err := (&controller.WorkspaceReconciler{
		Client:                       mgr.GetClient(),
		Scheme:                       mgr.GetScheme(),
		GatewayHost:                  gatewayHost,
		GatewayService:               gatewayService,
		GitSSHEncryptionKeyNamespace: gitSSHEncryptionKeyNamespace,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create controller", "controller", "workspace")
		os.Exit(1)
	}
	// The webhooks serve on every replica, not just the leader: admission has
	// to answer whichever pod the API server reaches (§7.5).
	if err := dwpkwebhook.SetupWorkspaceWebhookWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create webhook", "webhook", "workspace")
		os.Exit(1)
	}
	if err := dwpkwebhook.SetupUserSpaceWebhookWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create webhook", "webhook", "userspace")
		os.Exit(1)
	}
	if err := (&controller.ImageRegistryReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create controller", "controller", "imageregistry")
		os.Exit(1)
	}
	// +kubebuilder:scaffold:builder

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "Failed to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "Failed to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("Starting manager", "version", version)
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "Failed to run manager")
		os.Exit(1)
	}
}

// runAdminBootstrap provisions the platform's local admin identity - API
// token, UserSpace and login - then exits without starting the manager.
//
// It is a Helm post-install/post-upgrade hook Job rather than a pre- one
// because it creates a UserSpace: CRDs are ordinary chart resources, so at
// pre-install time the type does not exist yet and no controller is running to
// reconcile it. Both steps are idempotent, so every upgrade re-runs safely.
func runAdminBootstrap(namespace, serviceAccountName string) {
	kubeClient, err := ctrlclient.New(ctrl.GetConfigOrDie(), ctrlclient.Options{Scheme: scheme})
	if err != nil {
		setupLog.Error(err, "Failed to build Kubernetes client for admin bootstrap")
		os.Exit(1)
	}

	accountOpts := adminAccountOptionsFromEnv(namespace)

	// The token authenticates as the session ServiceAccount inside the admin's
	// own UserSpace namespace, which is the identity a browser login also
	// reaches. One admin identity, two ways to present it.
	created, err := bootstrap.AdminToken(context.Background(), kubeClient, bootstrap.AdminTokenOptions{
		StoreNamespace:        namespace,
		SubjectNamespace:      "dwpk-" + accountOpts.UserSpaceName,
		SubjectServiceAccount: serviceAccountName,
	})
	if err != nil {
		setupLog.Error(err, "Failed to bootstrap admin token")
		os.Exit(1)
	}

	// The account runs regardless of whether the token was newly issued: an
	// install that already had a token from an older chart still needs a login.
	runAdminAccountBootstrap(kubeClient, accountOpts)

	if !created {
		setupLog.Info("Admin token already provisioned, nothing to do", "namespace", namespace)
		return
	}

	setupLog.Info("Provisioned local admin token",
		"namespace", namespace,
		"secret", bootstrap.BootstrapSecretName,
		"instructions", "kubectl get secret "+bootstrap.BootstrapSecretName+" -n "+namespace+
			" -o jsonpath='{.data.token}' | base64 -d; then delete the secret")
}

// runAdminAccountBootstrap creates the admin login a person can actually use:
// a UserSpace, a local user, and the generated password. It runs in the same
// hook Job as the token bootstrap and is likewise idempotent.
func runAdminAccountBootstrap(kubeClient ctrlclient.Client, opts bootstrap.AdminAccountOptions) {
	result, err := bootstrap.AdminAccount(context.Background(), kubeClient, opts)
	if err != nil {
		setupLog.Error(err, "Failed to bootstrap admin account")
		os.Exit(1)
	}

	if !result.Created {
		setupLog.Info("Admin account already provisioned, nothing to do",
			"namespace", opts.Namespace, "username", opts.Username)
		return
	}
	if !result.PasswordGenerated {
		setupLog.Info("Provisioned local admin account with the configured password",
			"namespace", opts.Namespace, "username", opts.Username, "userspace", opts.UserSpaceName)
		return
	}

	setupLog.Info("Provisioned local admin account with a generated password",
		"namespace", opts.Namespace,
		"username", opts.Username,
		"userspace", opts.UserSpaceName,
		"secret", bootstrap.BootstrapSecretName,
		"instructions", "kubectl get secret "+bootstrap.BootstrapSecretName+" -n "+opts.Namespace+
			" -o jsonpath='{.data.password}' | base64 -d; then delete the secret")
}

// adminAccountOptionsFromEnv reads the DWPK__ADMIN_* settings the bootstrap
// Job is given. An unset password means "generate one".
func adminAccountOptionsFromEnv(namespace string) bootstrap.AdminAccountOptions {
	return bootstrap.AdminAccountOptions{
		Namespace:     namespace,
		UserSpaceName: envOrDefault("DWPK__ADMIN_USERSPACE", "admin"),
		Username:      envOrDefault("DWPK__ADMIN_USERNAME", "admin"),
		Email:         envOrDefault("DWPK__ADMIN_EMAIL", "admin@dwpk.local"),
		Password:      strings.TrimSpace(os.Getenv("DWPK__ADMIN_PASSWORD")),
	}
}

// splitAndTrim turns a comma-separated flag value into a clean list, dropping
// empties so a trailing comma or an unset flag yields nothing rather than a
// zero-length name the API server would reject.
func splitAndTrim(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func envOrDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
