package ui

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	dwpkv1alpha1 "github.com/devops-ia/dwpk/api/v1alpha1"
	"github.com/devops-ia/dwpk/internal/registry"
)

// CatalogEntryRow is one WorkspaceImage as the admin screen edits it.
type CatalogEntryRow struct {
	Name        string
	DisplayName string
	// Title is DisplayName or, when it is blank, the object name.
	Title       string
	Description string
	Image       string
	Icon        string
	// IconPath is the same-origin proxy path the summary's <img> loads from -
	// the CSP's img-src is 'self', so the raw external Icon URL can't be used
	// as a src directly (see catalog.go's iconPath/checkIconURL for the
	// user-facing catalog, which this mirrors).
	IconPath   string
	Tags       string
	Deprecated bool
	AllowRoot  bool
	Enabled    bool
	// DeprecateAt is the scheduled date as yyyy-mm-dd, or "" when none is set -
	// the format an <input type="date"> both renders and submits.
	DeprecateAt string
	// DeprecationNotice is the warning sentence while a scheduled date is still
	// ahead, and empty when there is nothing to warn about.
	DeprecationNotice string
	// Origin is the cloud or registry the image reference belongs to, derived
	// from its host - "AWS", "Docker Hub", and so on.
	Origin string
	// ManagedBy is the ImageRegistry that synced this entry, or "" for one
	// created by hand. A synced entry is overwritten on that registry's next
	// sync, so the form warns rather than pretending an edit here will stick.
	ManagedBy string
	// ImagePullSecret names a pull Secret for a private image, or "" for none.
	ImagePullSecret string
}

// AdminCatalogData backs GET /admin/catalog.
type AdminCatalogData struct {
	Session RequestSession
	Rows    []CatalogEntryRow
	// Tags are every tag in use, offered as suggestions while typing.
	Tags []string
	// Text is the current filter, echoed back so the form keeps what was typed.
	Text   string
	Error  string
	Notice string
	// RegistryNames offers every configured ImageRegistry as a filter option -
	// managing a registry itself lives on its own page (§UI: Registries).
	RegistryNames []string
	// RegistryFilter is the currently selected registry name, or "" for every
	// entry regardless of who synced it.
	RegistryFilter string
}

func (s *Server) handleAdminCatalog(w http.ResponseWriter, r *http.Request) {
	session, ok := s.requireUserSpaceAdmin(w, r)
	if !ok {
		return
	}
	s.renderCatalogAdmin(w, r, session, "")
}

func (s *Server) renderCatalogAdmin(w http.ResponseWriter, r *http.Request, session RequestSession, message string) {
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
	registries, err := api.ListImageRegistries(r.Context())
	if err != nil {
		s.writeErrorPage(w, r, err)
		return
	}

	// Filters. The predicate is workspaceImageVisible - the same one the
	// user-facing catalog uses - so an administrator searching for an entry and
	// a user browsing for one agree about what matches.
	text := strings.TrimSpace(r.URL.Query().Get("q"))
	registryFilter := strings.TrimSpace(r.URL.Query().Get("registry"))

	now := time.Now()
	rows := make([]CatalogEntryRow, 0, len(images))
	for i := range images {
		image := &images[i]
		// showDeprecated is true here where it is false on the user-facing
		// catalog: an administrator managing the list has to see the entries
		// they have deprecated, or they cannot un-deprecate them.
		if !workspaceImageVisible(*image, text, "", true) {
			continue
		}
		if registryFilter != "" && image.Labels[dwpkv1alpha1.ImageRegistryLabel] != registryFilter {
			continue
		}
		notice, _ := image.DeprecationNotice(now)
		rows = append(rows, CatalogEntryRow{
			Name:              image.Name,
			DisplayName:       image.Spec.DisplayName,
			Title:             image.Title(),
			Description:       image.Spec.Description,
			Image:             image.Spec.Image,
			Icon:              image.Spec.Icon,
			IconPath:          iconPath(s.basePath, image.Name, image.Spec.Icon),
			Tags:              strings.Join(image.Spec.Tags, ", "),
			Deprecated:        image.IsDeprecated(now),
			AllowRoot:         image.Spec.AllowRoot,
			DeprecateAt:       dateValue(image.Spec.DeprecateAt),
			DeprecationNotice: notice,
			Origin:            registry.OriginOf(image.Spec.Image),
			ManagedBy:         image.Labels[dwpkv1alpha1.ImageRegistryLabel],
			ImagePullSecret:   pullSecretName(image.Spec.ImagePullSecretRef),
		})
	}
	slices.SortFunc(rows, func(a, b CatalogEntryRow) int { return strings.Compare(a.Name, b.Name) })

	status := http.StatusOK
	if message != "" {
		status = http.StatusBadRequest
	}
	s.renderAuthedPage(w, r, status, session, "Catalog", AdminCatalogPage(AdminCatalogData{
		Session:        session,
		Rows:           rows,
		Tags:           catalogTags(images),
		Text:           text,
		Error:          message,
		Notice:         doneNotice(r),
		RegistryNames:  registryNames(registries),
		RegistryFilter: registryFilter,
	}))
}

