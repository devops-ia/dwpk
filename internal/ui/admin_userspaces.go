package ui

import (
	"net/http"
	"strings"

	dwpkv1alpha1 "github.com/devops-ia/dwpk/api/v1alpha1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// handleAdminCreateUserSpace provisions a tenant. The name and owner are all
// that is really required; everything else has a sensible default so an
// administrator can add someone without filling in a form of quantities.
func (s *Server) handleAdminCreateUserSpace(w http.ResponseWriter, r *http.Request) {
	_, api, ok := s.sessionAPI(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		s.writeErrorPage(w, r, errInvalidForm)
		return
	}

	userSpace, err := userSpaceFromForm(r)
	if err != nil {
		s.writeErrorPage(w, r, err)
		return
	}
	if err := api.CreateUserSpace(r.Context(), userSpace); err != nil {
		s.writeErrorPage(w, r, err)
		return
	}

	// A password on the same form means "and give them a local login". The
	// username and owner come from the UserSpace just created, so the two can
	// never disagree - which they could when this was a second form asking for
	// both again.
	if password := r.Form.Get("password"); password != "" && s.localUsers != nil {
		if _, err := s.localUsers.Create(r.Context(),
			userSpace.LoginName(), password, userSpace.Spec.Owner); err != nil {
			// The UserSpace exists by now. Say what happened rather than
			// implying nothing did - the account is real and simply has no
			// password yet, which is a state the operator can fix on this screen.
			session, _ := requestSessionFrom(r.Context())
			s.renderPeople(w, r, session,
				"The user was created, but the password sign-in was not: "+err.Error())
			return
		}
	}

	http.Redirect(w, r, s.path("/admin/users"), http.StatusFound)
}

// handleAdminDeleteUserSpace removes a tenant and, by garbage collection,
// their namespace and every workspace and volume in it.
// handleAdminDeleteUserSpace removes the person: their UserSpace, and the local
// login that authenticates it.
//
// Both, because they are one account to everyone but the API server. Deleting
// only the UserSpace used to leave a login that still accepted a password and
// then failed with "no UserSpace" - an account that exists enough to be
// confusing and not enough to work.
//
// The UserSpace goes first. If the login delete then fails, what is left is a
// dead credential that cannot reach anything, which the Users screen already
// lists and offers to remove; the other order would leave a namespace nobody
// can sign in to.
func (s *Server) handleAdminDeleteUserSpace(w http.ResponseWriter, r *http.Request) {
	session, api, ok := s.sessionAPI(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")

	// Read the owner before deleting, or there is nothing left to match the
	// login against.
	var owner string
	if userSpace, err := api.GetUserSpace(r.Context(), name); err == nil {
		owner = userSpace.Spec.Owner
	}

	if err := api.DeleteUserSpace(r.Context(), name); err != nil {
		s.writeErrorPage(w, r, err)
		return
	}
	if err := s.deleteLoginsFor(r, owner); err != nil {
		s.renderPeople(w, r, session,
			"The user was deleted, but their password sign-in was not: "+err.Error())
		return
	}
	http.Redirect(w, r, s.path("/admin/users"), http.StatusFound)
}

// deleteLoginsFor removes every local login belonging to an owner. Plural
// because nothing stops two existing, and leaving one behind is the bug this
// closes.
func (s *Server) deleteLoginsFor(r *http.Request, owner string) error {
	if s.localUsers == nil || owner == "" {
		return nil
	}
	logins, err := s.localUsers.FindByOwner(r.Context(), owner)
	if err != nil {
		return err
	}
	for _, login := range logins {
		if err := s.localUsers.Delete(r.Context(), login.SecretName); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) handleAdminUpdateQuota(w http.ResponseWriter, r *http.Request) {
	_, api, ok := s.sessionAPI(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		s.writeErrorPage(w, r, errInvalidForm)
		return
	}

	quota, err := quotaFromForm(r)
	if err != nil {
		s.writeErrorPage(w, r, err)
		return
	}
	if err := api.PatchUserSpaceQuota(r.Context(), r.PathValue("name"), quota); err != nil {
		s.writeErrorPage(w, r, err)
		return
	}
	http.Redirect(w, r, s.path(donePath("/admin/quota", "quota")), http.StatusFound)
}

func userSpaceFromForm(r *http.Request) (*dwpkv1alpha1.UserSpace, error) {
	name := strings.TrimSpace(r.Form.Get("name"))
	owner := strings.TrimSpace(r.Form.Get("owner"))
	if name == "" || owner == "" {
		return nil, apiError{status: http.StatusBadRequest, message: "name and owner are both required"}
	}

	networkPolicy := strings.TrimSpace(r.Form.Get("networkPolicy"))
	if networkPolicy != dwpkv1alpha1.NetworkPolicyClusterEgress {
		networkPolicy = dwpkv1alpha1.NetworkPolicyIsolated
	}

	quota, err := quotaFromForm(r)
	if err != nil {
		return nil, err
	}

	return &dwpkv1alpha1.UserSpace{
		TypeMeta:   metav1.TypeMeta{APIVersion: dwpkv1alpha1.GroupVersion.String(), Kind: "UserSpace"},
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: dwpkv1alpha1.UserSpaceSpec{
			Owner: owner,
			// Left empty rather than defaulted here: the fallbacks live on the
			// type (LoginName, ContactEmail, NamespaceName) so every reader
			// agrees, and writing the derived value into the object would freeze
			// it against a later rename.
			Username:      strings.TrimSpace(r.Form.Get("username")),
			Email:         strings.TrimSpace(r.Form.Get("email")),
			Namespace:     strings.TrimSpace(r.Form.Get("namespace")),
			Role:          roleOrDefault(strings.TrimSpace(r.Form.Get("role"))),
			NetworkPolicy: networkPolicy,
			Quota:         quota,
		},
	}, nil
}

// quotaFromForm parses the four quota fields, falling back to a modest default
// for any that are blank so a partially filled form still produces a usable
// tenant rather than a zero quota nobody can work in.
func quotaFromForm(r *http.Request) (dwpkv1alpha1.UserSpaceQuota, error) {
	cpu, err := quantityOrDefault(r.Form.Get("cpu"), "4")
	if err != nil {
		return dwpkv1alpha1.UserSpaceQuota{}, err
	}
	memory, err := quantityOrDefault(r.Form.Get("memory"), "8Gi")
	if err != nil {
		return dwpkv1alpha1.UserSpaceQuota{}, err
	}
	storage, err := quantityOrDefault(r.Form.Get("storage"), "50Gi")
	if err != nil {
		return dwpkv1alpha1.UserSpaceQuota{}, err
	}
	workspaces, err := int32OrDefault(r.Form.Get("workspaces"), 2)
	if err != nil {
		return dwpkv1alpha1.UserSpaceQuota{}, err
	}
	// Zero by default, because a cluster with no GPUs is the common one and a
	// quota nobody set should not silently hand out hardware.
	gpu, err := int32OrDefault(r.Form.Get("gpu"), 0)
	if err != nil {
		return dwpkv1alpha1.UserSpaceQuota{}, err
	}

	return dwpkv1alpha1.UserSpaceQuota{
		CPU:        cpu,
		Memory:     memory,
		Storage:    storage,
		GPU:        gpu,
		Workspaces: workspaces,
	}, nil
}

func quantityOrDefault(raw, fallback string) (resource.Quantity, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = fallback
	}
	quantity, err := resource.ParseQuantity(raw)
	if err != nil {
		return resource.Quantity{}, apiError{
			status:  http.StatusBadRequest,
			message: "not a valid quantity: " + raw,
		}
	}
	return quantity, nil
}

func int32OrDefault(raw string, fallback int32) (int32, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback, nil
	}
	// A quantity parse rather than strconv: it rejects the same shapes the API
	// server would, and keeps one error message for every numeric field.
	parsed, err := resource.ParseQuantity(raw)
	if err != nil || parsed.Value() < 0 {
		return 0, apiError{status: http.StatusBadRequest, message: "not a valid workspace count: " + raw}
	}
	return int32(parsed.Value()), nil
}
