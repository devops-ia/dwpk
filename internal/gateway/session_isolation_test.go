package gateway

import (
	"context"
	"net"
	"strings"
	"testing"

	dwpkv1alpha1 "github.com/devops-ia/dwpk/api/v1alpha1"
	workspacepkg "github.com/devops-ia/dwpk/internal/workspace"
	"golang.org/x/crypto/ssh"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	ctrlclientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestServerPublicKeyCallbackSessionIsolation(t *testing.T) {
	aliceLine, _ := newAuthorizedKey(t)
	bobLine, bobKey := newAuthorizedKey(t)
	sharedLine, sharedKey := newAuthorizedKey(t)

	tests := []struct {
		name       string
		user       string
		key        ssh.PublicKey
		workspaces []dwpkv1alpha1.Workspace
		wantKey    string
		wantErr    string
	}{
		{
			name: "scopes shared key to requested workspace",
			user: "alice-alice",
			key:  sharedKey,
			workspaces: []dwpkv1alpha1.Workspace{
				workspaceWithKeys("alice", "dwpk-alice", sharedLine),
				workspaceWithKeys("bob", "dwpk-bob", sharedLine),
			},
			wantKey: "dwpk-alice/alice",
		},
		{
			name: "fails cleanly when requested workspace does not trust key",
			user: "alice-alice",
			key:  bobKey,
			workspaces: []dwpkv1alpha1.Workspace{
				workspaceWithKeys("alice", "dwpk-alice", aliceLine),
				workspaceWithKeys("bob", "dwpk-bob", bobLine),
			},
			wantErr: `no Workspace named "alice-alice" matched public key`,
		},
		{
			// Two people each with a workspace called "dev" and one shared key
			// used to be an unresolvable collision: the SSH username was the bare
			// workspace name, so both matched. The identity now carries the owner,
			// so each is reachable and neither is ambiguous.
			name: "two workspaces of the same name in different namespaces stay distinct",
			user: "alice-dev",
			key:  sharedKey,
			workspaces: []dwpkv1alpha1.Workspace{
				workspaceWithKeys("dev", "dwpk-alice", sharedLine),
				workspaceWithKeys("dev", "dwpk-bob", sharedLine),
			},
			wantKey: "dwpk-alice/dev",
		},
		{
			name: "and the other one is reachable by its own identity",
			user: "bob-dev",
			key:  sharedKey,
			workspaces: []dwpkv1alpha1.Workspace{
				workspaceWithKeys("dev", "dwpk-alice", sharedLine),
				workspaceWithKeys("dev", "dwpk-bob", sharedLine),
			},
			wantKey: "dwpk-bob/dev",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newTestServer(t, tt.workspaces, nil)

			permissions, err := server.publicKeyCallback(testConnMetadata{user: tt.user}, tt.key)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("publicKeyCallback() error = nil, want substring %q", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("publicKeyCallback() error = %q, want substring %q", err.Error(), tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("publicKeyCallback() error = %v", err)
			}

			got, err := workspaceKeyFromPermissions(permissions)
			if err != nil {
				t.Fatalf("workspaceKeyFromPermissions() error = %v", err)
			}
			if got.String() != tt.wantKey {
				t.Fatalf("workspace key = %q, want %q", got.String(), tt.wantKey)
			}
		})
	}
}

func TestServerResolvePodTargetSessionIsolation(t *testing.T) {
	tests := []struct {
		name      string
		workspace dwpkv1alpha1.Workspace
		pods      []corev1.Pod
		want      podTarget
		wantErr   string
	}{
		{
			name:      "uses authenticated workspace namespace only",
			workspace: workspaceWithPod("alice", "dwpk-alice", "alice-0"),
			pods: []corev1.Pod{
				workspacePod("alice", "dwpk-alice", "alice-0"),
				workspacePod("alice", "dwpk-bob", "alice-0"),
			},
			want: podTarget{Namespace: "dwpk-alice", Name: "alice-0"},
		},
		{
			// Threaded straight off the Pod already fetched to resolve the
			// target, not a second lookup - see containerWorkingDir.
			name:      "carries the workspace container's WorkingDir as HomePath",
			workspace: workspaceWithPod("alice", "dwpk-alice", "alice-0"),
			pods: []corev1.Pod{
				workspacePodWithHome("alice", "dwpk-alice", "alice-0", "/home/dev"),
			},
			want: podTarget{Namespace: "dwpk-alice", Name: "alice-0", HomePath: "/home/dev"},
		},
		{
			name:      "fails instead of falling back to another namespace pod",
			workspace: workspaceWithPod("alice", "dwpk-alice", "alice-0"),
			pods: []corev1.Pod{
				workspacePod("alice", "dwpk-bob", "alice-0"),
			},
			wantErr: "get Pod dwpk-alice/alice-0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newTestServer(t, nil, tt.pods)

			got, err := server.resolvePodTarget(context.Background(), &tt.workspace)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("resolvePodTarget() error = nil, want substring %q", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("resolvePodTarget() error = %q, want substring %q", err.Error(), tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolvePodTarget() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("resolvePodTarget() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func workspaceWithPod(name, namespace, podName string) dwpkv1alpha1.Workspace {
	ws := workspaceWithKeys(name, namespace)
	ws.Status.PodName = podName
	return ws
}

func workspacePod(workspaceName, namespace, podName string) corev1.Pod {
	return corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: namespace,
			Labels: map[string]string{
				dwpkv1alpha1.WorkspaceLabel: workspaceName,
			},
		},
	}
}

func workspacePodWithHome(workspaceName, namespace, podName, homePath string) corev1.Pod {
	pod := workspacePod(workspaceName, namespace, podName)
	pod.Spec.Containers = []corev1.Container{{Name: workspacepkg.ContainerName, WorkingDir: homePath}}
	return pod
}

func newTestServer(t *testing.T, workspaces []dwpkv1alpha1.Workspace, pods []corev1.Pod) *Server {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := dwpkv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme() error = %v", err)
	}

	workspaceObjects := make([]ctrlclient.Object, 0, len(workspaces))
	for i := range workspaces {
		workspaceObjects = append(workspaceObjects, workspaces[i].DeepCopy())
	}

	podObjects := make([]runtime.Object, 0, len(pods))
	for i := range pods {
		podObjects = append(podObjects, pods[i].DeepCopy())
	}

	return &Server{
		WorkspaceClient: ctrlclientfake.NewClientBuilder().WithScheme(scheme).WithObjects(workspaceObjects...).Build(),
		KubeClient:      kubernetesfake.NewSimpleClientset(podObjects...),
	}
}

type testConnMetadata struct {
	user string
}

func (m testConnMetadata) User() string        { return m.user }
func (testConnMetadata) SessionID() []byte     { return nil }
func (testConnMetadata) ClientVersion() []byte { return nil }
func (testConnMetadata) ServerVersion() []byte { return nil }
func (testConnMetadata) RemoteAddr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 22}
}
func (testConnMetadata) LocalAddr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 2222}
}
