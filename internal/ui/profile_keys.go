package ui

import (
	"net/http"
	"slices"
	"strings"

	"golang.org/x/crypto/ssh"
)

// KeyRow is one SSH public key as the profile screen lists it.
//
// The blob itself is not shown. It is 400 characters that identify nothing to a
// reader, and the type and comment are what tell you which of your machines a
// key belongs to. Fingerprint is what you compare against `ssh-keygen -lf`.
type KeyRow struct {
	Type        string
	Comment     string
	Fingerprint string
	// Value is the original line, carried so the form can post back the keys it
	// is keeping. Never rendered as text.
	Value string
}

// keyRows parses stored keys for display. A key that will not parse is still
// listed - it is on the object, it will be copied onto workspaces, and hiding
// it would leave someone unable to remove the thing that is breaking them.
func keyRows(keys []string) []KeyRow {
	rows := make([]KeyRow, 0, len(keys))
	for _, key := range keys {
		row := KeyRow{Value: key, Type: "unrecognised"}
		if parsed, comment, _, _, err := ssh.ParseAuthorizedKey([]byte(key)); err == nil {
			row.Type = parsed.Type()
			row.Comment = comment
			row.Fingerprint = ssh.FingerprintSHA256(parsed)
		}
		rows = append(rows, row)
	}
	return rows
}

// handleProfileAddKey appends a key to this person's defaults.
//
// The key is validated here only to give an immediate, specific message. The
// decision is still admission's: this posts the whole list and the API server
// accepts or refuses it, so a key that gets past this check does not get past
// the cluster (SPEC §8.1).
func (s *Server) handleProfileAddKey(w http.ResponseWriter, r *http.Request) {
	session, api, ok := s.sessionAPI(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		s.writeErrorPage(w, r, errInvalidForm)
		return
	}

	key := strings.TrimSpace(r.Form.Get("key"))
	if key == "" {
		s.renderProfile(w, r, session, "Paste a public key first.", "")
		return
	}
	if _, _, _, _, err := ssh.ParseAuthorizedKey([]byte(key)); err != nil {
		s.renderProfile(w, r, session,
			"That does not look like an OpenSSH public key. Paste the contents of a .pub file.", "")
		return
	}

	userSpace, err := api.GetUserSpace(r.Context(), session.Identity.UserSpaceName)
	if err != nil {
		s.writeErrorPage(w, r, err)
		return
	}
	if slices.Contains(userSpace.Spec.SSHAuthorizedKeys, key) {
		s.renderProfile(w, r, session, "", "That key is already on your profile.")
		return
	}

	keys := append(slices.Clone(userSpace.Spec.SSHAuthorizedKeys), key)
	if err := api.PatchUserSpaceKeys(r.Context(), userSpace.Name, keys); err != nil {
		s.renderProfile(w, r, session, err.Error(), "")
		return
	}
	s.redirectToProfileTab(w, r, "keys", "key-added")
}

// handleProfileRemoveKey drops one key by its exact value.
//
// By value rather than by index: the form and the object can disagree if
// something else edited the list in between, and an index would then remove a
// different key than the one whose button was pressed.
func (s *Server) handleProfileRemoveKey(w http.ResponseWriter, r *http.Request) {
	session, api, ok := s.sessionAPI(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		s.writeErrorPage(w, r, errInvalidForm)
		return
	}

	target := strings.TrimSpace(r.Form.Get("key"))
	userSpace, err := api.GetUserSpace(r.Context(), session.Identity.UserSpaceName)
	if err != nil {
		s.writeErrorPage(w, r, err)
		return
	}

	keys := slices.DeleteFunc(slices.Clone(userSpace.Spec.SSHAuthorizedKeys),
		func(key string) bool { return strings.TrimSpace(key) == target })
	if len(keys) == len(userSpace.Spec.SSHAuthorizedKeys) {
		// Already gone. Someone pressed the button twice, or removed it in
		// another tab; either way the end state is the one they wanted.
		s.redirectToProfileTab(w, r, "keys", "key-removed")
		return
	}
	if err := api.PatchUserSpaceKeys(r.Context(), userSpace.Name, keys); err != nil {
		s.renderProfile(w, r, session, err.Error(), "")
		return
	}
	s.redirectToProfileTab(w, r, "keys", "key-removed")
}
