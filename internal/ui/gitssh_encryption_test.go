package ui

import (
	"bytes"
	"context"
	"strings"
	"testing"

	dwpkv1alpha1 "github.com/devops-ia/dwpk/api/v1alpha1"
	workspacepkg "github.com/devops-ia/dwpk/internal/workspace"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func testGitSSHClient(t *testing.T) ctrlclient.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 to scheme: %v", err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).Build()
}

// A throwaway ed25519 key, generated solely as test fixture data - never used
// for anything real. Matches profile_git_ssh_test.go's fixture.
const testGitSSHAPIPrivateKey = `-----BEGIN OPENSSH PRIVATE KEY-----
b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAAMwAAAAtzc2gtZW
QyNTUxOQAAACA0+IXikeIJqEjmYudirdq1HUEXASDvI7tm1OdIkKbbYAAAAIhU6eGcVOnh
nAAAAAtzc2gtZWQyNTUxOQAAACA0+IXikeIJqEjmYudirdq1HUEXASDvI7tm1OdIkKbbYA
AAAEANvqJTeC2BJa0qhR4y2FqY76ooaBp9Go5ZzW5GvtTLHDT4heKR4gmoSOZi52Kt2rUd
QRcBIO8ju2bU50iQpttgAAAABHRlc3QB
-----END OPENSSH PRIVATE KEY-----
`

func testAESKey() []byte {
	return bytes.Repeat([]byte{0x11}, 32)
}

// PutGitSSHKey must never write the plaintext key to the Secret it stores -
// this is the entire point of the encryption feature.
func TestPutGitSSHKeyStoresCiphertextNotPlaintext(t *testing.T) {
	t.Parallel()
	api := &requestAPI{workspaceClient: testGitSSHClient(t), gitSSHEncryptionKey: testAESKey()}
	ctx := context.Background()

	if err := api.PutGitSSHKey(ctx, "dwpk-alice", "github.com", []byte(testGitSSHAPIPrivateKey)); err != nil {
		t.Fatalf("PutGitSSHKey() error = %v", err)
	}

	secret := &corev1.Secret{}
	if err := api.workspaceClient.Get(ctx, ctrlclient.ObjectKey{Namespace: "dwpk-alice", Name: dwpkv1alpha1.GitSSHKeysSecretName}, secret); err != nil {
		t.Fatalf("get Secret: %v", err)
	}
	stored := secret.Data[dwpkv1alpha1.GitSSHKeyDataPrefix+"github.com"]
	if bytes.Contains(stored, []byte("OPENSSH PRIVATE KEY")) {
		t.Errorf("stored key-github.com carries the plaintext PEM verbatim: %q", stored)
	}
	if len(stored) == 0 {
		t.Fatal("key-github.com is empty")
	}

	meta := string(secret.Data[dwpkv1alpha1.GitSSHKeyMetaDataPrefix+"github.com"])
	if !strings.HasPrefix(meta, "ssh-ed25519 SHA256:") {
		t.Errorf("meta-github.com = %q, want a %q-prefixed fingerprint entry", meta, "ssh-ed25519 SHA256:")
	}
}

// GetGitSSHKeys must be able to list a key back (fingerprint and type) purely
// from the unencrypted metadata entry, with no decryption at all.
func TestGetGitSSHKeysListsFromMetadataWithoutDecrypting(t *testing.T) {
	t.Parallel()
	api := &requestAPI{workspaceClient: testGitSSHClient(t), gitSSHEncryptionKey: testAESKey()}
	ctx := context.Background()

	if err := api.PutGitSSHKey(ctx, "dwpk-alice", "github.com", []byte(testGitSSHAPIPrivateKey)); err != nil {
		t.Fatalf("PutGitSSHKey() error = %v", err)
	}

	// A requestAPI with no encryption key at all must still be able to list -
	// proof that listing never touches the key material.
	listOnly := &requestAPI{workspaceClient: api.workspaceClient}
	infos, err := listOnly.GetGitSSHKeys(ctx, "dwpk-alice")
	if err != nil {
		t.Fatalf("GetGitSSHKeys() error = %v", err)
	}
	if len(infos) != 1 {
		t.Fatalf("got %d keys, want 1", len(infos))
	}
	if infos[0].Host != "github.com" {
		t.Errorf("host = %q, want github.com", infos[0].Host)
	}
	if infos[0].KeyType != "ssh-ed25519" {
		t.Errorf("keyType = %q, want ssh-ed25519", infos[0].KeyType)
	}
	if !strings.HasPrefix(infos[0].Fingerprint, "SHA256:") {
		t.Errorf("fingerprint = %q, want a SHA256: fingerprint", infos[0].Fingerprint)
	}
}

