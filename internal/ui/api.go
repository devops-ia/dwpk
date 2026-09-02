package ui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	dwpkv1alpha1 "github.com/devops-ia/dwpk/api/v1alpha1"
	"github.com/devops-ia/dwpk/internal/auth"
	"github.com/devops-ia/dwpk/internal/workspace"
)

// apiVersion is the single place the REST prefix is spelled, so a future
// /api/v2 does not mean grepping for string literals.
const apiVersion = "/api/v1"

var errLocalUsersDisabled = apiError{status: http.StatusNotFound, message: "local user management is not enabled"}

// SessionResponse is what a client reads to learn who it is and, for
// cookie-authenticated clients, which CSRF token to send on writes.
type SessionResponse struct {
	Email              string `json:"email,omitempty"`
	UserSpaceName      string `json:"userspace,omitempty"`
	UserSpaceNamespace string `json:"namespace"`
	Role               string `json:"role,omitempty"`
	CSRFToken          string `json:"csrf_token,omitempty"`
}

type CreateWorkspaceRequest struct {
	Name         string `json:"name"`
	Image        string `json:"image"`
	CPU          string `json:"cpu,omitempty"`
	Memory       string `json:"memory,omitempty"`
	GPU          string `json:"gpu,omitempty"`
	Storage      string `json:"storage"`
	SSHPublicKey string `json:"ssh_public_key"`
}

type TokenResponse struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	CreatedAt string `json:"created_at"`
	// Token is the plaintext value, returned only by the issuing call and
	// never recoverable afterwards.
	Token string `json:"token,omitempty"`
}

// MembershipRequest changes the administrator-editable fields of a UserSpace.
// Every field is a pointer so an omitted one keeps its current value rather
// than being reset to the zero value.
type MembershipRequest struct {
	Role     *string `json:"role,omitempty"`
	Disabled *bool   `json:"disabled,omitempty"`
}

type LocalUserRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Owner    string `json:"owner"`
}

type LocalUserResponse struct {
	Name     string `json:"name"`
	Username string `json:"username"`
	Owner    string `json:"owner"`
}

type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (s *Server) apiRoutes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+apiVersion+"/session", s.handleAPISession)
	mux.HandleFunc("POST "+apiVersion+"/logout", s.handleAPILogout)

	mux.HandleFunc("GET "+apiVersion+"/workspace-images", s.handleAPIListWorkspaceImages)
	mux.HandleFunc("GET "+apiVersion+"/workspace-images/{name}", s.handleAPIGetWorkspaceImage)

	mux.HandleFunc("GET "+apiVersion+"/image-registries", s.handleAPIListImageRegistries)
	mux.HandleFunc("GET "+apiVersion+"/image-registries/{name}", s.handleAPIGetImageRegistry)

	mux.HandleFunc("GET "+apiVersion+"/workspaces", s.handleAPIListWorkspaces)
	mux.HandleFunc("POST "+apiVersion+"/workspaces", s.handleAPICreateWorkspace)
	mux.HandleFunc("GET "+apiVersion+"/workspaces/{name}", s.handleAPIGetWorkspace)
	mux.HandleFunc("DELETE "+apiVersion+"/workspaces/{name}", s.handleAPIDeleteWorkspace)
	mux.HandleFunc("POST "+apiVersion+"/workspaces/{name}/start", s.handleAPIStartWorkspace)
	mux.HandleFunc("POST "+apiVersion+"/workspaces/{name}/stop", s.handleAPIStopWorkspace)

	mux.HandleFunc("GET "+apiVersion+"/tokens", s.handleAPIListTokens)
	mux.HandleFunc("POST "+apiVersion+"/tokens", s.handleAPIIssueToken)
	mux.HandleFunc("DELETE "+apiVersion+"/tokens/{name}", s.handleAPIRevokeToken)

	mux.HandleFunc("GET "+apiVersion+"/admin/userspaces", s.handleAPIAdminUserSpaces)
	mux.HandleFunc("PATCH "+apiVersion+"/admin/userspaces/{name}", s.handleAPIAdminUpdateMembership)
	mux.HandleFunc("GET "+apiVersion+"/admin/quota", s.handleAPIAdminQuota)
	mux.HandleFunc("GET "+apiVersion+"/admin/local-users", s.handleAPIListLocalUsers)
	mux.HandleFunc("POST "+apiVersion+"/admin/local-users", s.handleAPICreateLocalUser)
	mux.HandleFunc("DELETE "+apiVersion+"/admin/local-users/{name}", s.handleAPIDeleteLocalUser)

	s.resourceRoutes(mux)

	return s.withAPISession(mux)
}

