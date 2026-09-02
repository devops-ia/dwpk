package ui

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	dwpkv1alpha1 "github.com/devops-ia/dwpk/api/v1alpha1"
	"github.com/devops-ia/dwpk/internal/auth"
	workspacepkg "github.com/devops-ia/dwpk/internal/workspace"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// boolTrue and boolFalse are the HTML attribute spellings, kept as constants
// so the same two literals are not scattered across templates and handlers.
const (
	boolTrue  = "true"
	boolFalse = "false"
)

// catalogPath is named once because the filter form's hx-get and the route
// that serves it must agree. They silently disagreed when the catalog moved off
// "/": filtering swapped the whole dashboard into the card grid.
const (
	catalogPath         = "/catalog"
	catalogAdminPath    = "/admin/catalog"
	registriesAdminPath = "/admin/registries"
)

const (
	sessionCookieName       = "dwpk_ui_session"
	loginStateCookieName    = "dwpk_ui_login_state"
	loginNextCookieName     = "dwpk_ui_login_next"
	loginRememberCookieName = "dwpk_ui_login_remember"
	loginRedirectParam      = "next"
	loginRememberParam      = "remember"

	// rememberedSessionTTL is the idle window a ticked "remember me" buys. The
	// ServiceAccount token behind the session is re-minted hourly regardless,
	// so this lengthens the window, not the authority.
	rememberedSessionTTL = 7 * 24 * time.Hour
)

type ProviderOption struct {
	Name  string
	Label string
}

type ImageCard struct {
	Name        string
	DisplayName string
	Description string
	IconPath    string
	Tags        []string
	Deprecated  bool
	// DeprecationNotice warns while a scheduled date is still ahead, and is
	// empty when there is nothing to say. The card warns; it does not block.
	DeprecationNotice string
	// Origin is the cloud or registry the image reference belongs to, derived
	// from its host - "AWS", "Docker Hub", and so on.
	Origin string

	// The rest is for the detail dialog only. Rendered as strings because the
	// dialog shows them and nothing computes with them; a card that carried the
	// whole spec would be a second copy of the API type to keep in step.
	Image           string
	ImagePullPolicy string
	Shell           string
	HomePath        string
	RunAs           string
	Command         string
	Maintainer      string
	DeprecateAt     string
	Placement       string
}

type CatalogData struct {
	Session RequestSession
	// Page is the cards for the requested page. Nine at a time, because they
	// render three to a line and a page that is not a multiple of three leaves
	// a ragged last row.
	Page Page[ImageCard]
	// Request backs the page links, which carry the current filters so changing
	// page does not drop the search.
	Request        *http.Request
	Text           string
	Tag            string
	Tags           []string
	ShowDeprecated bool
}

type CreateData struct {
	Session RequestSession
	Image   dwpkv1alpha1.WorkspaceImage
	SSHKey  string
	// Resources is what the resource form renders.
	Resources ResourceValues
	// HasKeyOnFile is true when the profile carries default keys, in which case
	// this form need not ask for one: the admission webhook supplies them.
	HasKeyOnFile bool
	// WorkspaceCount and WorkspaceLimit are this person's quota. The webhook is
	// what enforces it; these exist so a refusal is not the first anyone hears
	// of it.
	WorkspaceCount int
	WorkspaceLimit int32
}

// AtWorkspaceLimit reports whether creating another would be refused.
//
// The comparison is >=, matching the webhook's `count < limit` exactly. It is
// deliberately not >: with a limit of 2 and 2 already, one more would be a
// third. The two must agree, or the form promises something admission then
// takes away.
func (d CreateData) AtWorkspaceLimit() bool {
	return d.WorkspaceLimit > 0 && d.WorkspaceCount >= int(d.WorkspaceLimit)
}