// The whole point: what PutGitSSHKey stores must be exactly what
// workspace.DecryptGitSSHKey recovers with the same key - this is the
// contract the controller's ensureGitSSHRuntimeSecret depends on.
func TestPutGitSSHKeyRoundTripsThroughWorkspaceDecrypt(t *testing.T) {
	t.Parallel()
	key := testAESKey()
	api := &requestAPI{workspaceClient: testGitSSHClient(t), gitSSHEncryptionKey: key}
	ctx := context.Background()

	if err := api.PutGitSSHKey(ctx, "dwpk-alice", "github.com", []byte(testGitSSHAPIPrivateKey)); err != nil {
		t.Fatalf("PutGitSSHKey() error = %v", err)
	}

	secret := &corev1.Secret{}
	if err := api.workspaceClient.Get(ctx, ctrlclient.ObjectKey{Namespace: "dwpk-alice", Name: dwpkv1alpha1.GitSSHKeysSecretName}, secret); err != nil {
		t.Fatalf("get Secret: %v", err)
	}
	plaintext, err := workspacepkg.DecryptGitSSHKey(key, secret.Data[dwpkv1alpha1.GitSSHKeyDataPrefix+"github.com"])
	if err != nil {
		t.Fatalf("DecryptGitSSHKey() error = %v", err)
	}
	if string(plaintext) != testGitSSHAPIPrivateKey {
		t.Errorf("decrypted key = %q, want the original plaintext", plaintext)
	}
}

func TestPutGitSSHKeyRefusesWithNoEncryptionKeyConfigured(t *testing.T) {
	t.Parallel()
	api := &requestAPI{workspaceClient: testGitSSHClient(t)}

	err := api.PutGitSSHKey(context.Background(), "dwpk-alice", "github.com", []byte(testGitSSHAPIPrivateKey))
	if err != errGitSSHEncryptionNotConfigured {
		t.Errorf("PutGitSSHKey() error = %v, want errGitSSHEncryptionNotConfigured", err)
	}
}

// DeleteGitSSHKey must remove both the ciphertext and its metadata entry -
// leaving a stale meta-<host> behind would list a key that no longer exists.
func TestDeleteGitSSHKeyRemovesMetadataToo(t *testing.T) {
	t.Parallel()
	api := &requestAPI{workspaceClient: testGitSSHClient(t), gitSSHEncryptionKey: testAESKey()}
	ctx := context.Background()

	if err := api.PutGitSSHKey(ctx, "dwpk-alice", "github.com", []byte(testGitSSHAPIPrivateKey)); err != nil {
		t.Fatalf("PutGitSSHKey() error = %v", err)
	}
	if err := api.DeleteGitSSHKey(ctx, "dwpk-alice", "github.com"); err != nil {
		t.Fatalf("DeleteGitSSHKey() error = %v", err)
	}

	secret := &corev1.Secret{}
	err := api.workspaceClient.Get(ctx, ctrlclient.ObjectKey{Namespace: "dwpk-alice", Name: dwpkv1alpha1.GitSSHKeysSecretName}, secret)
	if err == nil {
		t.Errorf("Secret still exists after deleting the only key: %+v", secret.Data)
	}
}
