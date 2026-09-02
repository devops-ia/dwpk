package webhook

import (
	"strings"
	"testing"

	dwpkv1alpha1 "github.com/devops-ia/dwpk/api/v1alpha1"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

// Every key type the platform accepts must actually parse. The fixtures are
// real keys (see sshkeys_fixtures_test.go) because a plausible-looking string
// passes a regex and fails a parser, and the parser is what runs.
func TestValidateAuthorizedKeysAcceptsEveryOpenSSHType(t *testing.T) {
	t.Parallel()

	for name, key := range validPublicKeys {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if errs := validateAuthorizedKeys(field.NewPath("spec"), []string{key}); len(errs) != 0 {
				t.Fatalf("%s rejected: %v", name, errs)
			}
		})
	}

	// All seven at once, which is what a profile with a laptop, a desktop and a
	// security key actually looks like.
	all := make([]string, 0, len(validPublicKeys))
	for _, key := range validPublicKeys {
		all = append(all, key)
	}
	if errs := validateAuthorizedKeys(field.NewPath("spec"), all); len(errs) != 0 {
		t.Fatalf("a set of every type was rejected: %v", errs)
	}
}

func TestValidateAuthorizedKeysRejectsNonsense(t *testing.T) {
	t.Parallel()

	for name, key := range map[string]string{
		"empty":              "",
		"whitespace":         "   ",
		"a private key":      "-----BEGIN OPENSSH PRIVATE KEY-----",
		"type but no body":   "ssh-ed25519",
		"body is not base64": "ssh-ed25519 not-base64-at-all alice@laptop",
		"prose":              "please let me in",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if errs := validateAuthorizedKeys(field.NewPath("spec"), []string{key}); len(errs) == 0 {
				t.Fatalf("%q was accepted", key)
			}
		})
	}
}

// The error names which key was wrong without reprinting 400 characters of
// base64 that identify nothing.
func TestKeyErrorsAreReadable(t *testing.T) {
	t.Parallel()

	errs := validateAuthorizedKeys(field.NewPath("spec", "sshAuthorizedKeys"),
		[]string{"ssh-ed25519 nope alice@laptop"})
	if len(errs) != 1 {
		t.Fatalf("errs = %v", errs)
	}
	message := errs[0].Error()
	if !strings.Contains(message, "alice@laptop") {
		t.Fatalf("the error does not identify the key by its comment: %s", message)
	}
	if strings.Contains(message, "nope") {
		t.Fatalf("the error reprints the key body: %s", message)
	}
}

// The owner's keys are copied onto a workspace that has none, and a workspace
// that brought its own is left alone - otherwise a profile edit would silently
// change what a running machine trusts.
func TestApplyKeyDefault(t *testing.T) {
	t.Parallel()

	owner := &dwpkv1alpha1.UserSpace{
		Spec: dwpkv1alpha1.UserSpaceSpec{SSHAuthorizedKeys: []string{"ssh-ed25519 AAAA owner"}},
	}

	inherits := &dwpkv1alpha1.Workspace{}
	applyKeyDefault(inherits, owner)
	if len(inherits.Spec.SSHAuthorizedKeys) != 1 {
		t.Fatalf("a workspace with no keys did not inherit: %v", inherits.Spec.SSHAuthorizedKeys)
	}

	explicit := &dwpkv1alpha1.Workspace{
		Spec: dwpkv1alpha1.WorkspaceSpec{SSHAuthorizedKeys: []string{"ssh-ed25519 AAAA mine"}},
	}
	applyKeyDefault(explicit, owner)
	if explicit.Spec.SSHAuthorizedKeys[0] != "ssh-ed25519 AAAA mine" {
		t.Fatalf("an explicit key was overwritten: %v", explicit.Spec.SSHAuthorizedKeys)
	}

	// Cloned, not aliased: a later edit to the workspace must not reach back
	// into the UserSpace it was defaulted from.
	inherits.Spec.SSHAuthorizedKeys[0] = "changed"
	if owner.Spec.SSHAuthorizedKeys[0] == "changed" {
		t.Fatal("the workspace shares its slice with the UserSpace")
	}
}