type WorkspacePageData struct {
	Session     RequestSession
	Workspace   *dwpkv1alpha1.Workspace
	GatewayHost string
	Endpoint    string
	SSHCommand  string
	VSCodeLink  string
	Settled     bool
	// CPU, Memory and GPU are the configured limits, rendered for the facts list.
	CPU    string
	Memory string
	GPU    string
	// EditResources prefills the Edit dialog's resource form from the
	// workspace's current spec - same values as CPU/Memory/GPU above, plus
	// Storage and GPUResource, in the one shape resourceFields renders.
	EditResources ResourceValues
	// RestartRequired is true when the workspace has been edited since its pod
	// started, so the running container does not yet reflect its own spec.
	//
	// Deliberately unwired for now: the only live signal for "has the pod
	// actually picked up the new spec" is the owned StatefulSet's
	// currentRevision/updateRevision, which the workspace owner's RBAC
	// cannot read today. The edit flow instead uses the one-time Notice
	// flash below.
	RestartRequired bool
	// Notice is a one-time success message read back from a redirect's
	// ?done= query param (see doneNotice) - e.g. "Workspace resized."
	Notice string
}

// WorkspaceLogData carries either log lines or the reason there are none.
// Message is set when there is nothing to show, so the view never has to guess
// whether an empty Lines means "quiet" or "broken".
type WorkspaceLogData struct {
	Lines   string
	Message string
	Detail  string
}

type QuotaRow struct {
	Name           string
	Owner          string
	Namespace      string
	WorkspaceCount int
	WorkspaceLimit int32
	CPUUsed        string
	CPULimit       string
	MemoryUsed     string
	MemoryLimit    string
	StorageUsed    string
	StorageLimit   string
	GPUUsed        string
	GPULimit       string
}

// QuotaData backs the quota screen, which is now editable and therefore needs
// the session's CSRF token.
type QuotaData struct {
	Session RequestSession
	Rows    []QuotaRow
	Notice  string
}

// doneMessages are the success sentences a redirect may carry back. A key
// rather than the text itself, so a crafted link cannot put arbitrary words on
// someone's screen - and so an unknown key renders nothing rather than
// something odd.
//
// Only actions whose result is invisible on the page you land on are listed.
// A deleted row is its own confirmation; a quota that changed by 2Gi is not.
var doneMessages = map[string]string{
	"quota":           "Quota updated.",
	"resized":         "Workspace resized. It restarts on the new size.",
	"image":           "Catalog entry saved.",
	"request":         "Your request was sent to the project's managers.",
	"registry":        "Registry saved.",
	"sync":            "Sync requested.",
	"password":        "Your password has been changed.",
	"key-added":       "SSH key added.",
	"key-removed":     "SSH key removed.",
	"git-key-added":   "Git SSH key added.",
	"git-key-removed": "Git SSH key removed.",
	"token-revoked":   "API token revoked.",
}

const doneParam = "done"

func doneNotice(r *http.Request) string {
	return doneMessages[r.URL.Query().Get(doneParam)]
}

// donePath appends the success key to a redirect target.
func donePath(path, key string) string {
	return path + "?" + doneParam + "=" + url.QueryEscape(key)
}

type ErrorData struct {
	Session *RequestSession
	Status  int
	Message string
}

func ProviderNames() []auth.Name {
	return []auth.Name{
		auth.ProviderEntraID,
		auth.ProviderGoogle,
		auth.ProviderGitLab,
		auth.ProviderKeycloak,
		auth.ProviderGitHub,
	}
}

// providerLabels is the display name per provider. It is a lookup rather than
// a list because the set offered to a user comes from what an operator
// configured, not from what the code knows how to speak.
var providerLabels = map[auth.Name]string{
	auth.ProviderEntraID:  "Entra ID",
	auth.ProviderGoogle:   "Google",
	auth.ProviderGitLab:   "GitLab",
	auth.ProviderKeycloak: "Keycloak",
	auth.ProviderGitHub:   "GitHub",
}

// providerOptions renders the configured providers for the login drop-down,
// preserving the order the registry gave them.
func providerOptions(configured []auth.Name) []ProviderOption {
	options := make([]ProviderOption, 0, len(configured))
	for _, name := range configured {
		label, ok := providerLabels[name]
		if !ok {
			// A provider the registry accepted but this table does not name.
			// Showing the raw name beats hiding a working login method.
			label = string(name)
		}
		options = append(options, ProviderOption{Name: string(name), Label: label})
	}
	return options
}

func workspaceSSHCommand(workspace *dwpkv1alpha1.Workspace, gatewayHost string) string {
	return "ssh " + workspaceEndpoint(workspace, gatewayHost)
}

