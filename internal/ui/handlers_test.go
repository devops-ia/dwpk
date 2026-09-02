package ui

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	dwpkv1alpha1 "github.com/devops-ia/dwpk/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/remotecommand"
)

func TestCatalogShowsOnlyAuthorizedImages(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	server.loginFlow = fakeSessionAuthenticator{identity: SessionIdentity{UserSpaceNamespace: "dwpk-alice"}, token: "minted"}
	csrf, _ := server.csrfStore.Ensure("session-1")
	server.clientFactory = fakeAPIClientFactory{api: fakeAPI{
		images: []dwpkv1alpha1.WorkspaceImage{
			{ObjectMeta: metav1.ObjectMeta{Name: "python"}, Spec: dwpkv1alpha1.WorkspaceImageSpec{DisplayName: "Python", Tags: []string{"backend"}}},
			{ObjectMeta: metav1.ObjectMeta{Name: "gpu"}, Spec: dwpkv1alpha1.WorkspaceImageSpec{DisplayName: "GPU", Tags: []string{"gpu"}}},
		},
		allowedImages: map[string]bool{"python": true, "gpu": false},
	}}
	recorder := httptest.NewRecorder()
	request := authedRequest(http.MethodGet, "/catalog", csrf)
	server.Handler().ServeHTTP(recorder, request)
	body := recorder.Body.String()
	if !strings.Contains(body, "Python") || strings.Contains(body, "GPU") {
		t.Fatalf("unexpected catalog body: %s", body)
	}
}

func TestCreateWorkspaceRendersAPIServerMessage(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	server.loginFlow = fakeSessionAuthenticator{identity: SessionIdentity{UserSpaceNamespace: "dwpk-alice"}, token: "minted"}
	csrf, _ := server.csrfStore.Ensure("session-1")
	server.clientFactory = fakeAPIClientFactory{api: fakeAPI{
		createErr: apierrors.NewForbidden(schema.GroupResource{Group: "dwpk.devops-ia.io", Resource: "workspaces"}, "dev", errors.New("gpu image requires use grant")),
	}}
	request := authedFormRequest("/new", csrf, "name=dev&image=gpu&size=small&storage=20Gi&ssh_public_key=ssh-ed25519+AAAA")
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "gpu image requires use grant") {
		t.Fatalf("body missing API message: %s", recorder.Body.String())
	}
}

func TestWorkspacePageContainsConnectionHelpers(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	server.loginFlow = fakeSessionAuthenticator{identity: SessionIdentity{UserSpaceNamespace: "dwpk-alice"}, token: "minted"}
	csrf, _ := server.csrfStore.Ensure("session-1")
	server.clientFactory = fakeAPIClientFactory{api: fakeAPI{workspace: &dwpkv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: "dev", Namespace: "dwpk-alice"},
		Status:     dwpkv1alpha1.WorkspaceStatus{State: dwpkv1alpha1.WorkspaceStateRunning},
	}}}
	recorder := httptest.NewRecorder()
	request := authedRequest(http.MethodGet, "/w/dev", csrf)
	server.Handler().ServeHTTP(recorder, request)
	body := recorder.Body.String()
	if !strings.Contains(body, "ssh dev@dwpk.example.com") {
		t.Fatalf("missing SSH command: %s", body)
	}
	if !strings.Contains(body, "vscode://vscode-remote/ssh-remote+dev@dwpk.example.com/") {
		t.Fatalf("missing VS Code link: %s", body)
	}
}

// The VS Code link opens straight at the workspace's own home directory, not
// the container filesystem root - "/" is the one place nothing useful lives.
func TestWorkspacePageVSCodeLinkOpensAtHome(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	server.loginFlow = fakeSessionAuthenticator{identity: SessionIdentity{UserSpaceNamespace: "dwpk-alice"}, token: "minted"}
	csrf, _ := server.csrfStore.Ensure("session-1")
	server.clientFactory = fakeAPIClientFactory{api: fakeAPI{
		workspace: &dwpkv1alpha1.Workspace{
			ObjectMeta: metav1.ObjectMeta{Name: "dev", Namespace: "dwpk-alice"},
			Spec:       dwpkv1alpha1.WorkspaceSpec{ImageRef: dwpkv1alpha1.WorkspaceImageReference{Name: "python"}},
			Status:     dwpkv1alpha1.WorkspaceStatus{State: dwpkv1alpha1.WorkspaceStateRunning},
		},
		images: []dwpkv1alpha1.WorkspaceImage{{
			ObjectMeta: metav1.ObjectMeta{Name: "python"},
			Spec:       dwpkv1alpha1.WorkspaceImageSpec{HomePath: "/home/dev"},
		}},
	}}
	recorder := httptest.NewRecorder()
	request := authedRequest(http.MethodGet, "/w/dev", csrf)
	server.Handler().ServeHTTP(recorder, request)
	body := recorder.Body.String()
	if !strings.Contains(body, "vscode://vscode-remote/ssh-remote+dev@dwpk.example.com/home/dev") {
		t.Fatalf("VS Code link does not open at the image's home path: %s", body)
	}
}

