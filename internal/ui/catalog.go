package ui

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	dwpkv1alpha1 "github.com/devops-ia/dwpk/api/v1alpha1"
	"github.com/devops-ia/dwpk/internal/registry"
)

// maxIconBytes caps what the icon proxy will relay. The URL comes from a
// catalog entry an administrator wrote, and is fetched by the UI pod, so an
// unbounded copy is an unbounded memory cost triggered by anyone loading the
// catalog.
const maxIconBytes = 512 << 10

func (s *Server) handleCatalog(w http.ResponseWriter, r *http.Request) {
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
	images, err := api.ListWorkspaceImages(r.Context())
	if err != nil {
		s.writeErrorPage(w, r, err)
		return
	}
	usable, err := usableImageNames(r.Context(), api, images)
	if err != nil {
		s.writeErrorPage(w, r, err)
		return
	}

	showDeprecated := r.URL.Query().Get("deprecated") == "1"
	text := strings.TrimSpace(r.URL.Query().Get("q"))
	tag := strings.TrimSpace(r.URL.Query().Get("tag"))

	now := time.Now()
	cards := make([]ImageCard, 0, len(images))
	for i := range images {
		image := &images[i]
		if !usable(image.Name) || !catalogEntryVisible(image, text, tag, showDeprecated, now) {
			continue
		}
		cards = append(cards, ImageCard{
			Name:        image.Name,
			DisplayName: image.Title(),
			Description: image.Spec.Description,
			IconPath:    iconPath(s.basePath, image.Name, image.Spec.Icon),
			Tags:        slices.Clone(image.Spec.Tags),
			Deprecated:  image.IsDeprecated(now),
			// The notice is a sentence, not a count: gating a badge on "days
			// remaining > 0" hid it on the last day, when it mattered most.
			DeprecationNotice: func() string {
				notice, _ := image.DeprecationNotice(now)
				return notice
			}(),
			Origin:          registry.OriginOf(image.Spec.Image),
			Image:           image.Spec.Image,
			ImagePullPolicy: string(image.Spec.ImagePullPolicy),
			Shell:           image.Spec.Shell,
			HomePath:        image.Spec.HomePath,
			RunAs:           runAsSummary(image),
			Command:         strings.Join(image.Spec.Command, " "),
			Maintainer:      image.Spec.Maintainer,
			DeprecateAt:     dateValue(image.Spec.DeprecateAt),
			Placement:       placementSummary(image.Spec.Placement),
		})
	}
	slices.SortFunc(cards, func(a, b ImageCard) int { return strings.Compare(a.DisplayName, b.DisplayName) })

	data := CatalogData{
		Session:        session,
		Page:           paginateWith(cards, r, catalogPageSizes),
		Request:        r,
		Text:           text,
		Tag:            tag,
		Tags:           catalogTags(images),
		ShowDeprecated: showDeprecated,
	}

	// The live search swaps the grid alone. Re-rendering the filter form too
	// would move focus out of the field the user is typing in.
	if r.Header.Get("HX-Request") == boolTrue {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = CatalogGrid(data).Render(s.renderContext(r), w)
		return
	}
	s.renderAuthedPage(w, r, http.StatusOK, session, "Catalog", CatalogPage(data))
}

// usableImageNames answers "may this caller use image X" for the whole catalog.
//
// It asks once, unnamed, and only falls back to one question per image when
// that is refused. The old code was a SelfSubjectAccessReview per image per
// render, which a keystroke-triggered search would have multiplied by every
// keypress.
func usableImageNames(ctx context.Context, api RequestAPI, images []dwpkv1alpha1.WorkspaceImage) (func(string) bool, error) {
	all, err := api.CanI(ctx, "use", "workspaceimages", "")
	if err != nil {
		return nil, err
	}
	if all {
		return func(string) bool { return true }, nil
	}

	// A name-scoped grant still has to be honoured, so this is the slow path
	// rather than a flat refusal.
	allowed := make(map[string]bool, len(images))
	for i := range images {
		ok, err := api.CanUseWorkspaceImage(ctx, images[i].Name)
		if err != nil {
			return nil, err
		}
		allowed[images[i].Name] = ok
	}
	return func(name string) bool { return allowed[name] }, nil
}