func workspaceEndpoint(workspace *dwpkv1alpha1.Workspace, gatewayHost string) string {
	endpoint := strings.TrimSpace(workspace.Status.Endpoint)
	if endpoint == "" {
		endpoint = workspace.Name + "@" + gatewayHost
	}
	return endpoint
}

// workspaceVSCodeLink opens VS Code straight at the workspace's own home
// directory rather than the container's filesystem root - the default folder
// otherwise landed on "/", the one directory nothing useful lives in.
// homePath is empty when the catalog entry could not be read (a workspace
// whose image has since been deleted); "/" is still a working link then, just
// not the improved one.
func workspaceVSCodeLink(workspace *dwpkv1alpha1.Workspace, gatewayHost, homePath string) string {
	path := homePath
	if path == "" {
		path = "/"
	}
	return "vscode://vscode-remote/ssh-remote+" + workspaceEndpoint(workspace, gatewayHost) + path
}

func workspaceStateSettled(state string) bool {
	switch state {
	case dwpkv1alpha1.WorkspaceStateRunning, dwpkv1alpha1.WorkspaceStateSuspended, dwpkv1alpha1.WorkspaceStateFailed:
		return true
	default:
		return false
	}
}

// workspacePollTrigger says when the status card should re-fetch itself.
//
// It must never include "load". The card replaces itself with outerHTML, and
// htmx fires load triggers on freshly swapped content - so a load trigger here
// means every response immediately causes the next request, with no delay. That
// is a hot loop against the API server, and on screen it reads as a page that
// will not stop reloading.
//
// It is also redundant: the card is rendered server-side with current data, so
// there is nothing to fetch on load.
//
// A settled workspace returns "none" rather than "". An absent hx-trigger does
// not mean "never fire": htmx falls back to a default trigger chosen from the
// element, which for a <div> is click. Since this card swaps itself with
// outerHTML and holds the Delete button and its dialog, that default made
// clicking Delete open the dialog and immediately replace the card underneath
// it. "none" says what was meant, and leaves nothing to guess.
func workspacePollTrigger(settled bool) string {
	if settled {
		return "none"
	}
	return "every 3s"
}