func authedRequest(method, path, csrf string) *http.Request {
	request := httptest.NewRequest(method, path, nil)
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "session-1"})
	if csrf != "" {
		request.Header.Set(csrfHeaderName, csrf)
	}
	return request
}

func authedFormRequest(path, csrf, body string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set(csrfHeaderName, csrf)
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "session-1"})
	return request
}

type fakeAPIClientFactory struct{ api fakeAPI }

func (f fakeAPIClientFactory) ForToken(token string) (RequestAPI, error) { return f.api, nil }

type fakeAPI struct {
	images           []dwpkv1alpha1.WorkspaceImage
	allowedImages    map[string]bool
	workspace        *dwpkv1alpha1.Workspace
	createErr        error
	userSpaces       []dwpkv1alpha1.UserSpace
	workspaces       []dwpkv1alpha1.Workspace
	events           []corev1.Event
	allowedVerbs     map[string]bool
	deleteErr        error
	deletedWS        *string
	deletedClaim     *string
	membership       *UserSpaceMembership
	membershipErr    error
	patchedQuota     *dwpkv1alpha1.UserSpaceQuota
	createdUS        *dwpkv1alpha1.UserSpace
	deletedUS        *string
	listedNamespaces *[]string
	createdImage     *dwpkv1alpha1.WorkspaceImage
	editedImage      *WorkspaceImageEdit
	deletedImage     *string
	patchedKeys      *[]string
	patchedOnboarded *string
	platformConfig   *dwpkv1alpha1.PlatformConfig
	patchedConfig    *PlatformConfigUpdate
	logs             string
	logsErr          error
	imageRegistries  []dwpkv1alpha1.ImageRegistry
	createdRegistry  *dwpkv1alpha1.ImageRegistry
	editedRegistry   *ImageRegistryEdit
	deletedRegistry  *string
	forceSyncedName  *string
	gitSSHKeys       []GitSSHKeyInfo
	gitSSHKeysErr    error
	putGitSSHHost    *string
	putGitSSHKeyPEM  *[]byte
	putGitSSHErr     error
	deletedGitSSHKey *string
}