func (s *Server) handleAdminCreateWorkspaceImage(w http.ResponseWriter, r *http.Request) {
	session, ok := s.requireUserSpaceAdmin(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		s.writeErrorPage(w, r, errInvalidForm)
		return
	}
	deprecateAt, err := dateOrNil(r.Form.Get("deprecateAt"))
	if err != nil {
		s.renderCatalogAdmin(w, r, session, err.Error())
		return
	}
	api, err := s.clientFactory.ForToken(session.Token)
	if err != nil {
		s.writeErrorPage(w, r, err)
		return
	}

	name := strings.TrimSpace(r.Form.Get("displayName"))
	if name == "" {
		s.renderCatalogAdmin(w, r, session, "a catalog entry needs a name")
		return
	}
	image := &dwpkv1alpha1.WorkspaceImage{
		Spec: dwpkv1alpha1.WorkspaceImageSpec{
			DisplayName:        name,
			Description:        strings.TrimSpace(r.Form.Get("description")),
			Icon:               strings.TrimSpace(r.Form.Get("icon")),
			Tags:               splitList(r.Form.Get("tags")),
			Image:              strings.TrimSpace(r.Form.Get("image")),
			AllowRoot:          r.Form.Get("allowRoot") != "",
			DeprecateAt:        deprecateAt,
			ImagePullSecretRef: pullSecretRefOrNil(r.Form.Get("imagePullSecret")),
		},
	}
	if err := createWithSlug(r.Context(), api, image, name); err != nil {
		s.renderCatalogAdmin(w, r, session, err.Error())
		return
	}
	http.Redirect(w, r, s.path(donePath("/admin/catalog", "image")), http.StatusFound)
}

func (s *Server) handleAdminUpdateWorkspaceImage(w http.ResponseWriter, r *http.Request) {
	session, ok := s.requireUserSpaceAdmin(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		s.writeErrorPage(w, r, errInvalidForm)
		return
	}
	deprecateAt, err := dateOrNil(r.Form.Get("deprecateAt"))
	if err != nil {
		s.renderCatalogAdmin(w, r, session, err.Error())
		return
	}
	api, err := s.clientFactory.ForToken(session.Token)
	if err != nil {
		s.writeErrorPage(w, r, err)
		return
	}
	edit := WorkspaceImageEdit{
		Name:               r.PathValue("name"),
		Image:              strings.TrimSpace(r.Form.Get("image")),
		DisplayName:        strings.TrimSpace(r.Form.Get("displayName")),
		Description:        strings.TrimSpace(r.Form.Get("description")),
		Icon:               strings.TrimSpace(r.Form.Get("icon")),
		Tags:               splitList(r.Form.Get("tags")),
		Deprecated:         r.Form.Get("deprecated") != "",
		AllowRoot:          r.Form.Get("allowRoot") != "",
		DeprecateAt:        deprecateAt,
		ImagePullSecretRef: pullSecretRefOrNil(r.Form.Get("imagePullSecret")),
	}
	if err := api.PatchWorkspaceImage(r.Context(), edit); err != nil {
		s.renderCatalogAdmin(w, r, session, err.Error())
		return
	}
	http.Redirect(w, r, s.path(donePath("/admin/catalog", "image")), http.StatusFound)
}

