package ui

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	dwpkv1alpha1 "github.com/devops-ia/dwpk/api/v1alpha1"
)

func (s *Server) handleWorkspacePage(w http.ResponseWriter, r *http.Request) {
	session, ok := requestSessionFrom(r.Context())
	if !ok {
		s.redirectToLogin(w, r)
		return
	}
	data, err := s.workspacePageData(r, session)
	if err != nil {
		s.writeErrorPage(w, r, err)
		return
	}
	s.renderShell(w, r, session, PageShell{
		Title:         "Workspace " + data.Workspace.Name,
		Authenticated: true,
		Terminal:      true,
	}, WorkspacePage(data))
}

// handleTerminalWindow is the pop-out: the same terminal component over the same
// websocket, with no navigation around it, so it can be dragged to another
// monitor and left there.
func (s *Server) handleTerminalWindow(w http.ResponseWriter, r *http.Request) {
	session, ok := requestSessionFrom(r.Context())
	if !ok {
		s.redirectToLogin(w, r)
		return
	}
	data, err := s.workspacePageData(r, session)
	if err != nil {
		s.writeErrorPage(w, r, err)
		return
	}
	s.renderShell(w, r, session, PageShell{
		Title:    data.Workspace.Name + " terminal",
		Bare:     true,
		Terminal: true,
	}, TerminalWindow(data))
}