func (f fakeAPI) ListWorkspaceImages(ctx context.Context) ([]dwpkv1alpha1.WorkspaceImage, error) {
	return f.images, nil
}
func (f fakeAPI) GetWorkspaceImage(ctx context.Context, name string) (*dwpkv1alpha1.WorkspaceImage, error) {
	for _, image := range f.images {
		if image.Name == name {
			copy := image
			return &copy, nil
		}
	}
	return &dwpkv1alpha1.WorkspaceImage{}, nil
}
func (f fakeAPI) CreateWorkspaceImage(ctx context.Context, image *dwpkv1alpha1.WorkspaceImage) error {
	if f.createdImage != nil {
		*f.createdImage = *image
	}
	return nil
}
func (f fakeAPI) PatchWorkspaceImage(ctx context.Context, edit WorkspaceImageEdit) error {
	if f.editedImage != nil {
		*f.editedImage = edit
	}
	return nil
}
func (f fakeAPI) DeleteWorkspaceImage(ctx context.Context, name string) error {
	if f.deletedImage != nil {
		*f.deletedImage = name
	}
	return nil
}
func (f fakeAPI) CanUseWorkspaceImage(ctx context.Context, name string) (bool, error) {
	return f.allowedImages[name], nil
}
func (f fakeAPI) ListImageRegistries(ctx context.Context) ([]dwpkv1alpha1.ImageRegistry, error) {
	return f.imageRegistries, nil
}
func (f fakeAPI) GetImageRegistry(ctx context.Context, name string) (*dwpkv1alpha1.ImageRegistry, error) {
	for _, reg := range f.imageRegistries {
		if reg.Name == name {
			copy := reg
			return &copy, nil
		}
	}
	return &dwpkv1alpha1.ImageRegistry{}, nil
}
func (f fakeAPI) CreateImageRegistry(ctx context.Context, reg *dwpkv1alpha1.ImageRegistry) error {
	if f.createdRegistry != nil {
		*f.createdRegistry = *reg
	}
	return nil
}
func (f fakeAPI) PatchImageRegistry(ctx context.Context, edit ImageRegistryEdit) error {
	if f.editedRegistry != nil {
		*f.editedRegistry = edit
	}
	return nil
}
func (f fakeAPI) DeleteImageRegistry(ctx context.Context, name string) error {
	if f.deletedRegistry != nil {
		*f.deletedRegistry = name
	}
	return nil
}
func (f fakeAPI) ForceSyncImageRegistry(ctx context.Context, name string) error {
	if f.forceSyncedName != nil {
		*f.forceSyncedName = name
	}
	return nil
}
func (f fakeAPI) GetGitSSHKeys(ctx context.Context, namespace string) ([]GitSSHKeyInfo, error) {
	return f.gitSSHKeys, f.gitSSHKeysErr
}
func (f fakeAPI) PutGitSSHKey(ctx context.Context, namespace, host string, privateKeyPEM []byte) error {
	if f.putGitSSHErr != nil {
		return f.putGitSSHErr
	}
	if f.putGitSSHHost != nil {
		*f.putGitSSHHost = host
	}
	if f.putGitSSHKeyPEM != nil {
		*f.putGitSSHKeyPEM = privateKeyPEM
	}
	return nil
}
func (f fakeAPI) DeleteGitSSHKey(ctx context.Context, namespace, host string) error {
	if f.deletedGitSSHKey != nil {
		*f.deletedGitSSHKey = host
	}
	return nil
}
func (f fakeAPI) CanI(ctx context.Context, verb, resourceName, name string) (bool, error) {
	return f.allowedVerbs[verb+" "+resourceName], nil
}
func (f fakeAPI) CreateWorkspace(ctx context.Context, workspace *dwpkv1alpha1.Workspace) error {
	return f.createErr
}
func (f fakeAPI) DeleteWorkspace(ctx context.Context, namespace, name string) error {
	if f.deletedWS != nil {
		*f.deletedWS = namespace + "/" + name
	}
	return f.deleteErr
}
func (f fakeAPI) DeleteClaim(ctx context.Context, namespace, name string) error {
	if f.deletedClaim != nil {
		*f.deletedClaim = namespace + "/" + name
	}
	return nil
}
func (f fakeAPI) GetWorkspace(ctx context.Context, namespace, name string) (*dwpkv1alpha1.Workspace, error) {
	if f.workspace != nil {
		return f.workspace, nil
	}
	return &dwpkv1alpha1.Workspace{}, nil
}
func (f fakeAPI) PatchWorkspaceRunning(ctx context.Context, namespace, name string, running bool) (*dwpkv1alpha1.Workspace, error) {
	if f.workspace == nil {
		f.workspace = &dwpkv1alpha1.Workspace{}
	}
	copy := f.workspace.DeepCopy()
	copy.Spec.Running = running
	return copy, nil
}
func (f fakeAPI) PatchWorkspaceResources(
	ctx context.Context, namespace, name string,
	resources corev1.ResourceRequirements, env []corev1.EnvVar,
) (*dwpkv1alpha1.Workspace, error) {
	if f.workspace == nil {
		f.workspace = &dwpkv1alpha1.Workspace{}
	}
	copy := f.workspace.DeepCopy()
	copy.Spec.Resources = resources
	copy.Spec.Env = env
	return copy, nil
}
func (f fakeAPI) ListUserSpaces(ctx context.Context) ([]dwpkv1alpha1.UserSpace, error) {
	return f.userSpaces, nil
}
func (f fakeAPI) GetUserSpace(ctx context.Context, name string) (*dwpkv1alpha1.UserSpace, error) {
	for i := range f.userSpaces {
		if f.userSpaces[i].Name == name {
			return &f.userSpaces[i], nil
		}
	}
	return nil, apierrors.NewNotFound(dwpkv1alpha1.GroupVersion.WithResource("userspaces").GroupResource(), name)
}
func (f fakeAPI) PatchUserSpaceMembership(ctx context.Context, membership UserSpaceMembership) error {
	if f.membership != nil {
		*f.membership = membership
	}
	return f.membershipErr
}
func (f fakeAPI) CreateUserSpace(ctx context.Context, us *dwpkv1alpha1.UserSpace) error {
	if f.createdUS != nil {
		*f.createdUS = *us
	}
	return nil
}
func (f fakeAPI) DeleteUserSpace(ctx context.Context, name string) error {
	if f.deletedUS != nil {
		*f.deletedUS = name
	}
	return nil
}
func (f fakeAPI) PatchUserSpaceQuota(ctx context.Context, name string, quota dwpkv1alpha1.UserSpaceQuota) error {
	if f.patchedQuota != nil {
		*f.patchedQuota = quota
	}
	return nil
}
func (f fakeAPI) PatchUserSpaceKeys(ctx context.Context, name string, keys []string) error {
	if f.patchedKeys != nil {
		*f.patchedKeys = keys
	}
	return nil
}
func (f fakeAPI) PatchUserSpaceOnboardingCompleted(ctx context.Context, name string) error {
	if f.patchedOnboarded != nil {
		*f.patchedOnboarded = name
	}
	return nil
}

