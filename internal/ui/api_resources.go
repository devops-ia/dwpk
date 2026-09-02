package ui

import (
	"io"
	"net/http"
	"strconv"
	"strings"

	dwpkv1alpha1 "github.com/devops-ia/dwpk/api/v1alpha1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// The endpoints here bring the REST surface level with what the screens can do.
// Every one maps to a single Kubernetes verb on a single object, through the
// caller's own forwarded token - so a person who cannot delete a project gets
// the API server's own 403 here exactly as they do in the browser (SPEC §8.4).
//
// PATCH bodies use pointer fields throughout: an omitted key keeps its current
// value rather than being reset to a zero, which is the convention
// MembershipRequest already set.

// WorkspaceImageRequest is the editable half of a catalog entry. metadata.name
// is create-only: a rename is a delete and a recreate, and pretending otherwise
// in a PATCH would silently do nothing.
type WorkspaceImageRequest struct {
	Name        string   `json:"name,omitempty"`
	Image       string   `json:"image,omitempty"`
	DisplayName string   `json:"display_name,omitempty"`
	Description string   `json:"description,omitempty"`
	Icon        string   `json:"icon,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Deprecated  bool     `json:"deprecated,omitempty"`
}

// ImageRegistryRequest is the editable half of an ImageRegistry. Every field
// but Name is a pointer: an omitted key on PATCH keeps its current value,
// the same convention QuotaRequest already set (and, unlike
// WorkspaceImageRequest, followed correctly here from the start).
type ImageRegistryRequest struct {
	Name            string    `json:"name,omitempty"`
	Region          *string   `json:"region,omitempty"`
	RegistryID      *string   `json:"registry_id,omitempty"`
	RoleARN         *string   `json:"role_arn,omitempty"`
	IntervalSeconds *int32    `json:"interval_seconds,omitempty"`
	Include         *[]string `json:"include,omitempty"`
	Exclude         *[]string `json:"exclude,omitempty"`
	TagMode         *string   `json:"tag_mode,omitempty"`
	TagPatterns     *[]string `json:"tag_patterns,omitempty"`
	TagLimit        *int32    `json:"tag_limit,omitempty"`
	Prune           *bool     `json:"prune,omitempty"`
	ImagePullSecret *string   `json:"image_pull_secret,omitempty"`
}

type UserSpaceRequest struct {
	Name  string `json:"name"`
	Owner string `json:"owner"`
	Role  string `json:"role,omitempty"`
}

type QuotaRequest struct {
	Workspaces *int32  `json:"workspaces,omitempty"`
	CPU        *string `json:"cpu,omitempty"`
	Memory     *string `json:"memory,omitempty"`
	Storage    *string `json:"storage,omitempty"`
	GPU        *int32  `json:"gpu,omitempty"`
}

type PasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

// LogsResponse returns the tail as one string rather than a list of lines. A
// log is not a list: splitting it here would force every client to rejoin it,
// and would have to pick a line ending on their behalf.
type LogsResponse struct {
	Namespace string `json:"namespace"`
	Pod       string `json:"pod"`
	Lines     string `json:"lines"`
}

func (s *Server) resourceRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET "+apiVersion+"/workspaces/{name}/logs", s.handleAPIWorkspaceLogs)

	mux.HandleFunc("POST "+apiVersion+"/workspace-images", s.handleAPICreateWorkspaceImage)
	mux.HandleFunc("PATCH "+apiVersion+"/workspace-images/{name}", s.handleAPIPatchWorkspaceImage)
	mux.HandleFunc("DELETE "+apiVersion+"/workspace-images/{name}", s.handleAPIDeleteWorkspaceImage)

	mux.HandleFunc("POST "+apiVersion+"/image-registries", s.handleAPICreateImageRegistry)
	mux.HandleFunc("PATCH "+apiVersion+"/image-registries/{name}", s.handleAPIPatchImageRegistry)
	mux.HandleFunc("DELETE "+apiVersion+"/image-registries/{name}", s.handleAPIDeleteImageRegistry)
	mux.HandleFunc("POST "+apiVersion+"/image-registries/{name}/force-sync", s.handleAPIForceSyncImageRegistry)

	mux.HandleFunc("POST "+apiVersion+"/admin/userspaces", s.handleAPICreateUserSpace)
	mux.HandleFunc("DELETE "+apiVersion+"/admin/userspaces/{name}", s.handleAPIDeleteUserSpace)
	mux.HandleFunc("PATCH "+apiVersion+"/admin/quota/{name}", s.handleAPIPatchQuota)

	mux.HandleFunc("POST "+apiVersion+"/profile/password", s.handleAPIChangePassword)
}

// defaultAPILogTail matches what the Logs tab asks for, so the two surfaces
// return the same thing unless a caller says otherwise.
const defaultAPILogTail = 200

func (s *Server) handleAPIWorkspaceLogs(w http.ResponseWriter, r *http.Request) {
	s.serveAPI(w, r, func(session RequestSession, api RequestAPI) (int, any, error) {
		namespace := workspaceNamespace(r, session)
		ws, err := api.GetWorkspace(r.Context(), namespace, r.PathValue("name"))
		if err != nil {
			return 0, nil, err
		}
		if ws.Status.PodName == "" {
			return 0, nil, apiError{
				status:  http.StatusConflict,
				message: "workspace has no running pod; start it first",
			}
		}
		tail := int64(defaultAPILogTail)
		if raw := r.URL.Query().Get("tail"); raw != "" {
			parsed, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || parsed <= 0 {
				return 0, nil, apiError{status: http.StatusBadRequest, message: "tail must be a positive integer"}
			}
			tail = parsed
		}
		stream, err := api.WorkspaceLogs(r.Context(), namespace, ws.Status.PodName, tail)
		if err != nil {
			return 0, nil, err
		}
		defer stream.Close() //nolint:errcheck // a read-only stream

		lines, err := io.ReadAll(stream)
		if err != nil {
			return 0, nil, err
		}
		return http.StatusOK, LogsResponse{
			Namespace: namespace,
			Pod:       ws.Status.PodName,
			Lines:     string(lines),
		}, nil
	})
}

func (s *Server) handleAPICreateWorkspaceImage(w http.ResponseWriter, r *http.Request) {
	s.serveAPI(w, r, func(_ RequestSession, api RequestAPI) (int, any, error) {
		var req WorkspaceImageRequest
		if err := decodeJSONBody(r, &req); err != nil {
			return 0, nil, err
		}
		if strings.TrimSpace(req.Name) == "" || strings.TrimSpace(req.Image) == "" {
			return 0, nil, apiError{status: http.StatusBadRequest, message: "name and image are required"}
		}
		image := &dwpkv1alpha1.WorkspaceImage{}
		image.Name = strings.TrimSpace(req.Name)
		image.Spec.Image = strings.TrimSpace(req.Image)
		image.Spec.DisplayName = req.DisplayName
		image.Spec.Description = req.Description
		image.Spec.Icon = req.Icon
		image.Spec.Tags = req.Tags
		image.Spec.Deprecated = req.Deprecated
		if err := api.CreateWorkspaceImage(r.Context(), image); err != nil {
			return 0, nil, err
		}
		return http.StatusCreated, image, nil
	})
}

func (s *Server) handleAPIPatchWorkspaceImage(w http.ResponseWriter, r *http.Request) {
	s.serveAPI(w, r, func(_ RequestSession, api RequestAPI) (int, any, error) {
		var req WorkspaceImageRequest
		if err := decodeJSONBody(r, &req); err != nil {
			return 0, nil, err
		}
		edit := WorkspaceImageEdit{
			Name:        r.PathValue("name"),
			Image:       strings.TrimSpace(req.Image),
			DisplayName: req.DisplayName,
			Description: req.Description,
			Icon:        req.Icon,
			Tags:        req.Tags,
			Deprecated:  req.Deprecated,
		}
		if err := api.PatchWorkspaceImage(r.Context(), edit); err != nil {
			return 0, nil, err
		}
		return http.StatusOK, edit, nil
	})
}

// apiSizes converts the request's flat size triples into the CRD's shape.
// Limits are set equal to requests, matching the admin screen: a workspace that
// can burst past the size it was given makes the namespace quota a fiction.
func (s *Server) handleAPIDeleteWorkspaceImage(w http.ResponseWriter, r *http.Request) {
	s.serveAPI(w, r, func(_ RequestSession, api RequestAPI) (int, any, error) {
		if err := api.DeleteWorkspaceImage(r.Context(), r.PathValue("name")); err != nil {
			return 0, nil, err
		}
		return http.StatusNoContent, nil, nil
	})
}

func (s *Server) handleAPICreateImageRegistry(w http.ResponseWriter, r *http.Request) {
	s.serveAPI(w, r, func(_ RequestSession, api RequestAPI) (int, any, error) {
		var req ImageRegistryRequest
		if err := decodeJSONBody(r, &req); err != nil {
			return 0, nil, err
		}
		name := strings.TrimSpace(req.Name)
		if name == "" || req.Region == nil || strings.TrimSpace(*req.Region) == "" {
			return 0, nil, apiError{status: http.StatusBadRequest, message: "name and region are required"}
		}
		reg := &dwpkv1alpha1.ImageRegistry{}
		reg.Name = name
		reg.Spec.Provider = dwpkv1alpha1.ImageRegistryProviderAWSECR
		reg.Spec.AWS = &dwpkv1alpha1.AWSRegistry{Region: strings.TrimSpace(*req.Region)}
		if req.RegistryID != nil {
			reg.Spec.AWS.RegistryID = *req.RegistryID
		}
		if req.RoleARN != nil {
			reg.Spec.AWS.RoleARN = *req.RoleARN
		}
		reg.Spec.Sync = imageRegistrySyncFromRequest(dwpkv1alpha1.RegistrySync{}, req)
		if req.ImagePullSecret != nil {
			reg.Spec.ImagePullSecretRef = pullSecretRefOrNil(*req.ImagePullSecret)
		}
		if err := api.CreateImageRegistry(r.Context(), reg); err != nil {
			return 0, nil, err
		}
		return http.StatusCreated, reg, nil
	})
}

func (s *Server) handleAPIPatchImageRegistry(w http.ResponseWriter, r *http.Request) {
	s.serveAPI(w, r, func(_ RequestSession, api RequestAPI) (int, any, error) {
		var req ImageRegistryRequest
		if err := decodeJSONBody(r, &req); err != nil {
			return 0, nil, err
		}
		name := r.PathValue("name")
		reg, err := api.GetImageRegistry(r.Context(), name)
		if err != nil {
			return 0, nil, err
		}
		region, registryID, roleARN := "", "", ""
		if reg.Spec.AWS != nil {
			region, registryID, roleARN = reg.Spec.AWS.Region, reg.Spec.AWS.RegistryID, reg.Spec.AWS.RoleARN
		}
		if req.Region != nil {
			region = *req.Region
		}
		if req.RegistryID != nil {
			registryID = *req.RegistryID
		}
		if req.RoleARN != nil {
			roleARN = *req.RoleARN
		}
		sync := imageRegistrySyncFromRequest(reg.Spec.Sync, req)
		pullSecret := pullSecretName(reg.Spec.ImagePullSecretRef)
		if req.ImagePullSecret != nil {
			pullSecret = *req.ImagePullSecret
		}
		edit := ImageRegistryEdit{
			Name:               name,
			Region:             region,
			RegistryID:         registryID,
			RoleARN:            roleARN,
			IntervalSeconds:    sync.IntervalSeconds,
			Include:            sync.Include,
			Exclude:            sync.Exclude,
			TagMode:            sync.Tags.Mode,
			TagPatterns:        sync.Tags.Patterns,
			TagLimit:           sync.Tags.Limit,
			Prune:              sync.Prune,
			ImagePullSecretRef: pullSecretRefOrNil(pullSecret),
		}
		if err := api.PatchImageRegistry(r.Context(), edit); err != nil {
			return 0, nil, err
		}
		return http.StatusOK, edit, nil
	})
}

// imageRegistrySyncFromRequest overlays the request's pointer fields onto the
// registry's current sync config, so a PATCH omitting a field keeps it.
func imageRegistrySyncFromRequest(current dwpkv1alpha1.RegistrySync, req ImageRegistryRequest) dwpkv1alpha1.RegistrySync {
	sync := current
	if req.IntervalSeconds != nil {
		sync.IntervalSeconds = *req.IntervalSeconds
	}
	if req.Include != nil {
		sync.Include = *req.Include
	}
	if req.Exclude != nil {
		sync.Exclude = *req.Exclude
	}
	if req.TagMode != nil {
		sync.Tags.Mode = *req.TagMode
	}
	if req.TagPatterns != nil {
		sync.Tags.Patterns = *req.TagPatterns
	}
	if req.TagLimit != nil {
		sync.Tags.Limit = *req.TagLimit
	}
	if req.Prune != nil {
		sync.Prune = *req.Prune
	}
	return sync
}

func (s *Server) handleAPIDeleteImageRegistry(w http.ResponseWriter, r *http.Request) {
	s.serveAPI(w, r, func(_ RequestSession, api RequestAPI) (int, any, error) {
		if err := api.DeleteImageRegistry(r.Context(), r.PathValue("name")); err != nil {
			return 0, nil, err
		}
		return http.StatusNoContent, nil, nil
	})
}

func (s *Server) handleAPIForceSyncImageRegistry(w http.ResponseWriter, r *http.Request) {
	s.serveAPI(w, r, func(_ RequestSession, api RequestAPI) (int, any, error) {
		if err := api.ForceSyncImageRegistry(r.Context(), r.PathValue("name")); err != nil {
			return 0, nil, err
		}
		return http.StatusAccepted, nil, nil
	})
}

func (s *Server) handleAPICreateUserSpace(w http.ResponseWriter, r *http.Request) {
	s.serveAPI(w, r, func(_ RequestSession, api RequestAPI) (int, any, error) {
		var req UserSpaceRequest
		if err := decodeJSONBody(r, &req); err != nil {
			return 0, nil, err
		}
		if strings.TrimSpace(req.Name) == "" || strings.TrimSpace(req.Owner) == "" {
			return 0, nil, apiError{status: http.StatusBadRequest, message: "name and owner are required"}
		}
		userSpace := &dwpkv1alpha1.UserSpace{}
		userSpace.Name = strings.TrimSpace(req.Name)
		userSpace.Spec.Owner = strings.TrimSpace(req.Owner)
		userSpace.Spec.Role = strings.TrimSpace(req.Role)
		if err := api.CreateUserSpace(r.Context(), userSpace); err != nil {
			return 0, nil, err
		}
		return http.StatusCreated, userSpace, nil
	})
}

func (s *Server) handleAPIDeleteUserSpace(w http.ResponseWriter, r *http.Request) {
	s.serveAPI(w, r, func(_ RequestSession, api RequestAPI) (int, any, error) {
		if err := api.DeleteUserSpace(r.Context(), r.PathValue("name")); err != nil {
			return 0, nil, err
		}
		return http.StatusNoContent, nil, nil
	})
}

func (s *Server) handleAPIPatchQuota(w http.ResponseWriter, r *http.Request) {
	s.serveAPI(w, r, func(_ RequestSession, api RequestAPI) (int, any, error) {
		var req QuotaRequest
		if err := decodeJSONBody(r, &req); err != nil {
			return 0, nil, err
		}
		name := r.PathValue("name")
		userSpace, err := api.GetUserSpace(r.Context(), name)
		if err != nil {
			return 0, nil, err
		}
		quota := userSpace.Spec.Quota
		if req.Workspaces != nil {
			quota.Workspaces = *req.Workspaces
		}
		if err := applyQuantity(req.CPU, &quota.CPU, "cpu"); err != nil {
			return 0, nil, err
		}
		if err := applyQuantity(req.Memory, &quota.Memory, "memory"); err != nil {
			return 0, nil, err
		}
		if err := applyQuantity(req.Storage, &quota.Storage, "storage"); err != nil {
			return 0, nil, err
		}
		if req.GPU != nil {
			quota.GPU = *req.GPU
		}
		if err := api.PatchUserSpaceQuota(r.Context(), name, quota); err != nil {
			return 0, nil, err
		}
		return http.StatusOK, quota, nil
	})
}

func applyQuantity(raw *string, target *resource.Quantity, field string) error {
	if raw == nil {
		return nil
	}
	quantity, err := resource.ParseQuantity(strings.TrimSpace(*raw))
	if err != nil {
		return apiError{status: http.StatusBadRequest, message: field + ": " + err.Error()}
	}
	*target = quantity
	return nil
}

func (s *Server) handleAPIChangePassword(w http.ResponseWriter, r *http.Request) {
	s.serveAPI(w, r, func(session RequestSession, _ RequestAPI) (int, any, error) {
		if s.localUsers == nil {
			return 0, nil, errLocalUsersDisabled
		}
		// The username comes from the session's owner, never from the body: a
		// posted username would let anyone change anyone's password. Resolved
		// before the body is read, so a caller with no password here gets the
		// same answer whatever they sent.
		username := s.localUsernameFor(r, session.Identity)
		if username == "" {
			return 0, nil, apiError{
				status:  http.StatusConflict,
				message: "this account has no password to change here",
			}
		}
		var req PasswordRequest
		if err := decodeJSONBody(r, &req); err != nil {
			return 0, nil, err
		}
		if err := s.localUsers.SetPassword(r.Context(), username, req.CurrentPassword, req.NewPassword); err != nil {
			return 0, nil, apiError{status: http.StatusBadRequest, message: err.Error()}
		}
		return http.StatusNoContent, nil, nil
	})
}
