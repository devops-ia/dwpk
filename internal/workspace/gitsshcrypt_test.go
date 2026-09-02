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

package workspace

import (
	"bytes"
	"testing"
)

func testAESKey() []byte {
	return bytes.Repeat([]byte{0x42}, 32)
}

func TestEncryptDecryptGitSSHKeyRoundTrips(t *testing.T) {
	t.Parallel()
	key := testAESKey()
	plaintext := []byte("-----BEGIN OPENSSH PRIVATE KEY-----\nnot a real key\n-----END OPENSSH PRIVATE KEY-----\n")

	ciphertext, err := EncryptGitSSHKey(key, plaintext)
	if err != nil {
		t.Fatalf("EncryptGitSSHKey() error = %v", err)
	}
	if bytes.Contains(ciphertext, plaintext) {
		t.Error("ciphertext contains the plaintext verbatim")
	}

	got, err := DecryptGitSSHKey(key, ciphertext)
	if err != nil {
		t.Fatalf("DecryptGitSSHKey() error = %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Errorf("DecryptGitSSHKey() = %q, want %q", got, plaintext)
	}
}

// AES-GCM's own requirement: a nonce is never reused under the same key, so
// two encryptions of the same plaintext must never produce the same
// ciphertext.
func TestEncryptGitSSHKeyNeverReusesANonce(t *testing.T) {
	t.Parallel()
	key := testAESKey()
	plaintext := []byte("same plaintext every time")

	first, err := EncryptGitSSHKey(key, plaintext)
	if err != nil {
		t.Fatalf("EncryptGitSSHKey() error = %v", err)
	}
	second, err := EncryptGitSSHKey(key, plaintext)
	if err != nil {
		t.Fatalf("EncryptGitSSHKey() error = %v", err)
	}
	if bytes.Equal(first, second) {
		t.Error("two encryptions of the same plaintext produced identical ciphertext - the nonce was reused")
	}
}

// GCM's authentication tag must catch tampering, not silently return garbage.
func TestDecryptGitSSHKeyRejectsTamperedCiphertext(t *testing.T) {
	t.Parallel()
	key := testAESKey()
	ciphertext, err := EncryptGitSSHKey(key, []byte("plaintext"))
	if err != nil {
		t.Fatalf("EncryptGitSSHKey() error = %v", err)
	}
	tampered := bytes.Clone(ciphertext)
	tampered[len(tampered)-1] ^= 0xFF

	if _, err := DecryptGitSSHKey(key, tampered); err == nil {
		t.Error("DecryptGitSSHKey() accepted tampered ciphertext")
	}
}

func TestDecryptGitSSHKeyRejectsTheWrongKey(t *testing.T) {
	t.Parallel()
	ciphertext, err := EncryptGitSSHKey(testAESKey(), []byte("plaintext"))
	if err != nil {
		t.Fatalf("EncryptGitSSHKey() error = %v", err)
	}
	wrongKey := bytes.Repeat([]byte{0x24}, 32)

	if _, err := DecryptGitSSHKey(wrongKey, ciphertext); err == nil {
		t.Error("DecryptGitSSHKey() accepted the wrong key")
	}
}