func workspaceImageVisible(img dwpkv1alpha1.WorkspaceImage, text, tag string, showDeprecated bool) bool {
	if img.Spec.Deprecated && !showDeprecated {
		return false
	}
	tag = strings.TrimSpace(strings.ToLower(tag))
	if tag != "" {
		found := false
		for _, candidate := range img.Spec.Tags {
			if strings.EqualFold(candidate, tag) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	query := strings.TrimSpace(strings.ToLower(text))
	if query == "" {
		return true
	}
	haystacks := make([]string, 0, 4+len(img.Spec.Tags))
	haystacks = append(haystacks, img.Name, img.Spec.DisplayName, img.Spec.Description, img.Spec.Maintainer)
	haystacks = append(haystacks, img.Spec.Tags...)
	for _, haystack := range haystacks {
		if strings.Contains(strings.ToLower(haystack), query) {
			return true
		}
	}
	return false
}

func catalogTags(images []dwpkv1alpha1.WorkspaceImage) []string {
	unique := map[string]struct{}{}
	for _, image := range images {
		for _, tag := range image.Spec.Tags {
			tag = strings.TrimSpace(tag)
			if tag != "" {
				unique[tag] = struct{}{}
			}
		}
	}
	tags := make([]string, 0, len(unique))
	for tag := range unique {
		tags = append(tags, tag)
	}
	slices.Sort(tags)
	return tags
}

// iconPath is empty unless the image actually has an icon: the route answers
// 404 for one that does not, and a card that asks anyway draws a broken image.
func iconPath(basePath, name, icon string) string {
	if name == "" || strings.TrimSpace(icon) == "" {
		return ""
	}
	return basePath + "/workspace-images/" + url.PathEscape(name) + "/icon"
}

// ariaPressed renders the toggle's state for assistive technology. The button
// is icon-only, so this and its aria-label are the only things announcing what
// it does.
func ariaPressed(pressed bool) string {
	if pressed {
		return boolTrue
	}
	return boolFalse
}

// roleOptions lists the assignable roles in increasing order of authority.
// Granting administrator is refused at admission unless the caller already is
// one, so offering it here is safe: the API server has the final say.
func roleOptions() []string {
	return []string{
		dwpkv1alpha1.UserSpaceRoleUser,
		dwpkv1alpha1.UserSpaceRoleAdmin,
	}
}

// scopeLabel spells a token scope for the picker, saying what it means rather
// than repeating the value.
func scopeLabel(scope workspacepkg.TokenScope) string {
	if scope == workspacepkg.TokenScopeRead {
		return "Read only"
	}
	return "Full - everything your role allows"
}

// ThemeOption is one choice in the appearance picker.
type ThemeOption struct {
	Value string
	Label string
}

// themeOptions are the platform-wide appearance defaults. "system" is first
// because following the viewer's own operating system is the least surprising
// default a platform can pick for them.
func themeOptions() []ThemeOption {
	return []ThemeOption{
		{Value: "system", Label: "Follow the viewer's system setting"},
		{Value: "light", Label: "Light"},
		{Value: "dark", Label: "Dark"},
	}
}

// tabIndexOf keeps a tab strip to one tab stop: the selected tab is reachable
// by Tab, the rest by arrow keys, which is what a tablist is meant to do.
func tabIndexOf(selected bool) string {
	if selected {
		return "0"
	}
	return "-1"
}

func isAdmin(session RequestSession) bool {
	return session.Identity.Role == dwpkv1alpha1.UserSpaceRoleAdmin
}

// pageClass centres the signed-out pages - login and anonymous errors - in the
// viewport, and lays the signed-in pages out as a normal document.
func pageClass(authenticated bool) string {
	if authenticated {
		return "page"
	}
	return "page auth-shell"
}

func csrfHeadersJSON(token string) string {
	return fmt.Sprintf(`{"%s":"%s"}`, csrfHeaderName, token)
}

// orDash renders an unset string as an em dash. An empty table cell reads as a
// rendering fault; a dash reads as "nothing here yet".
func orDash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}

func quantityString(quantity *resource.Quantity) string {
	if quantity == nil {
		return "-"
	}
	return quantity.String()
}

// conditionsBlock renders one condition per line, aligned, the way kubectl
// describe does. Message goes last because it is the only variable-length part.
func conditionsBlock(conditions []metav1.Condition) string {
	lines := make([]string, 0, len(conditions))
	for _, condition := range conditions {
		line := fmt.Sprintf("%s=%s  %s", condition.Type, condition.Status, condition.Reason)
		if message := strings.TrimSpace(condition.Message); message != "" {
			line += "\n    " + message
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

// failureReason returns what to tell someone whose workspace did not start, or
// "" when there is nothing wrong. The controller's own reason and message are
// passed through rather than paraphrased: a paraphrase is one more thing that
// can be wrong, and the reason is usually the fix.
func failureReason(workspace *dwpkv1alpha1.Workspace) string {
	if workspace.Status.State != dwpkv1alpha1.WorkspaceStateFailed {
		return ""
	}
	for _, condition := range workspace.Status.Conditions {
		if condition.Status == metav1.ConditionFalse && strings.TrimSpace(condition.Message) != "" {
			return condition.Reason + ": " + condition.Message
		}
	}
	return "The controller reported no reason."
}

// statusTone maps a CR state onto the four semantic colours the chips use.
// Kept in one place because Workspace, UserSpace and Project spell their states
// differently but a reader should not have to learn three vocabularies.
//
// The three CRs share several state names - "Failed" and "Pending" are the same
// string in each - so the cases below name only one constant per value.
func statusTone(state string) string {
	switch state {
	case dwpkv1alpha1.WorkspaceStateRunning,
		dwpkv1alpha1.UserSpaceStateReady:
		return "ok"
	case dwpkv1alpha1.WorkspaceStateFailed, "Degraded":
		return "bad"
	case dwpkv1alpha1.WorkspaceStatePending, dwpkv1alpha1.WorkspaceStateStarting:
		return "busy"
	default:
		// Suspended, Disabled, and anything a newer controller reports that
		// this build has not heard of.
		return "idle"
	}
}

// Page is one screenful of rows plus what the controls need to render. It is
// generic because "a page of things" is the same shape whatever the things are,
// and the alternative is this logic copied per screen.
type Page[T any] struct {
	Items []T
	// Number is 1-based, and Size is 0 when the viewer asked for everything.
	Number int
	Size   int
	Total  int
	Pages  int
}

// pageSizes are the choices a table offers, and the FIRST is the default: an
// unrecognised ?per= falls back to it. 0 means "all" - spelled out in the query
// string as "all" rather than as 0, because a URL saying per=0 reads like a bug.
var pageSizes = []int{10, 5, 0}

// catalogPageSizes are the catalog's, because its rows are cards three to a
// line and a page that is not a multiple of three leaves a ragged last row.
var catalogPageSizes = []int{9, 15, 18, 0}

const (
	pageParam     = "page"
	pageSizeParam = "per"
	allPages      = "all"
)

// paginate slices rows for the requested page. An out-of-range page clamps to
// the last one rather than rendering empty: a bookmarked ?page=9 after rows
// were deleted should show the end of the list, not nothing.
func paginate[T any](items []T, r *http.Request) Page[T] {
	return paginateWith(items, r, pageSizes)
}

// paginateWith is the same, for a screen whose page sizes are its own.
//
// The allowlist is a parameter rather than a second pagination path: a screen
// that wants nine per page should not need its own clamping, its own "all", and
// its own off-by-one at the last page.
func paginateWith[T any](items []T, r *http.Request, sizes []int) Page[T] {
	size := pageSizeFrom(r, sizes)
	page := Page[T]{Items: items, Number: 1, Size: size, Total: len(items), Pages: 1}
	if size <= 0 || len(items) == 0 {
		return page
	}

	page.Pages = (len(items) + size - 1) / size
	page.Number = 1
	if raw := r.URL.Query().Get(pageParam); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 1 {
			page.Number = parsed
		}
	}
	if page.Number > page.Pages {
		page.Number = page.Pages
	}

	start := (page.Number - 1) * size
	end := min(start+size, len(items))
	page.Items = items[start:end]
	return page
}

// pageSizeFrom reads ?per=, accepting only what the screen offers.
//
// An allowlist rather than any integer: the value ends up as a slice length, and
// a crafted ?per=100000000 is a way to make the server render a page nobody
// asked for. Anything unrecognised falls back to the screen's first choice.
func pageSizeFrom(r *http.Request, sizes []int) int {
	raw := r.URL.Query().Get(pageSizeParam)
	if raw == allPages {
		return 0
	}
	if parsed, err := strconv.Atoi(raw); err == nil && slices.Contains(sizes, parsed) {
		return parsed
	}
	return sizes[0]
}

// pageNumbers is every page, because a platform's project list is tens of rows
// and an elided "1 … 7 8 9 … 40" control is machinery for a problem this does
// not have.
func pageNumbers(pages int) []int {
	numbers := make([]int, 0, pages)
	for i := 1; i <= pages; i++ {
		numbers = append(numbers, i)
	}
	return numbers
}

// linkPathString is linkPath for somewhere a string is needed rather than a
// templ.SafeURL - the pagination links build their query before wrapping.
func linkPathString(ctx context.Context, path string) string {
	return basePathOf(ctx) + path
}

// pageSizeLabel spells a size for the control. 0 is "All".
func pageSizeLabel(size int) string {
	if size <= 0 {
		return "All"
	}
	return strconv.Itoa(size)
}

// pageQuery builds the link for a page-size or page-number choice, keeping
// whatever other filters are already on the URL.
func pageQuery(r *http.Request, path string, size, number int) string {
	query := url.Values{}
	for key, values := range r.URL.Query() {
		if key != pageParam && key != pageSizeParam {
			query[key] = values
		}
	}
	if size <= 0 {
		query.Set(pageSizeParam, allPages)
	} else {
		query.Set(pageSizeParam, strconv.Itoa(size))
		if number > 1 {
			query.Set(pageParam, strconv.Itoa(number))
		}
	}
	if len(query) == 0 {
		return path
	}
	return path + "?" + query.Encode()
}