// handleAPILocalLogin exchanges username/password for the same server-side
// session a browser login creates, so scripts can authenticate without an
// OAuth2 browser redirect. The session id goes back as a cookie only; the
// CSRF token needed for later writes comes back in a header so the client
// does not need a second round trip to read it.
func (s *Server) handleAPILocalLogin(w http.ResponseWriter, r *http.Request) {
	if !s.loginFlow.LocalAuthEnabled() {
		writeJSONError(w, apiError{status: http.StatusNotFound, message: "local login is not enabled"})
		return
	}

	var req LoginRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeJSONError(w, err)
		return
	}

	sessionID, err := s.loginFlow.CompleteLocalLogin(r.Context(), LocalLoginRequest{
		Username: strings.TrimSpace(req.Username),
		Password: req.Password,
	})
	if err != nil {
		writeJSONError(w, apiError{status: http.StatusUnauthorized, message: "invalid username or password"})
		return
	}
	csrfToken, err := s.csrfStore.Ensure(sessionID)
	if err != nil {
		writeJSONError(w, err)
		return
	}

	http.SetCookie(w, s.cookie(sessionCookieName, sessionID, s.now().Add(s.sessionTTL)))
	w.Header().Set(csrfHeaderName, csrfToken)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAPISession(w http.ResponseWriter, r *http.Request) {
	session, ok := requestSessionFrom(r.Context())
	if !ok {
		writeJSONError(w, errNoCredentials)
		return
	}
	writeJSON(w, http.StatusOK, SessionResponse{
		Email:              session.Identity.Email,
		UserSpaceName:      session.Identity.UserSpaceName,
		UserSpaceNamespace: session.Identity.UserSpaceNamespace,
		Role:               session.Identity.Role,
		CSRFToken:          session.CSRFToken,
	})
}

