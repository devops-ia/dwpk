package ui

import (
	"net/http"
	"slices"
	"strconv"
	"strings"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	dwpkv1alpha1 "github.com/devops-ia/dwpk/api/v1alpha1"
)

// conditionTypeReady is the condition type name every reconciler in this
// project writes for its "everything worked" state (§5.2).
const conditionTypeReady = "Ready"

// neverLabel is what a timestamp-shaped field shows before it has a value -
// an expiry that never happens, or a sync that has not run yet.
const neverLabel = "never"

// noneLabel is the literal "none" - two unrelated meanings share it (an
// empty provider list, and htmx's own hx-trigger="none" value), which is
// exactly why it is one constant instead of two: the spelling has to match
// htmx's own regardless of what a given call site means by it.
const noneLabel = "none"

// RegistryRow is one ImageRegistry as the admin screen edits it.
type RegistryRow struct {
	Name       string
	Provider   string
	Region     string
	RegistryID string
	RoleARN    string
	// Status is "Ready" or "Degraded", read from the object's own conditions -
	// the exact strings statusChip/statusTone already know how to color.
	Status  string
	Message string
	Images  int32
	// LastSync is a rendered timestamp, or "never" before the first sync.
	LastSync        string
	IntervalSeconds int32
	// Include, Exclude and TagPatterns are newline-joined for a <textarea>: a
	// comma-based splitList is wrong here, a regex may legitimately contain one.
	Include         string
	Exclude         string
	TagMode         string
	TagPatterns     string
	TagLimit        int32
	Prune           bool
	ImagePullSecret string
}

// AdminRegistriesData backs GET /admin/registries.
type AdminRegistriesData struct {
	Session RequestSession
	Rows    []RegistryRow
	Error   string
	Notice  string
}

func (s *Server) handleAdminRegistries(w http.ResponseWriter, r *http.Request) {
	session, ok := s.requireUserSpaceAdmin(w, r)
	if !ok {
		return
	}
	s.renderRegistriesAdmin(w, r, session, "")
}

func (s *Server) renderRegistriesAdmin(w http.ResponseWriter, r *http.Request, session RequestSession, message string) {
	api, err := s.clientFactory.ForToken(session.Token)
	if err != nil {
		s.writeErrorPage(w, r, err)
		return
	}
	registries, err := api.ListImageRegistries(r.Context())
	if err != nil {
		s.writeErrorPage(w, r, err)
		return
	}

	status := http.StatusOK
	if message != "" {
		status = http.StatusBadRequest
	}
	s.renderAuthedPage(w, r, status, session, "Registries", AdminRegistriesPage(AdminRegistriesData{
		Session: session,
		Rows:    imageRegistryRows(registries),
		Error:   message,
		Notice:  doneNotice(r),
	}))
}

func imageRegistryRows(registries []dwpkv1alpha1.ImageRegistry) []RegistryRow {
	rows := make([]RegistryRow, 0, len(registries))
	for i := range registries {
		reg := &registries[i]
		status, message := "Pending", ""
		if ready := apimeta.FindStatusCondition(reg.Status.Conditions, conditionTypeReady); ready != nil {
			message = ready.Message
			if ready.Status == metav1.ConditionTrue {
				status = conditionTypeReady
			} else {
				status = "Degraded"
			}
		}
		lastSync := neverLabel
		if reg.Status.LastSyncTime != nil {
			lastSync = reg.Status.LastSyncTime.Format("2006-01-02 15:04:05")
		}
		var region, registryID, roleARN string
		if reg.Spec.AWS != nil {
			region, registryID, roleARN = reg.Spec.AWS.Region, reg.Spec.AWS.RegistryID, reg.Spec.AWS.RoleARN
		}
		rows = append(rows, RegistryRow{
			Name:            reg.Name,
			Provider:        reg.Spec.Provider,
			Region:          region,
			RegistryID:      registryID,
			RoleARN:         roleARN,
			Status:          status,
			Message:         message,
			Images:          reg.Status.Images,
			LastSync:        lastSync,
			IntervalSeconds: reg.Spec.Sync.IntervalSeconds,
			Include:         joinLines(reg.Spec.Sync.Include),
			Exclude:         joinLines(reg.Spec.Sync.Exclude),
			TagMode:         reg.Spec.Sync.Tags.Mode,
			TagPatterns:     joinLines(reg.Spec.Sync.Tags.Patterns),
			TagLimit:        reg.Spec.Sync.Tags.Limit,
			Prune:           reg.Spec.Sync.Prune,
			ImagePullSecret: pullSecretName(reg.Spec.ImagePullSecretRef),
		})
	}
	slices.SortFunc(rows, func(a, b RegistryRow) int { return strings.Compare(a.Name, b.Name) })
	return rows
}