func (s *Server) handleWorkspaceImageIcon(w http.ResponseWriter, r *http.Request) {
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
	allowed, err := api.CanUseWorkspaceImage(r.Context(), r.PathValue("name"))
	if err != nil {
		s.writeErrorPage(w, r, err)
		return
	}
	if !allowed {
		s.writeErrorPage(w, r, apiError{status: http.StatusForbidden, message: "you are not authorised to use this workspace image"})
		return
	}
	image, err := api.GetWorkspaceImage(r.Context(), r.PathValue("name"))
	if err != nil {
		s.writeErrorPage(w, r, err)
		return
	}
	iconURL := strings.TrimSpace(image.Spec.Icon)
	if iconURL == "" {
		http.NotFound(w, r)
		return
	}
	if err := checkIconURL(iconURL); err != nil {
		s.writeErrorPage(w, r, err)
		return
	}

	request, err := http.NewRequestWithContext(r.Context(), http.MethodGet, iconURL, nil)
	if err != nil {
		s.writeErrorPage(w, r, err)
		return
	}
	response, err := s.iconHTTPClient.Do(request)
	if err != nil {
		s.writeErrorPage(w, r, err)
		return
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		s.writeErrorPage(w, r, apiError{status: response.StatusCode, message: response.Status})
		return
	}

	contentType := response.Header.Get("Content-Type")
	if !strings.HasPrefix(contentType, "image/") {
		s.writeErrorPage(w, r, apiError{
			status:  http.StatusBadGateway,
			message: "the icon URL did not return an image",
		})
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, io.LimitReader(response.Body, maxIconBytes))
}

// checkIconURL refuses what this proxy will not fetch.
//
// The URL is written by whoever can edit a catalog entry and is fetched from
// inside the UI pod, which sits on the cluster network. Without a scheme
// allowlist that is a request forgery primitive: file://, and http:// to a
// link-local metadata address, both reach places a browser never could.
func checkIconURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return apiError{status: http.StatusBadRequest, message: "the icon URL is not a URL"}
	}
	if parsed.Scheme != "https" {
		return apiError{status: http.StatusBadRequest, message: "icon URLs must be https"}
	}
	if parsed.Host == "" {
		return apiError{status: http.StatusBadRequest, message: "the icon URL has no host"}
	}
	return nil
}

// catalogEntryVisible is workspaceImageVisible with the scheduled date folded
// in, so an entry whose date has passed hides exactly as a flagged one does.
func catalogEntryVisible(
	image *dwpkv1alpha1.WorkspaceImage, text, tag string, showDeprecated bool, now time.Time,
) bool {
	if image.IsDeprecated(now) && !showDeprecated {
		return false
	}
	// The flag itself is already handled above; pass true so the shared
	// predicate does not hide a dated entry a second time by a different rule.
	return workspaceImageVisible(*image, text, tag, true)
}

// runAsSummary says who the container runs as in one line, because "uid 1000"
// and "root, with privilege escalation" are the same field answered two ways.
func runAsSummary(image *dwpkv1alpha1.WorkspaceImage) string {
	if image.Spec.AllowRoot {
		return "root (uid 0), privilege escalation allowed"
	}
	return fmt.Sprintf("uid %d", image.Spec.RunAsUser)
}

// placementSummary flattens the scheduling constraints for a read-only list.
// Empty means "anywhere", which the dialog then omits rather than printing.
func placementSummary(placement *dwpkv1alpha1.WorkspacePlacement) string {
	if placement == nil {
		return ""
	}
	parts := []string{}
	for key, value := range placement.NodeSelector {
		parts = append(parts, key+"="+value)
	}
	slices.Sort(parts)
	if len(placement.Tolerations) > 0 {
		parts = append(parts, fmt.Sprintf("%d toleration(s)", len(placement.Tolerations)))
	}
	if placement.Affinity != nil {
		parts = append(parts, "affinity rules")
	}
	return strings.Join(parts, ", ")
}
