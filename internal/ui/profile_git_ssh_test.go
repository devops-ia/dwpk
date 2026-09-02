package ui

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	dwpkv1alpha1 "github.com/devops-ia/dwpk/api/v1alpha1"
)

// A throwaway ed25519 key, generated solely as test fixture data - never used
// for anything real.
const testGitSSHPrivateKey = `-----BEGIN OPENSSH PRIVATE KEY-----
b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAAMwAAAAtzc2gtZW
QyNTUxOQAAACA0+IXikeIJqEjmYudirdq1HUEXASDvI7tm1OdIkKbbYAAAAIhU6eGcVOnh
nAAAAAtzc2gtZWQyNTUxOQAAACA0+IXikeIJqEjmYudirdq1HUEXASDvI7tm1OdIkKbbYA
AAAEANvqJTeC2BJa0qhR4y2FqY76ooaBp9Go5ZzW5GvtTLHDT4heKR4gmoSOZi52Kt2rUd
QRcBIO8ju2bU50iQpttgAAAABHRlc3QB
-----END OPENSSH PRIVATE KEY-----
`

func TestNormalizeLineEndingsStripsCR(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"a\r\nb\r\nc": "a\nb\nc\n",
		"a\rb\rc":     "a\nb\nc\n",
		"a\nb\nc":     "a\nb\nc\n",
	}
	for input, want := range cases {
		if got := normalizeLineEndings(input); got != want {
			t.Errorf("normalizeLineEndings(%q) = %q, want %q", input, got, want)
		}
	}
}

// An HTML <textarea> submits CRLF line breaks regardless of what the user's
// editor used (the HTML forms spec requires it). golang.org/x/crypto/ssh
// tolerates the stray \r bytes at upload-time validation, but the OpenSSH
// client that later loads this same file inside the workspace does not, and
// fails with "error in libcrypto" - so the Secret must never carry them.
func TestAddGitSSHKeyStripsCRLFBeforeStoring(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	server.loginFlow = fakeSessionAuthenticator{
		identity: SessionIdentity{UserSpaceName: "alice", UserSpaceNamespace: "dwpk-alice"},
		token:    "minted",
	}
	csrf, _ := server.csrfStore.Ensure("session-1")
	var storedHost string
	var storedKey []byte
	server.clientFactory = fakeAPIClientFactory{api: fakeAPI{
		userSpaces:      []dwpkv1alpha1.UserSpace{*newUserSpace("alice", "alice@example.com", "dwpk-alice")},
		putGitSSHHost:   &storedHost,
		putGitSSHKeyPEM: &storedKey,
	}}

	crlfKey := strings.ReplaceAll(testGitSSHPrivateKey, "\n", "\r\n")
	form := "host=github.com&key=" + url.QueryEscape(crlfKey)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, authedFormRequest("/profile/git-ssh-keys", csrf, form))

	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303, body = %s", recorder.Code, recorder.Body.String())
	}
	if storedHost != "github.com" {
		t.Errorf("stored host = %q, want github.com", storedHost)
	}
	if strings.Contains(string(storedKey), "\r") {
		t.Errorf("stored key still carries \\r bytes: %q", storedKey)
	}
}

func TestAddGitSSHKeyRefusesAPassphraseProtectedKey(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	server.loginFlow = fakeSessionAuthenticator{
		identity: SessionIdentity{UserSpaceName: "alice", UserSpaceNamespace: "dwpk-alice"},
		token:    "minted",
	}
	csrf, _ := server.csrfStore.Ensure("session-1")
	server.clientFactory = fakeAPIClientFactory{api: fakeAPI{
		userSpaces: []dwpkv1alpha1.UserSpace{*newUserSpace("alice", "alice@example.com", "dwpk-alice")},
	}}

	form := "host=github.com&key=" + url.QueryEscape("not a real key")
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, authedFormRequest("/profile/git-ssh-keys", csrf, form))

	if !strings.Contains(recorder.Body.String(), "does not look like an OpenSSH private key") {
		t.Errorf("body = %s, want a refusal", recorder.Body.String())
	}
}
