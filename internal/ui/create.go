package ui

import (
	"net/http"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/yaml"
)

func (s *Server) handleNewWorkspaceForm(w http.ResponseWriter, r *http.Request) {
	session, ok := requestSessionFrom(r.Context())
	if !ok {
		s.redirectToLogin(w, r)
		return
	}
	api, err := s.clientFactory.ForToken(session.Token)
	if err != nil {
		s.writeErrorPage(w, r, err)
		return
	}
	imageName := strings.TrimSpace(r.URL.Query().Get("image"))
	if imageName == "" {
		// The form reads its sizes, resources and home path from an image, so
		// there is nothing to render without one. The catalog is where you pick.
		http.Redirect(w, r, s.path("/catalog"), http.StatusSeeOther)
		return
	}
	allowed, err := api.CanUseWorkspaceImage(r.Context(), imageName)
	if err != nil {
		s.writeErrorPage(w, r, err)
		return
	}
	if !allowed {
		s.writeErrorPage(w, r, apiError{status: http.StatusForbidden, message: "you are not authorised to use this workspace image"})
		return
	}
	image, err := api.GetWorkspaceImage(r.Context(), imageName)
	if err != nil {
		s.writeErrorPage(w, r, err)
		return
	}
	// The webhook rejects this on create anyway; refusing here means the user
	// finds out before filling the form in, not after submitting it.
	if image.IsDeprecated(time.Now()) {
		s.writeErrorPage(w, r, apiError{
			status:  http.StatusForbidden,
			message: "this catalog entry is deprecated and accepts no new workspaces",
		})
		return
	}
	// A key is required here only when the profile has none. With defaults set,
	// the admission webhook fills them in and the field is a way to use a
	// different key for this one workspace.
	//
	// The quota comes off the same object. Neither is worth failing the page
	// for: a form that will not render is worse than one whose footnote is
	// missing, and the webhook enforces the quota either way.
	hasDefaults := false
	limit := int32(0)
	if userSpace, err := api.GetUserSpace(r.Context(), session.Identity.UserSpaceName); err == nil {
		hasDefaults = len(userSpace.Spec.SSHAuthorizedKeys) > 0
		limit = userSpace.Spec.Quota.Workspaces
	}
	count := 0
	if existing, err := api.ListWorkspaces(r.Context(), session.Identity.UserSpaceNamespace); err == nil {
		// Every workspace counts, running or not: a stopped one still holds its
		// slot, which is what the webhook counts too.
		count = len(existing)
	}

	values := defaultResourceValues()
	values.GPUResource = string(s.gpuResource(r))

	s.renderAuthedPage(w, r, http.StatusOK, session, "Create workspace", CreateWorkspacePage(CreateData{
		Session:        session,
		Image:          *image,
		Resources:      values,
		HasKeyOnFile:   hasDefaults,
		WorkspaceCount: count,
		WorkspaceLimit: limit,
	}))
}

func (s *Server) handleCreateWorkspace(w http.ResponseWriter, r *http.Request) {
	session, ok := requestSessionFrom(r.Context())
	if !ok {
		s.redirectToLogin(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.writeErrorPage(w, r, err)
		return
	}
	draft, err := draftFromForm(r, session.Identity.UserSpaceNamespace)
	if err != nil {
		s.writeErrorPage(w, r, err)
		return
	}
	newWorkspace, err := buildWorkspace(draft)
	if err != nil {
		s.writeErrorPage(w, r, err)
		return
	}
	api, err := s.clientFactory.ForToken(session.Token)
	if err != nil {
		s.writeErrorPage(w, r, err)
		return
	}
	if err := api.CreateWorkspace(r.Context(), newWorkspace); err != nil {
		s.writeErrorPage(w, r, err)
		return
	}
	http.Redirect(w, r, s.path("/w/"+newWorkspace.Name), http.StatusFound)
}

// draftFromForm reads the create form. Every caller of buildWorkspace goes
// through it, so the preview and the submission cannot disagree about what a
// field is called.
func draftFromForm(r *http.Request, namespace string) (WorkspaceDraft, error) {
	env, err := envVarsFromForm(r)
	if err != nil {
		return WorkspaceDraft{}, err
	}
	return WorkspaceDraft{
		Name:      strings.TrimSpace(r.Form.Get("name")),
		Namespace: namespace,
		Image:     strings.TrimSpace(r.Form.Get("image")),
		SSHKey:    strings.TrimSpace(r.Form.Get("ssh_public_key")),
		Resources: resourceValuesFrom(r),
		Env:       env,
	}, nil
}

// envVarsFromForm reads the repeated env_key/env_value rows the create form
// submits. A row where both boxes are blank is skipped rather than rejected -
// it is what an untouched trailing row looks like, not a mistake. A row with
// only one side filled in is rejected, since that is a variable nobody
// finished naming.
func envVarsFromForm(r *http.Request) ([]corev1.EnvVar, error) {
	keys := r.Form["env_key"]
	values := r.Form["env_value"]
	var env []corev1.EnvVar
	for i, rawKey := range keys {
		key := strings.TrimSpace(rawKey)
		var value string
		if i < len(values) {
			value = strings.TrimSpace(values[i])
		}
		if key == "" && value == "" {
			continue
		}
		if key == "" {
			return nil, apiError{status: http.StatusBadRequest, message: "an environment variable is missing its name"}
		}
		if strings.ContainsAny(key, " =") {
			return nil, apiError{status: http.StatusBadRequest, message: "environment variable name " + key + " must not contain spaces or '='"}
		}
		env = append(env, corev1.EnvVar{Name: key, Value: value})
	}
	return env, nil
}

// handleWorkspacePreview renders the object the form currently describes.
//
// Server-rendered from buildWorkspace rather than assembled in JavaScript: the
// preview is only worth showing if it is the same object the POST will send,
// and two renderers drift. A draft that does not parse yet shows the parse
// error, which is more useful than a blank box while somebody is mid-keystroke.
func (s *Server) handleWorkspacePreview(w http.ResponseWriter, r *http.Request) {
	session, ok := requestSessionFrom(r.Context())
	if !ok {
		s.redirectToLogin(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.writeErrorPage(w, r, errInvalidForm)
		return
	}
	namespace := session.Identity.UserSpaceNamespace
	if chosen := strings.TrimSpace(r.Form.Get("namespace")); chosen != "" {
		namespace = chosen
	}

	draft, err := draftFromForm(r, namespace)
	if err != nil {
		s.renderFragment(w, r, WorkspacePreview("", err.Error()))
		return
	}
	workspace, err := buildWorkspace(draft)
	if err != nil {
		s.renderFragment(w, r, WorkspacePreview("", err.Error()))
		return
	}
	rendered, err := yaml.Marshal(workspace)
	if err != nil {
		s.renderFragment(w, r, WorkspacePreview("", err.Error()))
		return
	}
	s.renderFragment(w, r, WorkspacePreview(string(rendered), ""))
}