func (s *Server) handleAdminDeleteWorkspaceImage(w http.ResponseWriter, r *http.Request) {
	session, ok := s.requireUserSpaceAdmin(w, r)
	if !ok {
		return
	}
	api, err := s.clientFactory.ForToken(session.Token)
	if err != nil {
		s.writeErrorPage(w, r, err)
		return
	}
	if err := api.DeleteWorkspaceImage(r.Context(), r.PathValue("name")); err != nil {
		s.renderCatalogAdmin(w, r, session, err.Error())
		return
	}
	http.Redirect(w, r, s.path(donePath("/admin/catalog", "image")), http.StatusFound)
}

// registryNames is every configured ImageRegistry's name, sorted, for the
// catalog filter's <select> - the registries themselves are managed on their
// own page, so this is the one thing about them the catalog still needs.
func registryNames(registries []dwpkv1alpha1.ImageRegistry) []string {
	names := make([]string, len(registries))
	for i, reg := range registries {
		names[i] = reg.Name
	}
	slices.Sort(names)
	return names
}

// splitList turns a comma-separated field into a list, dropping the blanks a
// trailing comma leaves behind.
func splitList(value string) []string {
	items := []string{}
	for item := range strings.SplitSeq(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			items = append(items, item)
		}
	}
	return items
}

// createWithSlug names the object from the entry's name.
//
// Kubernetes requires a metadata.name and assigns the uid only afterwards, so
// "the name is the uid" cannot be done literally. A slug is the honest version:
// the person names the entry once, and the object name is derived from it so
// `kubectl get workspaceimage` stays readable and every Workspace's imageRef
// still points at something a human recognises.
//
// Collisions retry rather than fail. Two entries called "Python" are a
// reasonable thing to want, and asking somebody to invent a unique slug is
// exactly the job this removes.
func createWithSlug(
	ctx context.Context, api RequestAPI, image *dwpkv1alpha1.WorkspaceImage, name string,
) error {
	base := slugify(name)
	if base == "" {
		return fmt.Errorf("%q contains no characters usable in a name", name)
	}
	for attempt := 1; attempt <= slugAttempts; attempt++ {
		image.Name = base
		if attempt > 1 {
			image.Name = fmt.Sprintf("%s-%d", base, attempt)
		}
		err := api.CreateWorkspaceImage(ctx, image)
		if !apierrors.IsAlreadyExists(err) {
			return err
		}
	}
	return fmt.Errorf("could not find a free name for %q after %d attempts", name, slugAttempts)
}

// slugAttempts bounds the collision retry. Ten entries sharing one name is
// already a naming problem the platform should not paper over further.
const slugAttempts = 10

// slugify turns a display name into a DNS label: lowercase, alphanumerics kept,
// everything else a separator, no leading or trailing dash, 63 characters.
func slugify(name string) string {
	var out []rune
	previousDash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			out = append(out, r)
			previousDash = false
		case len(out) > 0 && !previousDash:
			out = append(out, '-')
			previousDash = true
		}
	}
	// Room for the "-10" a collision may append, so a retry cannot overflow the
	// 63-character limit and produce an invalid name.
	const maxSlug = 60
	if len(out) > maxSlug {
		out = out[:maxSlug]
	}
	return strings.Trim(string(out), "-")
}

// pullSecretName reads a pull-secret reference back as plain text for the
// form, or "" when none is set.
func pullSecretName(ref *corev1.LocalObjectReference) string {
	if ref == nil {
		return ""
	}
	return ref.Name
}

// pullSecretRefOrNil is the read direction: a blank field clears the
// reference rather than naming a Secret called "".
func pullSecretRefOrNil(name string) *corev1.LocalObjectReference {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	return &corev1.LocalObjectReference{Name: name}
}

// dateValue renders a scheduled date for an <input type="date">, which speaks
// only yyyy-mm-dd.
func dateValue(at *metav1.Time) string {
	if at == nil {
		return ""
	}
	return at.Format(time.DateOnly)
}

// dateOrNil reads that input back. Blank is not an error: it is how the date is
// cleared.
func dateOrNil(raw string) (*metav1.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.DateOnly, raw)
	if err != nil {
		return nil, fmt.Errorf("deprecation date: %q is not a date (expected yyyy-mm-dd)", raw)
	}
	return &metav1.Time{Time: parsed}, nil
}
