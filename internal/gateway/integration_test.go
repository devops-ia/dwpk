package gateway

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	dwpkv1alpha1 "github.com/devops-ia/dwpk/api/v1alpha1"
	"golang.org/x/crypto/ssh"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

// These tests stop at authentication and Pod target resolution. envtest runs a
// real API server, but no kubelet, so pods/exec and pods/portforward transport
// cannot succeed end-to-end here.
var (
	integrationEnv             *envtest.Environment
	integrationScheme          *runtime.Scheme
	integrationRESTConfig      *rest.Config
	integrationWorkspaceClient ctrlclient.Client
	integrationKubeClient      kubernetes.Interface
)

func TestMain(m *testing.M) {
	integrationScheme = runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(integrationScheme))
	utilruntime.Must(dwpkv1alpha1.AddToScheme(integrationScheme))

	integrationEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}
	if dir := firstEnvTestBinaryDir(); dir != "" {
		integrationEnv.BinaryAssetsDirectory = dir
	}

	var err error
	integrationRESTConfig, err = integrationEnv.Start()
	if err != nil {
		fmt.Fprintf(os.Stderr, "start envtest: %v\n", err)
		os.Exit(1)
	}

	integrationWorkspaceClient, err = ctrlclient.New(integrationRESTConfig, ctrlclient.Options{Scheme: integrationScheme})
	if err != nil {
		fmt.Fprintf(os.Stderr, "build controller-runtime client: %v\n", err)
		_ = integrationEnv.Stop()
		os.Exit(1)
	}

	integrationKubeClient, err = kubernetes.NewForConfig(integrationRESTConfig)
	if err != nil {
		fmt.Fprintf(os.Stderr, "build kubernetes clientset: %v\n", err)
		_ = integrationEnv.Stop()
		os.Exit(1)
	}

	code := m.Run()
	if err := integrationEnv.Stop(); err != nil {
		fmt.Fprintf(os.Stderr, "stop envtest: %v\n", err)
		os.Exit(1)
	}
	os.Exit(code)
}

func TestServerPublicKeyCallbackWithEnvtest(t *testing.T) {
	ctx := context.Background()
	sharedLine, sharedKey := newIntegrationAuthorizedKey(t)
	aliceLine, _ := newIntegrationAuthorizedKey(t)
	bobLine, bobKey := newIntegrationAuthorizedKey(t)

	t.Run("scopes shared key to requested workspace name", func(t *testing.T) {
		fixture := newIntegrationFixture(t, ctx, "pubkey-scope")
		fixture.createWorkspace(t, ctx, "alice", fixture.primaryNamespace.Name, sharedLine)
		fixture.createWorkspace(t, ctx, "bob", fixture.secondaryNamespace.Name, sharedLine)

		server := fixture.server(t)
		permissions, err := server.publicKeyCallback(
			integrationConnMetadata{user: fixture.primaryNamespace.Name + "-alice"}, sharedKey)
		if err != nil {
			t.Fatalf("publicKeyCallback() error = %v", err)
		}

		got, err := workspaceKeyFromPermissions(permissions)
		if err != nil {
			t.Fatalf("workspaceKeyFromPermissions() error = %v", err)
		}
		want := fixture.primaryNamespace.Name + "/alice"
		if got.String() != want {
			t.Fatalf("workspace key = %q, want %q", got.String(), want)
		}
	})

	t.Run("rejects key from another workspace", func(t *testing.T) {
		fixture := newIntegrationFixture(t, ctx, "pubkey-reject")
		fixture.createWorkspace(t, ctx, "alice", fixture.primaryNamespace.Name, aliceLine)
		fixture.createWorkspace(t, ctx, "bob", fixture.secondaryNamespace.Name, bobLine)

		server := fixture.server(t)
		user := fixture.primaryNamespace.Name + "-alice"
		_, err := server.publicKeyCallback(integrationConnMetadata{user: user}, bobKey)
		if err == nil {
			t.Fatal("publicKeyCallback() error = nil, want mismatch error")
		}
		if !strings.Contains(err.Error(), "no Workspace named "+strconv.Quote(user)+" matched public key") {
			t.Fatalf("publicKeyCallback() error = %q, want key mismatch error", err)
		}
	})

	// The same workspace name in two namespaces was previously unresolvable:
	// the SSH username was the bare name and matched both. Each now carries its
	// owner, so both are reachable and neither is ambiguous.
	t.Run("same workspace name in two namespaces resolves to the right one", func(t *testing.T) {
		fixture := newIntegrationFixture(t, ctx, "pubkey-duplicate")
		fixture.createWorkspace(t, ctx, "dev", fixture.primaryNamespace.Name, sharedLine)
		fixture.createWorkspace(t, ctx, "dev", fixture.secondaryNamespace.Name, sharedLine)

		server := fixture.server(t)
		for _, namespace := range []string{fixture.primaryNamespace.Name, fixture.secondaryNamespace.Name} {
			permissions, err := server.publicKeyCallback(
				integrationConnMetadata{user: namespace + "-dev"}, sharedKey)
			if err != nil {
				t.Fatalf("publicKeyCallback(%s-dev) error = %v", namespace, err)
			}
			got, err := workspaceKeyFromPermissions(permissions)
			if err != nil {
				t.Fatalf("workspaceKeyFromPermissions() error = %v", err)
			}
			if got.Namespace != namespace || got.Name != "dev" {
				t.Fatalf("resolved %s, want %s/dev", got, namespace)
			}
		}
	})
}