func (s *Server) handleAdminCreateImageRegistry(w http.ResponseWriter, r *http.Request) {
	session, ok := s.requireUserSpaceAdmin(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		s.writeErrorPage(w, r, errInvalidForm)
		return
	}
	api, err := s.clientFactory.ForToken(session.Token)
	if err != nil {
		s.writeErrorPage(w, r, err)
		return
	}
	name := strings.TrimSpace(r.Form.Get("name"))
	if name == "" {
		s.renderRegistriesAdmin(w, r, session, "a registry needs a name")
		return
	}
	reg := &dwpkv1alpha1.ImageRegistry{
		ObjectMeta: metav1.ObjectMeta{Name: slugify(name)},
		Spec: dwpkv1alpha1.ImageRegistrySpec{
			Provider: dwpkv1alpha1.ImageRegistryProviderAWSECR,
			AWS: &dwpkv1alpha1.AWSRegistry{
				Region:     strings.TrimSpace(r.Form.Get("region")),
				RegistryID: strings.TrimSpace(r.Form.Get("registryId")),
				RoleARN:    strings.TrimSpace(r.Form.Get("roleArn")),
			},
			Sync:               syncFromForm(r),
			ImagePullSecretRef: pullSecretRefOrNil(r.Form.Get("imagePullSecret")),
		},
	}
	if err := api.CreateImageRegistry(r.Context(), reg); err != nil {
		s.renderRegistriesAdmin(w, r, session, err.Error())
		return
	}
	http.Redirect(w, r, s.path(donePath(registriesAdminPath, "registry")), http.StatusFound)
}

func (s *Server) handleAdminUpdateImageRegistry(w http.ResponseWriter, r *http.Request) {
	session, ok := s.requireUserSpaceAdmin(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		s.writeErrorPage(w, r, errInvalidForm)
		return
	}
	api, err := s.clientFactory.ForToken(session.Token)
	if err != nil {
		s.writeErrorPage(w, r, err)
		return
	}
	sync := syncFromForm(r)
	edit := ImageRegistryEdit{
		Name:               r.PathValue("name"),
		Region:             strings.TrimSpace(r.Form.Get("region")),
		RegistryID:         strings.TrimSpace(r.Form.Get("registryId")),
		RoleARN:            strings.TrimSpace(r.Form.Get("roleArn")),
		IntervalSeconds:    sync.IntervalSeconds,
		Include:            sync.Include,
		Exclude:            sync.Exclude,
		TagMode:            sync.Tags.Mode,
		TagPatterns:        sync.Tags.Patterns,
		TagLimit:           sync.Tags.Limit,
		Prune:              sync.Prune,
		ImagePullSecretRef: pullSecretRefOrNil(r.Form.Get("imagePullSecret")),
	}
	if err := api.PatchImageRegistry(r.Context(), edit); err != nil {
		s.renderRegistriesAdmin(w, r, session, err.Error())
		return
	}
	http.Redirect(w, r, s.path(donePath(registriesAdminPath, "registry")), http.StatusFound)
}

func (s *Server) handleAdminDeleteImageRegistry(w http.ResponseWriter, r *http.Request) {
	session, ok := s.requireUserSpaceAdmin(w, r)
	if !ok {
		return
	}
	api, err := s.clientFactory.ForToken(session.Token)
	if err != nil {
		s.writeErrorPage(w, r, err)
		return
	}
	if err := api.DeleteImageRegistry(r.Context(), r.PathValue("name")); err != nil {
		s.renderRegistriesAdmin(w, r, session, err.Error())
		return
	}
	http.Redirect(w, r, s.path(donePath(registriesAdminPath, "registry")), http.StatusFound)
}

func (s *Server) handleAdminForceSyncImageRegistry(w http.ResponseWriter, r *http.Request) {
	session, ok := s.requireUserSpaceAdmin(w, r)
	if !ok {
		return
	}
	api, err := s.clientFactory.ForToken(session.Token)
	if err != nil {
		s.writeErrorPage(w, r, err)
		return
	}
	if err := api.ForceSyncImageRegistry(r.Context(), r.PathValue("name")); err != nil {
		s.renderRegistriesAdmin(w, r, session, err.Error())
		return
	}
	http.Redirect(w, r, s.path(donePath(registriesAdminPath, "sync")), http.StatusFound)
}

// syncFromForm reads the fields registryEntry and addRegistryDialog share.
func syncFromForm(r *http.Request) dwpkv1alpha1.RegistrySync {
	interval, err := strconv.Atoi(strings.TrimSpace(r.Form.Get("intervalSeconds")))
	if err != nil || interval <= 0 {
		interval = dwpkv1alpha1.DefaultSyncIntervalSeconds
	}
	limit, err := strconv.Atoi(strings.TrimSpace(r.Form.Get("tagLimit")))
	if err != nil || limit <= 0 {
		limit = 1
	}
	tagMode := strings.TrimSpace(r.Form.Get("tagMode"))
	if tagMode == "" {
		tagMode = dwpkv1alpha1.TagSelectorModeLatest
	}
	return dwpkv1alpha1.RegistrySync{
		IntervalSeconds: int32(interval),
		Include:         splitLines(r.Form.Get("include")),
		Exclude:         splitLines(r.Form.Get("exclude")),
		Tags: dwpkv1alpha1.TagSelector{
			Mode:     tagMode,
			Patterns: splitLines(r.Form.Get("tagPatterns")),
			Limit:    int32(limit),
		},
		Prune: r.Form.Get("prune") != "",
	}
}

// splitLines and joinLines are splitList's newline-based sibling: a regex
// pattern may legitimately contain a comma, so include/exclude/tagPatterns
// render as one pattern per line rather than a comma-separated field.
func splitLines(value string) []string {
	items := []string{}
	for line := range strings.SplitSeq(value, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			items = append(items, line)
		}
	}
	return items
}

func joinLines(items []string) string {
	return strings.Join(items, "\n")
}
