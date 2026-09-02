package ui

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"

	dwpkv1alpha1 "github.com/devops-ia/dwpk/api/v1alpha1"
	workspacepkg "github.com/devops-ia/dwpk/internal/workspace"
)

// AdminWorkspaceRow is one workspace the caller is allowed to see.
type AdminWorkspaceRow struct {
	Name      string
	Namespace string
	Image     string
	Status    string
	Running   bool
	// CPU, Memory and GPU are what the workspace asks for, rendered for the
	// table. Requests rather than limits: that is what the quota counts.
	CPU    string
	Memory string
	GPU    string
}

// WorkspaceTarget is a namespace a workspace can be created into, labelled by
// the person who owns it.
type WorkspaceTarget struct {
	Owner     string
	Namespace string
}

// AdminWorkspacesData backs the workspaces screen.
type AdminWorkspacesData struct {
	Session RequestSession
	Rows    []AdminWorkspaceRow
	// Targets and Images back the create form. Both are empty for a plain
	// user, who creates from the catalog in their own namespace instead.
	Targets []WorkspaceTarget
	Images  []ImageOption
	Notice  string
}

// ImageOption is one catalog entry offered by the create form.
type ImageOption struct {
	Name string
}

// handleAdminWorkspaces lists the workspaces the caller may act on, scoped to
// their role: an admin sees the cluster, a manager sees their projects'
// members, a plain user sees their own namespace.
func (s *Server) handleAdminWorkspaces(w http.ResponseWriter, r *http.Request) {
	session, api, ok := s.sessionAPI(w, r)
	if !ok {
		return
	}
	workspaces, err := visibleWorkspaces(r.Context(), api, session.Identity)
	if err != nil {
		s.writeErrorPage(w, r, err)
		return
	}

	images, err := api.ListWorkspaceImages(r.Context())
	if err != nil {
		s.writeErrorPage(w, r, err)
		return
	}
	targets, err := creatableNamespaces(r.Context(), api, session.Identity)
	if err != nil {
		s.writeErrorPage(w, r, err)
		return
	}

	data := AdminWorkspacesData{
		Session: session,
		Rows:    adminWorkspaceRows(workspaces, s.gpuResource(r)),
		Targets: targets,
		Images:  imageOptions(images),
		Notice:  doneNotice(r),
	}

	// htmx swaps the table alone when the filter changes; a full page load
	// still renders the whole screen.
	if r.Header.Get("HX-Request") == boolTrue {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = AdminWorkspacesTable(data).Render(s.renderContext(r), w)
		return
	}
	s.renderAuthedPage(w, r, http.StatusOK, session, "Workspaces", AdminWorkspacesPage(data))
}

// visibleWorkspaces resolves what one identity may list.
//
// A manager cannot use a cluster-scoped LIST: their rights come from
// per-namespace RoleBindings, and Kubernetes answers an unscoped LIST with a
// flat 403 rather than a filtered list. So their namespaces are named one at a
// time, which first means finding who shares their projects.
func visibleWorkspaces(ctx context.Context, api RequestAPI, identity SessionIdentity) ([]dwpkv1alpha1.Workspace, error) {
	if identity.Role == dwpkv1alpha1.UserSpaceRoleAdmin {
		return api.ListWorkspaces(ctx, "")
	}
	return api.ListWorkspaces(ctx, identity.UserSpaceNamespace)
}

// ariaCurrent marks the selected chip. The attribute has to be absent rather
// than "false" on the others, so this returns the empty string for those.
func ariaCurrent(selected bool) string {
	if selected {
		return "page"
	}
	return ""
}

// handleAdminCreateWorkspace creates a workspace in someone else's namespace.
//
// The SSH key is required and belongs to the person who will use it: a
// workspace created without their key is one they cannot reach. That is why
// this asks for it rather than defaulting to the administrator's own.
func (s *Server) handleAdminCreateWorkspace(w http.ResponseWriter, r *http.Request) {
	_, api, ok := s.sessionAPI(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		s.writeErrorPage(w, r, errInvalidForm)
		return
	}
	draft, err := draftFromForm(r, strings.TrimSpace(r.Form.Get("namespace")))
	if err != nil {
		s.writeErrorPage(w, r, err)
		return
	}
	newWorkspace, err := buildWorkspace(draft)
	if err != nil {
		s.writeErrorPage(w, r, err)
		return
	}
	if err := api.CreateWorkspace(r.Context(), newWorkspace); err != nil {
		s.writeErrorPage(w, r, err)
		return
	}
	http.Redirect(w, r, s.path("/admin/workspaces"), http.StatusFound)
}

func imageOptions(images []dwpkv1alpha1.WorkspaceImage) []ImageOption {
	now := time.Now()
	options := make([]ImageOption, 0, len(images))
	for i := range images {
		if images[i].IsDeprecated(now) {
			continue
		}
		options = append(options, ImageOption{Name: images[i].Name})
	}
	return options
}