func TestServerResolvePodTargetWithEnvtest(t *testing.T) {
	ctx := context.Background()

	t.Run("uses authenticated workspace namespace only", func(t *testing.T) {
		fixture := newIntegrationFixture(t, ctx, "pod-scope")
		workspace := fixture.createWorkspace(t, ctx, "alice", fixture.primaryNamespace.Name, fixture.authorizedKey)
		workspace.Status.PodName = "alice-0"
		fixture.createPod(t, ctx, "alice", fixture.primaryNamespace.Name, "alice-0")
		fixture.createPod(t, ctx, "alice", fixture.secondaryNamespace.Name, "alice-0")

		got, err := fixture.server(t).resolvePodTarget(ctx, workspace)
		if err != nil {
			t.Fatalf("resolvePodTarget() error = %v", err)
		}
		want := podTarget{Namespace: fixture.primaryNamespace.Name, Name: "alice-0"}
		if got != want {
			t.Fatalf("resolvePodTarget() = %#v, want %#v", got, want)
		}
	})

	t.Run("fails instead of crossing namespaces", func(t *testing.T) {
		fixture := newIntegrationFixture(t, ctx, "pod-cross-namespace")
		workspace := fixture.createWorkspace(t, ctx, "alice", fixture.primaryNamespace.Name, fixture.authorizedKey)
		workspace.Status.PodName = "alice-0"
		fixture.createPod(t, ctx, "alice", fixture.secondaryNamespace.Name, "alice-0")

		_, err := fixture.server(t).resolvePodTarget(ctx, workspace)
		if err == nil {
			t.Fatal("resolvePodTarget() error = nil, want namespaced not found error")
		}
		want := fmt.Sprintf("get Pod %s/alice-0", fixture.primaryNamespace.Name)
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("resolvePodTarget() error = %q, want substring %q", err, want)
		}
	})

	t.Run("rejects pod with wrong workspace label", func(t *testing.T) {
		fixture := newIntegrationFixture(t, ctx, "pod-label")
		workspace := fixture.createWorkspace(t, ctx, "alice", fixture.primaryNamespace.Name, fixture.authorizedKey)
		workspace.Status.PodName = "alice-0"
		fixture.createMislabeledPod(t, ctx, fixture.primaryNamespace.Name, "alice-0", "bob")

		_, err := fixture.server(t).resolvePodTarget(ctx, workspace)
		if err == nil {
			t.Fatal("resolvePodTarget() error = nil, want label mismatch error")
		}
		if !strings.Contains(err.Error(), `is not labeled for Workspace "alice"`) {
			t.Fatalf("resolvePodTarget() error = %q, want label mismatch error", err)
		}
	})
}

type integrationFixture struct {
	primaryNamespace   *corev1.Namespace
	secondaryNamespace *corev1.Namespace
	authorizedKey      string
	imageName          string
}

func newIntegrationFixture(t *testing.T, ctx context.Context, prefix string) integrationFixture {
	t.Helper()

	primaryNamespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: prefix + "-a"}}
	if err := integrationWorkspaceClient.Create(ctx, primaryNamespace); err != nil {
		t.Fatalf("Create(namespace %s) error = %v", primaryNamespace.Name, err)
	}

	secondaryNamespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: prefix + "-b"}}
	if err := integrationWorkspaceClient.Create(ctx, secondaryNamespace); err != nil {
		t.Fatalf("Create(namespace %s) error = %v", secondaryNamespace.Name, err)
	}

	fixture := integrationFixture{
		primaryNamespace:   primaryNamespace,
		secondaryNamespace: secondaryNamespace,
		imageName:          prefix + "-image",
	}
	fixture.authorizedKey, _ = newIntegrationAuthorizedKey(t)

	fixture.createUserSpace(t, ctx, prefix+"-user", primaryNamespace.Name)
	fixture.createUserSpace(t, ctx, prefix+"-other-user", secondaryNamespace.Name)
	fixture.createWorkspaceImage(t, ctx, fixture.imageName)

	return fixture
}

