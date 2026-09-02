package gateway

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"

	dwpkv1alpha1 "github.com/devops-ia/dwpk/api/v1alpha1"
	"golang.org/x/crypto/ssh"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestResolveWorkspaceTargetByPublicKey(t *testing.T) {
	aliceLine, aliceKey := newAuthorizedKey(t)
	bobLine, bobKey := newAuthorizedKey(t)
	sharedLine, sharedKey := newAuthorizedKey(t)

	tests := []struct {
		name       string
		key        ssh.PublicKey
		workspaces []dwpkv1alpha1.Workspace
		wantName   string
		wantNS     string
		wantPod    string
		wantErr    string
	}{
		{
			name: "matches workspace and derives statefulset pod name",
			key:  aliceKey,
			workspaces: []dwpkv1alpha1.Workspace{
				workspaceWithKeys("alice", "dwpk-alice", aliceLine),
				workspaceWithKeys("bob", "dwpk-bob", bobLine),
			},
			wantName: "alice",
			wantNS:   "dwpk-alice",
			wantPod:  "alice-0",
		},
		{
			name: "ignores malformed authorized key entries while matching valid key",
			key:  bobKey,
			workspaces: []dwpkv1alpha1.Workspace{
				workspaceWithKeys("alice", "dwpk-alice", "ssh-ed25519 definitely-not-base64", bobLine),
			},
			wantName: "alice",
			wantNS:   "dwpk-alice",
			wantPod:  "alice-0",
		},
		{
			name: "returns clear error when no workspace matches key",
			key:  aliceKey,
			workspaces: []dwpkv1alpha1.Workspace{
				workspaceWithKeys("bob", "dwpk-bob", bobLine),
			},
			wantErr: "no Workspace matched public key",
		},
		{
			name: "returns clear error when key matches more than one workspace",
			key:  sharedKey,
			workspaces: []dwpkv1alpha1.Workspace{
				workspaceWithKeys("alice", "dwpk-alice", sharedLine),
				workspaceWithKeys("bob", "dwpk-bob", sharedLine),
			},
			wantErr: "matched multiple Workspaces",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveWorkspaceTargetByPublicKey(tt.key, tt.workspaces)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("ResolveWorkspaceTargetByPublicKey() error = nil, want substring %q", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("ResolveWorkspaceTargetByPublicKey() error = %q, want substring %q", err.Error(), tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveWorkspaceTargetByPublicKey() error = %v", err)
			}
			if got.Workspace.Name != tt.wantName {
				t.Fatalf("workspace name = %q, want %q", got.Workspace.Name, tt.wantName)
			}
			if got.PodNamespace != tt.wantNS {
				t.Fatalf("pod namespace = %q, want %q", got.PodNamespace, tt.wantNS)
			}
			if got.PodName != tt.wantPod {
				t.Fatalf("pod name = %q, want %q", got.PodName, tt.wantPod)
			}
		})
	}
}

func workspaceWithKeys(name, namespace string, keys ...string) dwpkv1alpha1.Workspace {
	return dwpkv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: dwpkv1alpha1.WorkspaceSpec{
			SSHAuthorizedKeys: keys,
		},
	}
}

func newAuthorizedKey(t *testing.T) (string, ssh.PublicKey) {
	t.Helper()

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}

	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("NewSignerFromKey() error = %v", err)
	}

	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey()))), signer.PublicKey()
}