// creatableNamespaces is whose namespace this person may create a workspace in.
//
// Only an administrator gets a choice. Everybody else creates in their own
// namespace from the catalog, which is the ordinary route and the only one
// their token permits anyway - offering a namespace they cannot write to would
// be a dropdown whose every option 403s.
func creatableNamespaces(ctx context.Context, api RequestAPI, identity SessionIdentity) ([]WorkspaceTarget, error) {
	if identity.Role != dwpkv1alpha1.UserSpaceRoleAdmin {
		return nil, nil
	}
	userSpaces, err := api.ListUserSpaces(ctx)
	if err != nil {
		return nil, err
	}

	targets := make([]WorkspaceTarget, 0, len(userSpaces))
	for i := range userSpaces {
		spec := userSpaces[i].Spec
		if namespace := userSpaces[i].Status.Namespace; namespace != "" {
			targets = append(targets, WorkspaceTarget{Owner: spec.Owner, Namespace: namespace})
		}
	}
	slices.SortFunc(targets, func(a, b WorkspaceTarget) int { return strings.Compare(a.Owner, b.Owner) })
	return targets, nil
}

// workspaceRows renders what each workspace is asking for.
//
// CPU reads from Requests and Memory/GPU read from Limits - genuinely
// different sides now, not an inconsistency: ResourceValues.Requirements
// writes CPU into Requests only (never a limit, so it's never artificially
// throttled) and memory into Limits only (it needs a hard ceiling). A
// workspace that names neither shows a dash rather than "0": unset and zero
// are different answers.
func adminWorkspaceRows(
	workspaces []dwpkv1alpha1.Workspace, gpuResource corev1.ResourceName,
) []AdminWorkspaceRow {
	rows := make([]AdminWorkspaceRow, 0, len(workspaces))
	for i := range workspaces {
		ws := &workspaces[i]
		rows = append(rows, AdminWorkspaceRow{
			Name:      ws.Name,
			Namespace: ws.Namespace,
			Image:     ws.Spec.ImageRef.Name,
			Status:    ws.Status.State,
			Running:   ws.Spec.Running,
			CPU:       quantityText(ws.Spec.Resources.Requests, corev1.ResourceCPU),
			Memory:    quantityText(ws.Spec.Resources.Limits, corev1.ResourceMemory),
			GPU:       quantityText(ws.Spec.Resources.Limits, gpuResource),
		})
	}
	slices.SortFunc(rows, func(a, b AdminWorkspaceRow) int {
		if order := strings.Compare(a.Namespace, b.Namespace); order != 0 {
			return order
		}
		return strings.Compare(a.Name, b.Name)
	})
	return rows
}

// handleAdminStartWorkspace, handleAdminStopWorkspace and
// handleAdminDeleteWorkspace act on somebody else's workspace from the
// administration screen.
//
// They are the same two verbs the owner's own screen uses - patch spec.running,
// and delete - issued with the caller's own token. An administrator can do this
// because their RBAC says so, not because these handlers are special.
func (s *Server) handleAdminStartWorkspace(w http.ResponseWriter, r *http.Request) {
	s.setAdminWorkspaceRunning(w, r, true)
}

func (s *Server) handleAdminStopWorkspace(w http.ResponseWriter, r *http.Request) {
	s.setAdminWorkspaceRunning(w, r, false)
}

func (s *Server) setAdminWorkspaceRunning(w http.ResponseWriter, r *http.Request, running bool) {
	_, api, ok := s.sessionAPI(w, r)
	if !ok {
		return
	}
	_, err := api.PatchWorkspaceRunning(
		r.Context(), r.PathValue("namespace"), r.PathValue("name"), running)
	if err != nil {
		s.writeErrorPage(w, r, err)
		return
	}
	http.Redirect(w, r, s.path("/admin/workspaces"), http.StatusFound)
}

// handleAdminDeleteWorkspace deletes a workspace, and by default the home
// volume with it.
//
// The typed name is checked HERE, not only in the browser. The dialog disables
// its button until the text matches, but that is a convenience: a POST can be
// made without ever loading the page, and a confirmation that only exists in
// JavaScript confirms nothing.
//
// The volume is deleted after the workspace, and only if the workspace deletion
// succeeded. The other order would destroy somebody's data and then fail to
// remove the thing that was supposed to be going away.
func (s *Server) handleAdminDeleteWorkspace(w http.ResponseWriter, r *http.Request) {
	_, api, ok := s.sessionAPI(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		s.writeErrorPage(w, r, errInvalidForm)
		return
	}
	namespace, name := r.PathValue("namespace"), r.PathValue("name")

	if typed := strings.TrimSpace(r.Form.Get("confirm_name")); typed != name {
		s.writeErrorPage(w, r, apiError{
			status: http.StatusBadRequest,
			message: fmt.Sprintf(
				"the workspace was not deleted: %q does not match %q", typed, name),
		})
		return
	}

	if err := api.DeleteWorkspace(r.Context(), namespace, name); err != nil {
		s.writeErrorPage(w, r, err)
		return
	}
	if r.Form.Get("delete_volume") != "" {
		if err := api.DeleteClaim(r.Context(), namespace, workspaceClaimName(name)); err != nil {
			// The workspace is already gone, so this cannot be undone by
			// failing. Say exactly what is left behind rather than implying
			// nothing happened.
			s.writeErrorPage(w, r, fmt.Errorf(
				"the workspace was deleted but its home volume %s was not: %w",
				workspaceClaimName(name), err))
			return
		}
	}
	http.Redirect(w, r, s.path("/admin/workspaces"), http.StatusFound)
}

// runAction is the path segment the run switch posts to.
func runAction(running bool) string {
	if running {
		return "stop"
	}
	return "start"
}

// workspaceClaimName is the PVC a workspace's home directory lives in, for the
// screens that have a name but no object.
func workspaceClaimName(name string) string {
	return workspacepkg.HomeVolumeName + "-" + name + "-0"
}
