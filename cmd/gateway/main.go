package main

import (
	"flag"
	"os"

	_ "k8s.io/client-go/plugin/pkg/client/auth"

	dwpkv1alpha1 "github.com/devops-ia/dwpk/api/v1alpha1"
	"github.com/devops-ia/dwpk/internal/gateway"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	crconfig "sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

// version is stamped at build time with -ldflags "-X main.version=...".
var version = "dev"

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(dwpkv1alpha1.AddToScheme(scheme))
}

func main() {
	var listenAddress string
	var hostKeyPath string

	opts := zap.Options{Development: true}
	flag.StringVar(&listenAddress, "listen-address", ":2222", "The address the SSH gateway listens on.")
	flag.StringVar(&hostKeyPath, "host-key-path", "hack/spike-ssh/hostkey.pem",
		"Path to the SSH host private key. Created if absent.")
	// The "kubeconfig" flag itself is registered by crconfig's init(); reuse
	// it instead of redefining, which would panic ("flag redefined").
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	config, err := crconfig.GetConfig()
	if err != nil {
		setupLog.Error(err, "Failed to build Kubernetes config")
		os.Exit(1)
	}

	workspaceClient, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		setupLog.Error(err, "Failed to create controller-runtime client")
		os.Exit(1)
	}
	kubeClient, err := kubernetes.NewForConfig(config)
	if err != nil {
		setupLog.Error(err, "Failed to create Kubernetes clientset")
		os.Exit(1)
	}
	HostSigner, err := gateway.LoadOrCreateHostKey(hostKeyPath)
	if err != nil {
		setupLog.Error(err, "Failed to load SSH host key", "path", hostKeyPath)
		os.Exit(1)
	}

	server := gateway.NewServer(workspaceClient, kubeClient, config, HostSigner)
	setupLog.Info("Starting gateway", "version", version, "listenAddress", listenAddress)
	if err := server.ListenAndServe(ctrl.SetupSignalHandler(), listenAddress); err != nil {
		setupLog.Error(err, "Failed to run gateway")
		os.Exit(1)
	}
}