func (s *Server) handleAPILogout(w http.ResponseWriter, r *http.Request) {
	session, ok := requestSessionFrom(r.Context())
	if !ok {
		writeJSONError(w, errNoCredentials)
		return
	}
	// A bearer-authenticated caller has no session to end; revoking its token
	// is a DELETE /tokens/{name}, not a logout.
	if session.SessionID == "" {
		writeJSONError(w, apiError{status: http.StatusBadRequest, message: "bearer tokens are revoked, not logged out"})
		return
	}
	if err := s.loginFlow.Logout(session.SessionID); err != nil && !errors.Is(err, ErrSessionNotFound) {
		writeJSONError(w, err)
		return
	}
	s.csrfStore.Delete(session.SessionID)
	s.clearCookie(w, sessionCookieName)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAPIListWorkspaceImages(w http.ResponseWriter, r *http.Request) {
	s.serveAPI(w, r, func(session RequestSession, api RequestAPI) (int, any, error) {
		images, err := api.ListWorkspaceImages(r.Context())
		if err != nil {
			return 0, nil, err
		}
		return http.StatusOK, itemsOf(images), nil
	})
}

func (s *Server) handleAPIGetWorkspaceImage(w http.ResponseWriter, r *http.Request) {
	s.serveAPI(w, r, func(session RequestSession, api RequestAPI) (int, any, error) {
		image, err := api.GetWorkspaceImage(r.Context(), r.PathValue("name"))
		if err != nil {
			return 0, nil, err
		}
		return http.StatusOK, image, nil
	})
}

func (s *Server) handleAPIListImageRegistries(w http.ResponseWriter, r *http.Request) {
	s.serveAPI(w, r, func(session RequestSession, api RequestAPI) (int, any, error) {
		registries, err := api.ListImageRegistries(r.Context())
		if err != nil {
			return 0, nil, err
		}
		return http.StatusOK, itemsOf(registries), nil
	})
}

func (s *Server) handleAPIGetImageRegistry(w http.ResponseWriter, r *http.Request) {
	s.serveAPI(w, r, func(session RequestSession, api RequestAPI) (int, any, error) {
		reg, err := api.GetImageRegistry(r.Context(), r.PathValue("name"))
		if err != nil {
			return 0, nil, err
		}
		return http.StatusOK, reg, nil
	})
}

// handleAPIListWorkspaces scopes by role through the same visibleWorkspaces the
// screens use. It used to list session.Identity.UserSpaceNamespace regardless,
// so an admin or a manager saw fewer workspaces through the API than through
// the browser - two answers to one question, from one identity.
func (s *Server) handleAPIListWorkspaces(w http.ResponseWriter, r *http.Request) {
	s.serveAPI(w, r, func(session RequestSession, api RequestAPI) (int, any, error) {
		workspaces, err := visibleWorkspaces(r.Context(), api, session.Identity)
		if err != nil {
			return 0, nil, err
		}
		return http.StatusOK, itemsOf(filterWorkspaces(workspaces,
			r.URL.Query().Get("namespace"))), nil
	})
}

// filterWorkspaces narrows an already-authorized list. It is a convenience for
// the caller, never a security control: what may be seen was decided by
// visibleWorkspaces and, before that, by the API server.
func filterWorkspaces(workspaces []dwpkv1alpha1.Workspace, namespace string) []dwpkv1alpha1.Workspace {
	if namespace == "" {
		return workspaces
	}
	kept := make([]dwpkv1alpha1.Workspace, 0, len(workspaces))
	for _, ws := range workspaces {
		if ws.Namespace == namespace {
			kept = append(kept, ws)
		}
	}
	return kept
}

func (s *Server) handleAPIGetWorkspace(w http.ResponseWriter, r *http.Request) {
	s.serveAPI(w, r, func(session RequestSession, api RequestAPI) (int, any, error) {
		ws, err := api.GetWorkspace(r.Context(), workspaceNamespace(r, session), r.PathValue("name"))
		if err != nil {
			return 0, nil, err
		}
		return http.StatusOK, ws, nil
	})
}

// workspaceNamespace picks which namespace a single-workspace route acts on.
// Absent a query parameter it is the caller's own, which is what every client
// before role-scoped listing assumed. Naming another namespace is allowed to be
// attempted by anyone: RBAC on the forwarded token decides the outcome, not
// this function (SPEC §8.1).
func workspaceNamespace(r *http.Request, session RequestSession) string {
	if ns := strings.TrimSpace(r.URL.Query().Get("namespace")); ns != "" {
		return ns
	}
	return session.Identity.UserSpaceNamespace
}

func (s *Server) handleAPICreateWorkspace(w http.ResponseWriter, r *http.Request) {
	s.serveAPI(w, r, func(session RequestSession, api RequestAPI) (int, any, error) {
		var req CreateWorkspaceRequest
		if err := decodeJSONBody(r, &req); err != nil {
			return 0, nil, err
		}
		ws, err := buildWorkspace(WorkspaceDraft{
			Name:      strings.TrimSpace(req.Name),
			Namespace: session.Identity.UserSpaceNamespace,
			Image:     strings.TrimSpace(req.Image),
			SSHKey:    strings.TrimSpace(req.SSHPublicKey),
			Resources: ResourceValues{
				CPU:         req.CPU,
				MemoryLimit: req.Memory,
				Storage:     strings.TrimSpace(req.Storage),
				GPU:         req.GPU,
				GPUResource: string(s.gpuResource(r)),
			},
		})
		if err != nil {
			return 0, nil, apiError{status: http.StatusBadRequest, message: err.Error()}
		}
		if err := api.CreateWorkspace(r.Context(), ws); err != nil {
			return 0, nil, err
		}
		return http.StatusCreated, ws, nil
	})
}

// deleteVolumeRequested reports whether the caller wants the workspace's home
// PVC removed along with it. Deleting it is the default - the UI's own
// confirmation dialog defaults to checked for the same reason (an orphaned
// PVC is invisible until it shows up on a bill) - so this only turns it off
// on an explicit "false", never requires opting in.
func deleteVolumeRequested(r *http.Request) bool {
	return !strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("delete_volume")), "false")
}