func (f integrationFixture) server(t *testing.T) *Server {
	t.Helper()
	return &Server{
		WorkspaceClient: integrationWorkspaceClient,
		KubeClient:      integrationKubeClient,
		RESTConfig:      integrationRESTConfig,
	}
}

func (f integrationFixture) createUserSpace(t *testing.T, ctx context.Context, name, namespace string) {
	t.Helper()
	userSpace := &dwpkv1alpha1.UserSpace{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: dwpkv1alpha1.UserSpaceSpec{
			Owner: name + "@example.com",
			Quota: dwpkv1alpha1.UserSpaceQuota{
				CPU:        resource.MustParse("2"),
				Memory:     resource.MustParse("4Gi"),
				Storage:    resource.MustParse("20Gi"),
				Workspaces: 1,
			},
			NetworkPolicy: dwpkv1alpha1.NetworkPolicyIsolated,
		},
	}
	if err := integrationWorkspaceClient.Create(ctx, userSpace); err != nil {
		t.Fatalf("Create(UserSpace %s) error = %v", name, err)
	}

	latest := userSpace.DeepCopy()
	latest.Status.Namespace = namespace
	latest.Status.State = dwpkv1alpha1.UserSpaceStateReady
	if err := integrationWorkspaceClient.Status().Update(ctx, latest); err != nil {
		t.Fatalf("Status().Update(UserSpace %s) error = %v", name, err)
	}
}

func (f integrationFixture) createWorkspaceImage(t *testing.T, ctx context.Context, name string) {
	t.Helper()
	image := &dwpkv1alpha1.WorkspaceImage{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: dwpkv1alpha1.WorkspaceImageSpec{
			DisplayName: name,
			Image:       "example.invalid/workspace:latest",
		},
	}
	if err := integrationWorkspaceClient.Create(ctx, image); err != nil {
		t.Fatalf("Create(WorkspaceImage %s) error = %v", name, err)
	}
}

func (f integrationFixture) createWorkspace(t *testing.T, ctx context.Context, name, namespace string, keys ...string) *dwpkv1alpha1.Workspace {
	t.Helper()
	workspace := &dwpkv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: dwpkv1alpha1.WorkspaceSpec{
			ImageRef:          dwpkv1alpha1.WorkspaceImageReference{Name: f.imageName},
			SSHAuthorizedKeys: keys,
			Running:           true,
		},
	}
	if err := integrationWorkspaceClient.Create(ctx, workspace); err != nil {
		t.Fatalf("Create(Workspace %s/%s) error = %v", namespace, name, err)
	}
	return workspace
}

func (f integrationFixture) createPod(t *testing.T, ctx context.Context, workspaceName, namespace, podName string) {
	t.Helper()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: namespace,
			Labels: map[string]string{
				dwpkv1alpha1.WorkspaceLabel: workspaceName,
			},
		},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "workspace", Image: "example.invalid/workspace:latest"}}},
	}
	if _, err := integrationKubeClient.CoreV1().Pods(namespace).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		t.Fatalf("Create(Pod %s/%s) error = %v", namespace, podName, err)
	}
}

func (f integrationFixture) createMislabeledPod(t *testing.T, ctx context.Context, namespace, podName, labelValue string) {
	t.Helper()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: namespace,
			Labels: map[string]string{
				dwpkv1alpha1.WorkspaceLabel: labelValue,
			},
		},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "workspace", Image: "example.invalid/workspace:latest"}}},
	}
	if _, err := integrationKubeClient.CoreV1().Pods(namespace).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		t.Fatalf("Create(Pod %s/%s) error = %v", namespace, podName, err)
	}
}

func newIntegrationAuthorizedKey(t *testing.T) (string, ssh.PublicKey) {
	t.Helper()

	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}

	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatalf("NewSignerFromKey() error = %v", err)
	}

	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey()))), signer.PublicKey()
}

func firstEnvTestBinaryDir() string {
	basePath := filepath.Join("..", "..", "bin", "k8s")
	entries, err := os.ReadDir(basePath)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if entry.IsDir() {
			return filepath.Join(basePath, entry.Name())
		}
	}
	return ""
}

type integrationConnMetadata struct {
	user string
}

func (m integrationConnMetadata) User() string { return m.user }
func (integrationConnMetadata) SessionID() []byte {
	return nil
}
func (integrationConnMetadata) ClientVersion() []byte { return nil }
func (integrationConnMetadata) ServerVersion() []byte { return nil }
func (integrationConnMetadata) RemoteAddr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 22}
}
func (integrationConnMetadata) LocalAddr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 2222}
}