func (s *Server) handleWorkspaceStatus(w http.ResponseWriter, r *http.Request) {
	session, ok := requestSessionFrom(r.Context())
	if !ok {
		s.redirectToLogin(w, r)
		return
	}
	data, err := s.workspacePageData(r, session)
	if err != nil {
		s.writeErrorPage(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if data.Settled {
		w.WriteHeader(286)
	}
	_ = WorkspaceStatusCard(data).Render(s.renderContext(r), w)
}

func (s *Server) handleStartWorkspace(w http.ResponseWriter, r *http.Request) {
	s.handleWorkspaceRunningPatch(w, r, true)
}

func (s *Server) handleStopWorkspace(w http.ResponseWriter, r *http.Request) {
	s.handleWorkspaceRunningPatch(w, r, false)
}

func (s *Server) handleWorkspaceRunningPatch(w http.ResponseWriter, r *http.Request, running bool) {
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
	workspace, err := api.PatchWorkspaceRunning(r.Context(), session.Identity.UserSpaceNamespace, r.PathValue("name"), running)
	if err != nil {
		s.writeErrorPage(w, r, err)
		return
	}
	// Two callers, two answers. The workspace page posts with htmx and wants the
	// fragment back; the dashboard cards are plain forms, and handing a browser
	// a layout-less fragment strands it on a page with no stylesheet, no script
	// and no way back - which is what "Stop loses the styles" was.
	if r.Header.Get("HX-Request") != boolTrue {
		s.redirectBack(w, r, "/w/"+workspace.Name)
		return
	}

	resourceValues := resourceValuesFromWorkspace(workspace, s.gpuResource(r))
	data := WorkspacePageData{
		Session:       session,
		Workspace:     workspace,
		GatewayHost:   s.gatewayHost,
		Endpoint:      workspaceEndpoint(workspace, s.gatewayHost),
		SSHCommand:    workspaceSSHCommand(workspace, s.gatewayHost),
		VSCodeLink:    workspaceVSCodeLink(workspace, s.gatewayHost, s.workspaceHomePath(r.Context(), api, workspace)),
		CPU:           resourceValues.CPU,
		Memory:        resourceValues.MemoryLimit,
		GPU:           resourceValues.GPU,
		EditResources: resourceValues,
		// Never settled here, whatever the status says. This response is the
		// object as just patched, and the controller has not yet reacted - its
		// state is still the one from before the button was pressed. Rendering
		// that as settled means the card stops polling and sits on a stale
		// "Suspended" until somebody reloads the page.
		//
		// The next poll gets the real state, and answers 286 to stop itself
		// once the workspace has actually finished moving.
		Settled: false,
	}
	s.renderFragment(w, r, WorkspaceStatusCard(data))
}

// handleUpdateWorkspaceResources lets the owner change their own workspace's
// resources and env vars after creation. Storage is never in the submitted
// form (resourceFields renders it disabled for this dialog), so the patch
// never touches it - it is immutable, CEL-enforced.
//
// It does not restart the running pod itself. The reconciler keeps the
// StatefulSet's template current on every pass regardless, but a live pod is
// not force-recreated just because its template changed - the redirect's
// flash notice says so, rather than promising something this handler does
// not do.
func (s *Server) handleUpdateWorkspaceResources(w http.ResponseWriter, r *http.Request) {
	session, ok := requestSessionFrom(r.Context())
	if !ok {
		s.redirectToLogin(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.writeErrorPage(w, r, err)
		return
	}
	requirements, err := resourceValuesFrom(r).Requirements()
	if err != nil {
		s.writeErrorPage(w, r, err)
		return
	}
	env, err := envVarsFromForm(r)
	if err != nil {
		s.writeErrorPage(w, r, err)
		return
	}
	api, err := s.clientFactory.ForToken(session.Token)
	if err != nil {
		s.writeErrorPage(w, r, err)
		return
	}
	name := r.PathValue("name")
	if _, err := api.PatchWorkspaceResources(r.Context(), session.Identity.UserSpaceNamespace, name, requirements, env); err != nil {
		s.writeErrorPage(w, r, err)
		return
	}
	http.Redirect(w, r, s.path(donePath("/w/"+name, "resized")), http.StatusFound)
}

// logTailLines is what fits a scrollable pane without asking the API server for
// a whole container's history every five seconds.
const logTailLines = 200

func (s *Server) handleWorkspaceLogs(w http.ResponseWriter, r *http.Request) {
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
	namespace := session.Identity.UserSpaceNamespace
	workspace, err := api.GetWorkspace(r.Context(), namespace, r.PathValue("name"))
	if err != nil {
		s.writeErrorPage(w, r, err)
		return
	}
	s.renderFragment(w, r, WorkspaceLogView(workspaceLogData(r, api, namespace, workspace)))
}

// workspaceLogData turns each outcome into something a reader can act on. A
// forbidden tail is the API server's own message, not a paraphrase (SPEC §8.3).
func workspaceLogData(r *http.Request, api RequestAPI, namespace string, workspace *dwpkv1alpha1.Workspace) WorkspaceLogData {
	if workspace.Status.PodName == "" {
		return WorkspaceLogData{
			Message: "No pod is running.",
			Detail:  "Logs appear once the workspace is started.",
		}
	}
	stream, err := api.WorkspaceLogs(r.Context(), namespace, workspace.Status.PodName, logTailLines)
	if err != nil {
		return WorkspaceLogData{Message: "Logs are unavailable.", Detail: err.Error()}
	}
	defer stream.Close() //nolint:errcheck // a read-only stream

	lines, err := io.ReadAll(stream)
	if err != nil {
		return WorkspaceLogData{Message: "Logs stopped mid-read.", Detail: err.Error()}
	}
	if len(lines) == 0 {
		return WorkspaceLogData{
			Message: "No output yet.",
			Detail:  "The container has written nothing to stdout or stderr.",
		}
	}
	return WorkspaceLogData{Lines: string(lines)}
}

func (s *Server) workspacePageData(r *http.Request, session RequestSession) (WorkspacePageData, error) {
	api, err := s.clientFactory.ForToken(session.Token)
	if err != nil {
		return WorkspacePageData{}, err
	}
	workspace, err := api.GetWorkspace(r.Context(), session.Identity.UserSpaceNamespace, r.PathValue("name"))
	if err != nil {
		return WorkspacePageData{}, err
	}
	resourceValues := resourceValuesFromWorkspace(workspace, s.gpuResource(r))
	return WorkspacePageData{
		Session:       session,
		Workspace:     workspace,
		GatewayHost:   s.gatewayHost,
		Endpoint:      workspaceEndpoint(workspace, s.gatewayHost),
		SSHCommand:    workspaceSSHCommand(workspace, s.gatewayHost),
		VSCodeLink:    workspaceVSCodeLink(workspace, s.gatewayHost, s.workspaceHomePath(r.Context(), api, workspace)),
		Settled:       workspaceStateSettled(workspace.Status.State),
		CPU:           resourceValues.CPU,
		Memory:        resourceValues.MemoryLimit,
		GPU:           resourceValues.GPU,
		EditResources: resourceValues,
		Notice:        doneNotice(r),
	}, nil
}

// workspaceHomePath is the catalog entry's own HomePath, or "" when it could
// not be read - a lookup failure here is not worth failing the whole page
// over, since workspaceVSCodeLink already has a working fallback.
func (s *Server) workspaceHomePath(ctx context.Context, api RequestAPI, workspace *dwpkv1alpha1.Workspace) string {
	image, err := api.GetWorkspaceImage(ctx, workspace.Spec.ImageRef.Name)
	if err != nil {
		return ""
	}
	return image.Spec.HomePath
}

// handleDeleteWorkspace lets the owner delete their own workspace, the same
// way an admin already could from /admin/workspaces - typed-name confirmation
// checked here (not only in the dialog's JavaScript), and the home volume
// deleted by default because leaving it behind is an invisible resource that
// only turns up on a bill later.
//
// Order matters: the volume is only removed once the workspace deletion has
// succeeded, and a failure at that second step says exactly what is left
// rather than implying nothing happened.
func (s *Server) handleDeleteWorkspace(w http.ResponseWriter, r *http.Request) {
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
	if err := r.ParseForm(); err != nil {
		s.writeErrorPage(w, r, errInvalidForm)
		return
	}
	namespace, name := session.Identity.UserSpaceNamespace, r.PathValue("name")

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
			s.writeErrorPage(w, r, fmt.Errorf(
				"the workspace was deleted but its home volume %s was not: %w",
				workspaceClaimName(name), err))
			return
		}
	}
	http.Redirect(w, r, s.path("/"), http.StatusFound)
}