func (s *Server) handleAPIDeleteWorkspace(w http.ResponseWriter, r *http.Request) {
	s.serveAPI(w, r, func(session RequestSession, api RequestAPI) (int, any, error) {
		namespace, name := workspaceNamespace(r, session), r.PathValue("name")
		if err := api.DeleteWorkspace(r.Context(), namespace, name); err != nil {
			return 0, nil, err
		}
		if deleteVolumeRequested(r) {
			if err := api.DeleteClaim(r.Context(), namespace, workspaceClaimName(name)); err != nil {
				return 0, nil, apiError{
					status: http.StatusConflict,
					message: "the workspace was deleted but its home volume " +
						workspaceClaimName(name) + " was not: " + err.Error(),
				}
			}
		}
		return http.StatusNoContent, nil, nil
	})
}

func (s *Server) handleAPIStartWorkspace(w http.ResponseWriter, r *http.Request) {
	s.patchWorkspaceRunning(w, r, true)
}

func (s *Server) handleAPIStopWorkspace(w http.ResponseWriter, r *http.Request) {
	s.patchWorkspaceRunning(w, r, false)
}

func (s *Server) patchWorkspaceRunning(w http.ResponseWriter, r *http.Request, running bool) {
	s.serveAPI(w, r, func(session RequestSession, api RequestAPI) (int, any, error) {
		ws, err := api.PatchWorkspaceRunning(r.Context(), workspaceNamespace(r, session), r.PathValue("name"), running)
		if err != nil {
			return 0, nil, err
		}
		return http.StatusOK, ws, nil
	})
}

func (s *Server) handleAPIAdminUserSpaces(w http.ResponseWriter, r *http.Request) {
	s.serveAPI(w, r, func(session RequestSession, api RequestAPI) (int, any, error) {
		userSpaces, err := api.ListUserSpaces(r.Context())
		if err != nil {
			return 0, nil, err
		}
		return http.StatusOK, itemsOf(userSpaces), nil
	})
}

// handleAPIAdminUpdateMembership is the REST twin of the admin screen's
// membership form. Authorization is the API server's: the patch goes out
// under the caller's own token.
func (s *Server) handleAPIAdminUpdateMembership(w http.ResponseWriter, r *http.Request) {
	s.serveAPI(w, r, func(_ RequestSession, api RequestAPI) (int, any, error) {
		var req MembershipRequest
		if err := decodeJSONBody(r, &req); err != nil {
			return 0, nil, err
		}
		name := r.PathValue("name")

		// Read first so an omitted field keeps what it had; a merge patch of
		// three fields would otherwise blank the two the caller left out.
		current, err := s.userSpaceByName(r.Context(), api, name)
		if err != nil {
			return 0, nil, err
		}
		membership, err := membershipFromForm(
			name,
			valueOr(req.Role, current.Spec.EffectiveRole()),
			boolText(valueOrBool(req.Disabled, current.Spec.Disabled)),
		)
		if err != nil {
			return 0, nil, err
		}
		if err := api.PatchUserSpaceMembership(r.Context(), membership); err != nil {
			return 0, nil, err
		}
		return http.StatusOK, membership, nil
	})
}

// userSpaceByName finds one UserSpace through the list the caller is already
// allowed to read, so no extra RBAC verb is needed for a single get.
func (s *Server) userSpaceByName(ctx context.Context, api RequestAPI, name string) (*dwpkv1alpha1.UserSpace, error) {
	userSpaces, err := api.ListUserSpaces(ctx)
	if err != nil {
		return nil, err
	}
	for i := range userSpaces {
		if userSpaces[i].Name == name {
			return &userSpaces[i], nil
		}
	}
	return nil, apiError{status: http.StatusNotFound, message: "userspace not found"}
}

func valueOr(value *string, fallback string) string {
	if value == nil {
		return fallback
	}
	return *value
}

