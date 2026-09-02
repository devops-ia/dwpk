package ui

import (
	"errors"
	"net/http"
	"regexp"
	"strings"

	"golang.org/x/crypto/ssh"
)

// validGitSSHHost is a conservative hostname shape: it also has to be a safe
// Kubernetes Secret data key once prefixed with gitSSHKeyDataPrefix, so this
// is stricter than "whatever DNS allows" on purpose - no leading/trailing
// dot or hyphen, no consecutive dots.
var validGitSSHHost = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?)*$`)

// normalizeLineEndings turns CRLF and lone CR into LF.
//
// An HTML <textarea> submits its value with CRLF line breaks (the HTML forms
// spec requires it), whatever line ending the user's editor actually used.
// golang.org/x/crypto/ssh's PEM decoder tolerates the stray \r bytes, so
// validation below passes either way - but the OpenSSH client that later
// loads this same file inside the workspace does not, and fails with "error
// in libcrypto" the moment it tries to base64-decode a PEM line carrying one.
// Fixing it here, once, at the one place a key ever enters the system, means
// every consumer downstream can assume LF.
func normalizeLineEndings(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	// A key file conventionally ends in exactly one newline - ssh-keygen
	// writes one, and some parsers (unlike golang.org/x/crypto/ssh) care.
	return s + "\n"
}

// handleProfileAddGitSSHKey adds one host's private key.
//
// This Secret write has no admission webhook behind it the way a Workspace
// or WorkspaceImage change does - corev1.Secret is a built-in type, and
// nothing in this repo validates it at admission. So unlike
// handleProfileAddKey's public-key check (a fast-path convenience in front
// of the real gate), the checks here ARE the only gate.
func (s *Server) handleProfileAddGitSSHKey(w http.ResponseWriter, r *http.Request) {
	session, api, ok := s.sessionAPI(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		s.writeErrorPage(w, r, errInvalidForm)
		return
	}

	host := strings.TrimSpace(r.Form.Get("host"))
	if host == "" {
		s.renderProfile(w, r, session, "Enter the host this key is for, e.g. github.com.", "")
		return
	}
	if !validGitSSHHost.MatchString(host) || len(host) > 253 {
		s.renderProfile(w, r, session, "That doesn't look like a hostname - letters, digits, dots and hyphens only.", "")
		return
	}

	key := normalizeLineEndings(strings.TrimSpace(r.Form.Get("key")))
	if key == "" {
		s.renderProfile(w, r, session, "Paste a private key first.", "")
		return
	}
	if _, err := ssh.ParsePrivateKey([]byte(key)); err != nil {
		var passphraseErr *ssh.PassphraseMissingError
		if errors.As(err, &passphraseErr) {
			s.renderProfile(w, r, session,
				`That key has a passphrase; remove it before uploading (ssh-keygen -p -f <file> -N "") - `+
					"a workspace has nowhere to prompt for one.", "")
			return
		}
		s.renderProfile(w, r, session,
			"That does not look like an OpenSSH private key. Paste the contents of the key file itself, not the .pub file.", "")
		return
	}

	namespace := session.Identity.UserSpaceNamespace
	if err := api.PutGitSSHKey(r.Context(), namespace, host, []byte(key)); err != nil {
		if errors.Is(err, ErrGitSSHKeyExists) {
			s.renderProfile(w, r, session, "", "You already have a key for "+host+"; remove it first to replace it.")
			return
		}
		s.renderProfile(w, r, session, err.Error(), "")
		return
	}
	s.redirectToProfileTab(w, r, "keys", "git-key-added")
}

// handleProfileRemoveGitSSHKey drops one host's key.
func (s *Server) handleProfileRemoveGitSSHKey(w http.ResponseWriter, r *http.Request) {
	session, api, ok := s.sessionAPI(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		s.writeErrorPage(w, r, errInvalidForm)
		return
	}

	host := strings.TrimSpace(r.Form.Get("host"))
	namespace := session.Identity.UserSpaceNamespace
	if err := api.DeleteGitSSHKey(r.Context(), namespace, host); err != nil {
		s.renderProfile(w, r, session, err.Error(), "")
		return
	}
	s.redirectToProfileTab(w, r, "keys", "git-key-removed")
}