// ListWorkspaces honours the namespace the way the API server does, so a test
// can tell a cluster-wide list from a set of namespaced ones.
func (f fakeAPI) ListWorkspaces(ctx context.Context, namespace string) ([]dwpkv1alpha1.Workspace, error) {
	if f.listedNamespaces != nil {
		*f.listedNamespaces = append(*f.listedNamespaces, namespace)
	}
	if namespace == "" {
		return f.workspaces, nil
	}
	matching := []dwpkv1alpha1.Workspace{}
	for _, ws := range f.workspaces {
		if ws.Namespace == namespace {
			matching = append(matching, ws)
		}
	}
	return matching, nil
}
func (f fakeAPI) WorkspaceLogs(ctx context.Context, namespace, pod string, tailLines int64) (io.ReadCloser, error) {
	if f.logsErr != nil {
		return nil, f.logsErr
	}
	return io.NopCloser(strings.NewReader(f.logs)), nil
}
func (f fakeAPI) GetPlatformConfig(ctx context.Context) (*dwpkv1alpha1.PlatformConfig, error) {
	if f.platformConfig != nil {
		return f.platformConfig, nil
	}
	return nil, apierrors.NewNotFound(
		dwpkv1alpha1.GroupVersion.WithResource("platformconfigs").GroupResource(),
		dwpkv1alpha1.PlatformConfigName)
}
func (f fakeAPI) PatchPlatformConfig(ctx context.Context, update PlatformConfigUpdate) error {
	if f.patchedConfig != nil {
		*f.patchedConfig = update
	}
	return nil
}
func (f fakeAPI) OpenTerminal(ctx context.Context, req TerminalStreamRequest) error { return nil }

var _ RequestAPI = fakeAPI{}
var _ remotecommand.TerminalSizeQueue
var _ = corev1.ConditionTrue
var _ = resource.MustParse
var _ = time.Second

// A create form submitted with no key must leave sshAuthorizedKeys nil, not
// []string{""}. A one-element list holding an empty string is not empty: it
// would stop the mutating webhook defaulting the owner's keys in, and then be
// refused by the validator for containing a blank key.
func TestBuildWorkspaceOmitsABlankKeyEntirely(t *testing.T) {
	t.Parallel()

	draft := WorkspaceDraft{
		Name:      "dev",
		Namespace: testOwnerNS,
		Image:     testImageName,
		SSHKey:    "   ",
		Resources: defaultResourceValues(),
	}
	blank, err := buildWorkspace(draft)
	if err != nil {
		t.Fatalf("buildWorkspace() error = %v", err)
	}
	if len(blank.Spec.SSHAuthorizedKeys) != 0 {
		t.Fatalf("a blank key produced %#v, want nothing", blank.Spec.SSHAuthorizedKeys)
	}

	draft.SSHKey = "ssh-ed25519 AAAA me"
	given, err := buildWorkspace(draft)
	if err != nil {
		t.Fatalf("buildWorkspace() error = %v", err)
	}
	if len(given.Spec.SSHAuthorizedKeys) != 1 {
		t.Fatalf("a supplied key produced %#v", given.Spec.SSHAuthorizedKeys)
	}
}

func (f fakeAPI) ListEvents(ctx context.Context, namespace string) ([]corev1.Event, error) {
	return f.events, nil
}