func valueOrBool(value *bool, fallback bool) bool {
	if value == nil {
		return fallback
	}
	return *value
}

func boolText(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func (s *Server) handleAPIAdminQuota(w http.ResponseWriter, r *http.Request) {
	s.serveAPI(w, r, func(session RequestSession, api RequestAPI) (int, any, error) {
		userSpaces, err := api.ListUserSpaces(r.Context())
		if err != nil {
			return 0, nil, err
		}
		workspaces, err := api.ListWorkspaces(r.Context(), "")
		if err != nil {
			return 0, nil, err
		}
		return http.StatusOK, itemsOf(quotaRows(userSpaces, workspaces, s.gpuResource(r))), nil
	})
}

func (s *Server) handleAPIListTokens(w http.ResponseWriter, r *http.Request) {
	s.serveAPI(w, r, func(session RequestSession, _ RequestAPI) (int, any, error) {
		if s.apiTokens == nil {
			return 0, nil, errAPITokensDisabled
		}
		records, err := s.apiTokens.List(r.Context(), auth.TokenKindApplication, session.Identity.UserSpaceNamespace)
		if err != nil {
			return 0, nil, err
		}
		responses := make([]TokenResponse, 0, len(records))
		for _, record := range records {
			responses = append(responses, tokenResponse(record, false))
		}
		return http.StatusOK, itemsOf(responses), nil
	})
}

func (s *Server) handleAPIIssueToken(w http.ResponseWriter, r *http.Request) {
	s.serveAPI(w, r, func(session RequestSession, _ RequestAPI) (int, any, error) {
		if s.apiTokens == nil {
			return 0, nil, errAPITokensDisabled
		}
		// The scope picks which ServiceAccount the token mints for, and that
		// account's RBAC is the whole enforcement. Nothing here checks the scope
		// on later requests, because nothing here should: the API server does.
		scope := workspace.TokenScope(strings.TrimSpace(r.URL.Query().Get("scope")))
		if body := issueScopeFromBody(r); body != "" {
			scope = body
		}
		record, err := s.apiTokens.Issue(r.Context(), auth.TokenGrant{
			Kind:                  auth.TokenKindApplication,
			SubjectNamespace:      session.Identity.UserSpaceNamespace,
			SubjectServiceAccount: workspace.ServiceAccountForScope(scope),
			ExpiresAt:             tokenExpiryFrom(r.URL.Query().Get("expires")),
		})
		if err != nil {
			return 0, nil, err
		}
		return http.StatusCreated, tokenResponse(record, true), nil
	})
}

// issueScopeFromBody reads an optional {"scope": "..."} body. A token request
// with no body at all is the common case, so a decode failure is not an error -
// it just means no scope was named, and the default is the narrower one.
func issueScopeFromBody(r *http.Request) workspace.TokenScope {
	var req struct {
		Scope string `json:"scope"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return ""
	}
	return workspace.TokenScope(strings.TrimSpace(req.Scope))
}

// handleAPIRevokeToken refuses to revoke a token belonging to another
// namespace: the Secrets all live in one namespace the caller cannot see, so
// without this check a token name from anywhere would be deletable by anyone.
func (s *Server) handleAPIRevokeToken(w http.ResponseWriter, r *http.Request) {
	s.serveAPI(w, r, func(session RequestSession, _ RequestAPI) (int, any, error) {
		if s.apiTokens == nil {
			return 0, nil, errAPITokensDisabled
		}
		name := r.PathValue("name")
		records, err := s.apiTokens.List(r.Context(), auth.TokenKindApplication, session.Identity.UserSpaceNamespace)
		if err != nil {
			return 0, nil, err
		}
		for _, record := range records {
			if record.SecretName == name {
				if err := s.apiTokens.Revoke(r.Context(), name); err != nil {
					return 0, nil, err
				}
				return http.StatusNoContent, nil, nil
			}
		}
		return 0, nil, apiError{status: http.StatusNotFound, message: "token not found"}
	})
}

func (s *Server) handleAPIListLocalUsers(w http.ResponseWriter, r *http.Request) {
	s.serveAdminAPI(w, r, func(_ RequestSession) (int, any, error) {
		users, err := s.localUsers.List(r.Context())
		if err != nil {
			return 0, nil, err
		}
		responses := make([]LocalUserResponse, 0, len(users))
		for _, user := range users {
			responses = append(responses, localUserResponse(user))
		}
		return http.StatusOK, itemsOf(responses), nil
	})
}

func (s *Server) handleAPICreateLocalUser(w http.ResponseWriter, r *http.Request) {
	s.serveAdminAPI(w, r, func(_ RequestSession) (int, any, error) {
		var req LocalUserRequest
		if err := decodeJSONBody(r, &req); err != nil {
			return 0, nil, err
		}
		user, err := s.localUsers.Create(r.Context(), strings.TrimSpace(req.Username), req.Password, strings.TrimSpace(req.Owner))
		if err != nil {
			if errors.Is(err, auth.ErrLocalUserExists) {
				return 0, nil, apiError{status: http.StatusConflict, message: err.Error()}
			}
			return 0, nil, apiError{status: http.StatusBadRequest, message: err.Error()}
		}
		return http.StatusCreated, localUserResponse(user), nil
	})
}

func (s *Server) handleAPIDeleteLocalUser(w http.ResponseWriter, r *http.Request) {
	s.serveAdminAPI(w, r, func(_ RequestSession) (int, any, error) {
		if err := s.localUsers.Delete(r.Context(), r.PathValue("name")); err != nil {
			return 0, nil, err
		}
		return http.StatusNoContent, nil, nil
	})
}

// serveAPI resolves the session, builds a Kubernetes client under the
// caller's own credential, and renders whatever the handler returns as JSON.
// Every authorization decision therefore stays with the API server: the UI
// holds no standing permission of its own (§8.1).
func (s *Server) serveAPI(w http.ResponseWriter, r *http.Request, handle func(RequestSession, RequestAPI) (int, any, error)) {
	session, ok := requestSessionFrom(r.Context())
	if !ok {
		writeJSONError(w, errNoCredentials)
		return
	}
	api, err := s.clientFactory.ForToken(session.Token)
	if err != nil {
		writeJSONError(w, err)
		return
	}
	status, body, err := handle(session, api)
	if err != nil {
		writeJSONError(w, err)
		return
	}
	writeJSON(w, status, body)
}

// serveAdminAPI gates local-user management on the caller's own RBAC rather
// than on a flag the UI trusts: the same SelfSubjectAccessReview the admin
// screens use decides whether this caller is an administrator.
func (s *Server) serveAdminAPI(w http.ResponseWriter, r *http.Request, handle func(RequestSession) (int, any, error)) {
	s.serveAPI(w, r, func(session RequestSession, api RequestAPI) (int, any, error) {
		if s.localUsers == nil {
			return 0, nil, errLocalUsersDisabled
		}
		allowed, err := api.CanI(r.Context(), "delete", "userspaces", "")
		if err != nil {
			return 0, nil, err
		}
		if !allowed {
			return 0, nil, apiError{status: http.StatusForbidden, message: "administrator privileges required"}
		}
		return handle(session)
	})
}

func decodeJSONBody(r *http.Request, target any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxJSONBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return apiError{status: http.StatusBadRequest, message: "invalid JSON body: " + err.Error()}
	}
	return nil
}

// maxJSONBodyBytes caps request bodies so a malformed or hostile client
// cannot make the UI buffer an unbounded amount of memory.
const maxJSONBodyBytes = 1 << 20

func itemsOf[T any](items []T) map[string]any {
	if items == nil {
		items = []T{}
	}
	return map[string]any{"items": items}
}

func tokenResponse(record auth.TokenRecord, includePlaintext bool) TokenResponse {
	response := TokenResponse{
		Name:      record.SecretName,
		Namespace: record.SubjectNamespace,
		CreatedAt: record.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
	}
	if includePlaintext {
		response.Token = record.Plaintext
	}
	return response
}

func localUserResponse(user auth.LocalUser) LocalUserResponse {
	return LocalUserResponse{Name: user.SecretName, Username: user.Username, Owner: user.Owner}
}
