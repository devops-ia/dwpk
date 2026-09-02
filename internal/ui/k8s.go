package ui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	dwpkv1alpha1 "github.com/devops-ia/dwpk/api/v1alpha1"
	"github.com/devops-ia/dwpk/internal/gateway"
	workspacepkg "github.com/devops-ia/dwpk/internal/workspace"
	"golang.org/x/crypto/ssh"
	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// jsonPatchAddOp is the JSON Patch "add" op - see the comment above
// PatchWorkspaceResources for why "add" rather than "replace".
const jsonPatchAddOp = "add"

// specKey is the JSON Merge Patch top-level key every PatchX helper in this
// file writes into: {"spec": ...}.
const specKey = "spec"

type RequestAPI interface {
	ListWorkspaceImages(ctx context.Context) ([]dwpkv1alpha1.WorkspaceImage, error)
	GetWorkspaceImage(ctx context.Context, name string) (*dwpkv1alpha1.WorkspaceImage, error)
	CreateWorkspaceImage(ctx context.Context, image *dwpkv1alpha1.WorkspaceImage) error
	PatchWorkspaceImage(ctx context.Context, edit WorkspaceImageEdit) error
	DeleteWorkspaceImage(ctx context.Context, name string) error
	CanUseWorkspaceImage(ctx context.Context, name string) (bool, error)
	ListImageRegistries(ctx context.Context) ([]dwpkv1alpha1.ImageRegistry, error)
	GetImageRegistry(ctx context.Context, name string) (*dwpkv1alpha1.ImageRegistry, error)
	CreateImageRegistry(ctx context.Context, reg *dwpkv1alpha1.ImageRegistry) error
	PatchImageRegistry(ctx context.Context, edit ImageRegistryEdit) error
	DeleteImageRegistry(ctx context.Context, name string) error
	ForceSyncImageRegistry(ctx context.Context, name string) error
	GetGitSSHKeys(ctx context.Context, namespace string) ([]GitSSHKeyInfo, error)
	PutGitSSHKey(ctx context.Context, namespace, host string, privateKeyPEM []byte) error
	DeleteGitSSHKey(ctx context.Context, namespace, host string) error
	CanI(ctx context.Context, verb, resourceName, name string) (bool, error)
	CreateWorkspace(ctx context.Context, workspace *dwpkv1alpha1.Workspace) error
	GetWorkspace(ctx context.Context, namespace, name string) (*dwpkv1alpha1.Workspace, error)
	DeleteWorkspace(ctx context.Context, namespace, name string) error
	DeleteClaim(ctx context.Context, namespace, name string) error
	PatchWorkspaceRunning(ctx context.Context, namespace, name string, running bool) (*dwpkv1alpha1.Workspace, error)
	PatchWorkspaceResources(
		ctx context.Context, namespace, name string,
		resources corev1.ResourceRequirements, env []corev1.EnvVar,
	) (*dwpkv1alpha1.Workspace, error)
	GetUserSpace(ctx context.Context, name string) (*dwpkv1alpha1.UserSpace, error)
	ListUserSpaces(ctx context.Context) ([]dwpkv1alpha1.UserSpace, error)
	CreateUserSpace(ctx context.Context, userSpace *dwpkv1alpha1.UserSpace) error
	DeleteUserSpace(ctx context.Context, name string) error
	PatchUserSpaceMembership(ctx context.Context, membership UserSpaceMembership) error
	PatchUserSpaceQuota(ctx context.Context, name string, quota dwpkv1alpha1.UserSpaceQuota) error
	PatchUserSpaceKeys(ctx context.Context, name string, keys []string) error
	PatchUserSpaceOnboardingCompleted(ctx context.Context, name string) error
	ListWorkspaces(ctx context.Context, namespace string) ([]dwpkv1alpha1.Workspace, error)
	WorkspaceLogs(ctx context.Context, namespace, pod string, tailLines int64) (io.ReadCloser, error)
	GetPlatformConfig(ctx context.Context) (*dwpkv1alpha1.PlatformConfig, error)
	PatchPlatformConfig(ctx context.Context, update PlatformConfigUpdate) error
	OpenTerminal(ctx context.Context, req TerminalStreamRequest) error
	ListEvents(ctx context.Context, namespace string) ([]corev1.Event, error)
}

type APIClientFactory interface {
	ForToken(token string) (RequestAPI, error)
}

type ClientFactory struct {
	baseConfig *rest.Config
	scheme     *runtime.Scheme
	// gitSSHEncryptionKey encrypts every git-ssh key PutGitSSHKey writes. It
	// is read once at startup with the UI's own credential (never the
	// caller's forwarded token - see cmd/ui/main.go), and carried on every
	// requestAPI this factory produces, the same way gatewayHost is.
	gitSSHEncryptionKey []byte
}

type TerminalStreamRequest struct {
	Namespace string
	Name      string
	Term      string
	Stdin     io.Reader
	Stdout    io.Writer
	Stderr    io.Writer
	Sizes     remotecommand.TerminalSizeQueue
}

func NewClientFactory(baseConfig *rest.Config, scheme *runtime.Scheme, gitSSHEncryptionKey []byte) *ClientFactory {
	return &ClientFactory{baseConfig: rest.CopyConfig(baseConfig), scheme: scheme, gitSSHEncryptionKey: gitSSHEncryptionKey}
}

func (f *ClientFactory) ForToken(token string) (RequestAPI, error) {
	config := rest.CopyConfig(f.baseConfig)
	config.BearerToken = token
	config.BearerTokenFile = ""
	config.Username = ""
	config.Password = ""
	config.CertFile = ""
	config.KeyFile = ""

	workspaceClient, err := ctrlclient.New(config, ctrlclient.Options{Scheme: f.scheme})
	if err != nil {
		return nil, fmt.Errorf("create controller-runtime client: %w", err)
	}
	kubeClient, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("create kubernetes clientset: %w", err)
	}
	return &requestAPI{
		workspaceClient:     workspaceClient,
		kubeClient:          kubeClient,
		restConfig:          config,
		gitSSHEncryptionKey: f.gitSSHEncryptionKey,
	}, nil
}

type requestAPI struct {
	workspaceClient     ctrlclient.Client
	kubeClient          kubernetes.Interface
	restConfig          *rest.Config
	gitSSHEncryptionKey []byte
}

func (a *requestAPI) ListWorkspaceImages(ctx context.Context) ([]dwpkv1alpha1.WorkspaceImage, error) {
	var list dwpkv1alpha1.WorkspaceImageList
	if err := a.workspaceClient.List(ctx, &list); err != nil {
		return nil, fmt.Errorf("list WorkspaceImages: %w", err)
	}
	return list.Items, nil
}

func (a *requestAPI) GetWorkspaceImage(ctx context.Context, name string) (*dwpkv1alpha1.WorkspaceImage, error) {
	img := &dwpkv1alpha1.WorkspaceImage{}
	if err := a.workspaceClient.Get(ctx, ctrlclient.ObjectKey{Name: name}, img); err != nil {
		return nil, fmt.Errorf("get WorkspaceImage %q: %w", name, err)
	}
	return img, nil
}

// WorkspaceImageEdit is the administrator-editable part of a catalog entry.
// metadata.name is not in it: a rename is a delete and a recreate, and the
// form says so rather than pretending otherwise.
type WorkspaceImageEdit struct {
	Name string
	// Image is the container reference. It is editable: pinning a catalog entry
	// to a new tag is the ordinary way to roll an image forward, and doing it by
	// deleting and recreating the entry would break every Workspace pointing at
	// it in between.
	Image       string
	DisplayName string
	Description string
	Icon        string
	Tags        []string
	Deprecated  bool
	// DeprecateAt schedules deprecation. Nil clears any existing date.
	DeprecateAt *metav1.Time
	// AllowRoot runs the container as uid 0. Admin-only, enforced by the
	// WorkspaceImage validator rather than here.
	AllowRoot bool
	// ImagePullSecretRef names a pull Secret for a private image. Nil clears
	// it.
	ImagePullSecretRef *corev1.LocalObjectReference
}

type workspaceImagePatch struct {
	Spec workspaceImagePatchSpec `json:"spec"`
}

type workspaceImagePatchSpec struct {
	// omitempty: a merge patch that sends "image": "" would blank a required
	// field. An edit that does not mention the image leaves it alone.
	Image       string   `json:"image,omitempty"`
	DisplayName string   `json:"displayName"`
	Description string   `json:"description"`
	Icon        string   `json:"icon"`
	Tags        []string `json:"tags"`
	Deprecated  bool     `json:"deprecated"`
	// Both lists are always sent, empty included: the form submits the whole
	// selection, so clearing one has to mean "none" rather than "unchanged".
	// Always sent, null included: clearing the date has to mean "no longer
	// scheduled" rather than "unchanged", which an omitempty would swallow.
	DeprecateAt *metav1.Time `json:"deprecateAt"`
	// Always sent, false included: unticking the box has to mean "no longer
	// root", which an omitempty would silently drop.
	AllowRoot bool `json:"allowRoot"`
	// Always sent, null included: clearing the pull secret has to mean "no
	// longer needed" rather than "unchanged".
	ImagePullSecretRef *corev1.LocalObjectReference `json:"imagePullSecretRef"`
}

func (a *requestAPI) CreateWorkspaceImage(ctx context.Context, image *dwpkv1alpha1.WorkspaceImage) error {
	if err := a.workspaceClient.Create(ctx, image); err != nil {
		return fmt.Errorf("create WorkspaceImage %s: %w", image.Name, err)
	}
	return nil
}

// PatchWorkspaceImage replaces the editable fields wholesale. A merge patch
// swaps a list rather than merging it, which is exactly what a full-form edit
// of tags or sizes means.
func (a *requestAPI) PatchWorkspaceImage(ctx context.Context, edit WorkspaceImageEdit) error {
	img := &dwpkv1alpha1.WorkspaceImage{}
	img.Name = edit.Name
	body, err := json.Marshal(workspaceImagePatch{Spec: workspaceImagePatchSpec{
		Image:              edit.Image,
		DisplayName:        edit.DisplayName,
		Description:        edit.Description,
		Icon:               edit.Icon,
		Tags:               edit.Tags,
		Deprecated:         edit.Deprecated,
		DeprecateAt:        edit.DeprecateAt,
		AllowRoot:          edit.AllowRoot,
		ImagePullSecretRef: edit.ImagePullSecretRef,
	}})
	if err != nil {
		return fmt.Errorf("build WorkspaceImage patch for %s: %w", edit.Name, err)
	}
	if err := a.workspaceClient.Patch(ctx, img, ctrlclient.RawPatch(types.MergePatchType, body)); err != nil {
		return fmt.Errorf("patch WorkspaceImage %s: %w", edit.Name, err)
	}
	return nil
}

func (a *requestAPI) DeleteWorkspaceImage(ctx context.Context, name string) error {
	img := &dwpkv1alpha1.WorkspaceImage{}
	img.Name = name
	if err := a.workspaceClient.Delete(ctx, img); err != nil {
		return fmt.Errorf("delete WorkspaceImage %s: %w", name, err)
	}
	return nil
}

func (a *requestAPI) CanUseWorkspaceImage(ctx context.Context, name string) (bool, error) {
	return a.CanI(ctx, "use", "workspaceimages", name)
}

func (a *requestAPI) ListImageRegistries(ctx context.Context) ([]dwpkv1alpha1.ImageRegistry, error) {
	var list dwpkv1alpha1.ImageRegistryList
	if err := a.workspaceClient.List(ctx, &list); err != nil {
		return nil, fmt.Errorf("list ImageRegistries: %w", err)
	}
	return list.Items, nil
}

func (a *requestAPI) GetImageRegistry(ctx context.Context, name string) (*dwpkv1alpha1.ImageRegistry, error) {
	reg := &dwpkv1alpha1.ImageRegistry{}
	if err := a.workspaceClient.Get(ctx, ctrlclient.ObjectKey{Name: name}, reg); err != nil {
		return nil, fmt.Errorf("get ImageRegistry %q: %w", name, err)
	}
	return reg, nil
}

func (a *requestAPI) CreateImageRegistry(ctx context.Context, reg *dwpkv1alpha1.ImageRegistry) error {
	if err := a.workspaceClient.Create(ctx, reg); err != nil {
		return fmt.Errorf("create ImageRegistry %s: %w", reg.Name, err)
	}
	return nil
}

// ImageRegistryEdit is the administrator-editable part of an ImageRegistry.
// Name and Provider are not in it: a rename is a delete and a recreate, and
// changing provider mid-life would leave every already-synced WorkspaceImage
// pointing at a client built for the old one.
type ImageRegistryEdit struct {
	Name               string
	Region             string
	RegistryID         string
	RoleARN            string
	IntervalSeconds    int32
	Include            []string
	Exclude            []string
	TagMode            string
	TagPatterns        []string
	TagLimit           int32
	Prune              bool
	ImagePullSecretRef *corev1.LocalObjectReference
}

type imageRegistryPatch struct {
	Spec imageRegistryPatchSpec `json:"spec"`
}

// imageRegistryPatchSpec re-sends every editable field on every save, exactly
// like workspaceImagePatchSpec: this is a full-form edit, and an omitempty
// here would mean an emptied RoleARN or a cleared ImagePullSecretRef is
// silently ignored rather than cleared.
type imageRegistryPatchSpec struct {
	AWS                imageRegistryPatchAWS        `json:"aws"`
	Sync               imageRegistryPatchSync       `json:"sync"`
	ImagePullSecretRef *corev1.LocalObjectReference `json:"imagePullSecretRef"`
}

type imageRegistryPatchAWS struct {
	Region     string `json:"region"`
	RegistryID string `json:"registryId"`
	RoleARN    string `json:"roleArn"`
}

type imageRegistryPatchSync struct {
	IntervalSeconds int32                  `json:"intervalSeconds"`
	Include         []string               `json:"include"`
	Exclude         []string               `json:"exclude"`
	Tags            imageRegistryPatchTags `json:"tags"`
	Prune           bool                   `json:"prune"`
}

type imageRegistryPatchTags struct {
	Mode     string   `json:"mode"`
	Patterns []string `json:"patterns"`
	Limit    int32    `json:"limit"`
}

// imageRegistryMergePatch is the pure builder behind PatchImageRegistry - no
// client, no context, table-testable, per this repo's Clean Code rule that
// desired-state construction stays a pure function.
func imageRegistryMergePatch(edit ImageRegistryEdit) ([]byte, error) {
	return json.Marshal(imageRegistryPatch{Spec: imageRegistryPatchSpec{
		AWS: imageRegistryPatchAWS{
			Region:     edit.Region,
			RegistryID: edit.RegistryID,
			RoleARN:    edit.RoleARN,
		},
		Sync: imageRegistryPatchSync{
			IntervalSeconds: edit.IntervalSeconds,
			Include:         edit.Include,
			Exclude:         edit.Exclude,
			Tags: imageRegistryPatchTags{
				Mode:     edit.TagMode,
				Patterns: edit.TagPatterns,
				Limit:    edit.TagLimit,
			},
			Prune: edit.Prune,
		},
		ImagePullSecretRef: edit.ImagePullSecretRef,
	}})
}

func (a *requestAPI) PatchImageRegistry(ctx context.Context, edit ImageRegistryEdit) error {
	reg := &dwpkv1alpha1.ImageRegistry{}
	reg.Name = edit.Name
	body, err := imageRegistryMergePatch(edit)
	if err != nil {
		return fmt.Errorf("build ImageRegistry patch for %s: %w", edit.Name, err)
	}
	if err := a.workspaceClient.Patch(ctx, reg, ctrlclient.RawPatch(types.MergePatchType, body)); err != nil {
		return fmt.Errorf("patch ImageRegistry %s: %w", edit.Name, err)
	}
	return nil
}

func (a *requestAPI) DeleteImageRegistry(ctx context.Context, name string) error {
	reg := &dwpkv1alpha1.ImageRegistry{}
	reg.Name = name
	if err := a.workspaceClient.Delete(ctx, reg); err != nil {
		return fmt.Errorf("delete ImageRegistry %s: %w", name, err)
	}
	return nil
}

// ForceSyncImageRegistry retriggers a sync outside its configured interval by
// bumping an annotation - the controller's own watch already delivers an
// annotation-only update, so this needs no field on the CRD and no dedicated
// controller logic. The value must change on every call, or the merge patch
// is a no-op: sending the same annotation value twice produces no update
// event at all.
func (a *requestAPI) ForceSyncImageRegistry(ctx context.Context, name string) error {
	reg := &dwpkv1alpha1.ImageRegistry{}
	reg.Name = name
	body := fmt.Appendf(nil, `{"metadata":{"annotations":{"dwpk.devops-ia.io/force-sync":%q}}}`,
		time.Now().UTC().Format(time.RFC3339Nano))
	if err := a.workspaceClient.Patch(ctx, reg, ctrlclient.RawPatch(types.MergePatchType, body)); err != nil {
		return fmt.Errorf("force-sync ImageRegistry %s: %w", name, err)
	}
	return nil
}

// ErrGitSSHKeyExists is returned by PutGitSSHKey when the host already has a
// key - remove it first, rather than silently overwriting a key that might
// still be in use elsewhere.
var ErrGitSSHKeyExists = errors.New("a key for that host already exists")

// errGitSSHEncryptionNotConfigured is returned by PutGitSSHKey when the UI
// never found an encryption key at startup (see cmd/ui/main.go) - refusing
// to write is the only honest option; there is nothing else to encrypt under.
var errGitSSHEncryptionNotConfigured = errors.New(
	"git-ssh key storage is not configured on this platform - ask an administrator")

// GitSSHKeyInfo is one stored key as far as anything outside this function
// ever sees it - never the key material itself, matching LocalUser's rule
// that a credential store never hands the credential back out.
type GitSSHKeyInfo struct {
	Host        string
	Fingerprint string
	KeyType     string
}

// GetGitSSHKeys lists the caller's own git-ssh keys. A missing Secret is not
// an error - it means no keys have been added yet.
//
// Reads only the unencrypted GitSSHKeyMetaDataPrefix entry per host - never
// the ciphertext, and never decrypts. Listing needs a fingerprint and a key
// type, not the key itself, so it needs no access to the encryption key at
// all.
func (a *requestAPI) GetGitSSHKeys(ctx context.Context, namespace string) ([]GitSSHKeyInfo, error) {
	secret := &corev1.Secret{}
	err := a.workspaceClient.Get(ctx, ctrlclient.ObjectKey{Namespace: namespace, Name: dwpkv1alpha1.GitSSHKeysSecretName}, secret)
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get git-ssh keys in %s: %w", namespace, err)
	}

	infos := make([]GitSSHKeyInfo, 0, len(secret.Data))
	for key, meta := range secret.Data {
		host, ok := strings.CutPrefix(key, dwpkv1alpha1.GitSSHKeyMetaDataPrefix)
		if !ok {
			continue
		}
		info := GitSSHKeyInfo{Host: host, KeyType: "unrecognised"}
		if keyType, fingerprint, ok := strings.Cut(string(meta), " "); ok {
			info.KeyType, info.Fingerprint = keyType, fingerprint
		}
		infos = append(infos, info)
	}
	return infos, nil
}

// PutGitSSHKey adds a new key for a host that does not already have one, and
// regenerates the config entry every mounted workspace's GIT_SSH_COMMAND
// reads. Get-then-create-or-patch, not a merge patch: a Secret's Data map is
// wholesale-replaced by design here (see workspace.GitSSHConfig), the same
// correctness requirement Phase 1's JSON-Patch work already established for
// spec.env.
//
// privateKeyPEM arrives already validated as a real, unencrypted OpenSSH
// private key (profile_git_ssh.go's ssh.ParsePrivateKey check) - fingerprint
// and type are derived from that same parse, once, before the plaintext is
// ever encrypted, so the Secret this writes carries only ciphertext plus the
// two facts about it that GetGitSSHKeys needs to list it back.
func (a *requestAPI) PutGitSSHKey(ctx context.Context, namespace, host string, privateKeyPEM []byte) error {
	if len(a.gitSSHEncryptionKey) == 0 {
		return errGitSSHEncryptionNotConfigured
	}
	signer, err := ssh.ParsePrivateKey(privateKeyPEM)
	if err != nil {
		return fmt.Errorf("parse validated private key: %w", err)
	}
	ciphertext, err := workspacepkg.EncryptGitSSHKey(a.gitSSHEncryptionKey, privateKeyPEM)
	if err != nil {
		return fmt.Errorf("encrypt git-ssh key for %s: %w", host, err)
	}

	secret := &corev1.Secret{}
	err = a.workspaceClient.Get(ctx, ctrlclient.ObjectKey{Namespace: namespace, Name: dwpkv1alpha1.GitSSHKeysSecretName}, secret)
	switch {
	case apierrors.IsNotFound(err):
		secret = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: dwpkv1alpha1.GitSSHKeysSecretName, Namespace: namespace},
			Type:       corev1.SecretTypeOpaque,
			Data:       map[string][]byte{},
		}
	case err != nil:
		return fmt.Errorf("get git-ssh keys in %s: %w", namespace, err)
	}

	dataKey := dwpkv1alpha1.GitSSHKeyDataPrefix + host
	if _, exists := secret.Data[dataKey]; exists {
		return ErrGitSSHKeyExists
	}

	original := secret.DeepCopy()
	if secret.Data == nil {
		secret.Data = map[string][]byte{}
	}
	secret.Data[dataKey] = ciphertext
	secret.Data[dwpkv1alpha1.GitSSHKeyMetaDataPrefix+host] = []byte(signer.PublicKey().Type() + " " + ssh.FingerprintSHA256(signer.PublicKey()))
	secret.Data["config"] = []byte(workspacepkg.GitSSHConfig(workspacepkg.GitSSHHostsFromData(secret.Data)))

	if secret.ResourceVersion == "" {
		if err := a.workspaceClient.Create(ctx, secret); err != nil {
			return fmt.Errorf("create git-ssh keys Secret in %s: %w", namespace, err)
		}
		return nil
	}
	if err := a.workspaceClient.Patch(ctx, secret, ctrlclient.MergeFrom(original)); err != nil {
		return fmt.Errorf("patch git-ssh keys Secret in %s: %w", namespace, err)
	}
	return nil
}

// DeleteGitSSHKey removes one host's key. Deleting the Secret entirely once
// the last key is gone, rather than leaving an empty one behind: a Workspace
// mounts this Secret whenever it exists, so a key-less Secret would still
// mount a now-pointless volume into every workspace in the namespace.
func (a *requestAPI) DeleteGitSSHKey(ctx context.Context, namespace, host string) error {
	secret := &corev1.Secret{}
	err := a.workspaceClient.Get(ctx, ctrlclient.ObjectKey{Namespace: namespace, Name: dwpkv1alpha1.GitSSHKeysSecretName}, secret)
	if apierrors.IsNotFound(err) {
		return nil // already gone
	}
	if err != nil {
		return fmt.Errorf("get git-ssh keys in %s: %w", namespace, err)
	}

	dataKey := dwpkv1alpha1.GitSSHKeyDataPrefix + host
	if _, exists := secret.Data[dataKey]; !exists {
		return nil // already gone
	}

	remainingHosts := workspacepkg.GitSSHHostsFromData(secret.Data)
	remainingHosts = slices.DeleteFunc(remainingHosts, func(h string) bool { return h == host })
	if len(remainingHosts) == 0 {
		if err := a.workspaceClient.Delete(ctx, secret); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete git-ssh keys Secret in %s: %w", namespace, err)
		}
		return nil
	}

	original := secret.DeepCopy()
	delete(secret.Data, dataKey)
	delete(secret.Data, dwpkv1alpha1.GitSSHKeyMetaDataPrefix+host)
	secret.Data["config"] = []byte(workspacepkg.GitSSHConfig(remainingHosts))
	if err := a.workspaceClient.Patch(ctx, secret, ctrlclient.MergeFrom(original)); err != nil {
		return fmt.Errorf("patch git-ssh keys Secret in %s: %w", namespace, err)
	}
	return nil
}

// CanI asks the API server whether the caller's own identity permits an
// action, so authorization decisions stay with Kubernetes RBAC rather than
// being re-implemented in the UI (§8.1).
func (a *requestAPI) CanI(ctx context.Context, verb, resourceName, name string) (bool, error) {
	review, err := a.kubeClient.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, &authorizationv1.SelfSubjectAccessReview{
		Spec: authorizationv1.SelfSubjectAccessReviewSpec{
			ResourceAttributes: &authorizationv1.ResourceAttributes{
				Group:    dwpkv1alpha1.GroupVersion.Group,
				Verb:     verb,
				Resource: resourceName,
				Name:     name,
			},
		},
	}, metav1.CreateOptions{})
	if err != nil {
		return false, fmt.Errorf("check %s permission on %s %q: %w", verb, resourceName, name, err)
	}
	return review.Status.Allowed, nil
}

func (a *requestAPI) CreateWorkspace(ctx context.Context, ws *dwpkv1alpha1.Workspace) error {
	if err := a.workspaceClient.Create(ctx, ws); err != nil {
		return fmt.Errorf("create Workspace %s/%s: %w", ws.Namespace, ws.Name, err)
	}
	return nil
}

func (a *requestAPI) GetWorkspace(ctx context.Context, namespace, name string) (*dwpkv1alpha1.Workspace, error) {
	ws := &dwpkv1alpha1.Workspace{}
	if err := a.workspaceClient.Get(ctx, ctrlclient.ObjectKey{Namespace: namespace, Name: name}, ws); err != nil {
		return nil, fmt.Errorf("get Workspace %s/%s: %w", namespace, name, err)
	}
	return ws, nil
}

func (a *requestAPI) DeleteWorkspace(ctx context.Context, namespace, name string) error {
	ws := &dwpkv1alpha1.Workspace{}
	ws.Namespace = namespace
	ws.Name = name
	if err := a.workspaceClient.Delete(ctx, ws); err != nil {
		return fmt.Errorf("delete Workspace %s/%s: %w", namespace, name, err)
	}
	return nil
}

// DeleteClaim removes a PersistentVolumeClaim by name.
//
// An already-absent claim is not an error: the caller has just deleted the
// workspace, and a volume that was never provisioned, or already removed by
// hand, means the same thing to them as one this call deleted.
func (a *requestAPI) DeleteClaim(ctx context.Context, namespace, name string) error {
	if err := a.kubeClient.CoreV1().PersistentVolumeClaims(namespace).
		Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete PersistentVolumeClaim %s/%s: %w", namespace, name, err)
	}
	return nil
}

func (a *requestAPI) PatchWorkspaceRunning(ctx context.Context, namespace, name string, running bool) (*dwpkv1alpha1.Workspace, error) {
	ws := &dwpkv1alpha1.Workspace{}
	ws.Namespace = namespace
	ws.Name = name
	patch := ctrlclient.RawPatch(types.MergePatchType, fmt.Appendf(nil, `{"spec":{"running":%t}}`, running))
	if err := a.workspaceClient.Patch(ctx, ws, patch); err != nil {
		return nil, fmt.Errorf("patch Workspace %s/%s running=%t: %w", namespace, name, running, err)
	}
	return a.GetWorkspace(ctx, namespace, name)
}

type jsonPatchOp struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value any    `json:"value"`
}

// PatchWorkspaceResources replaces spec.resources and spec.env wholesale.
//
// A JSON Patch "add", not a merge patch like PatchWorkspaceRunning above.
// ResourceValues.Requirements omits empty Limits/Requests keys, and
// WorkspaceSpec.Env is omitempty - a JSON Merge Patch only ever merges
// object keys and never deletes one that's simply absent from the body, so
// an edit that removes the GPU or an env var would leave the old value in
// place forever. "add" rather than "replace": replace errors if the path
// doesn't already exist (a workspace created with no env vars has no
// spec.env key at all), while add both creates and overwrites.
func (a *requestAPI) PatchWorkspaceResources(
	ctx context.Context, namespace, name string,
	resources corev1.ResourceRequirements, env []corev1.EnvVar,
) (*dwpkv1alpha1.Workspace, error) {
	ws := &dwpkv1alpha1.Workspace{}
	ws.Namespace = namespace
	ws.Name = name
	body, err := workspaceResourcesPatch(resources, env)
	if err != nil {
		return nil, fmt.Errorf("build resources patch for %s/%s: %w", namespace, name, err)
	}
	if err := a.workspaceClient.Patch(ctx, ws, ctrlclient.RawPatch(types.JSONPatchType, body)); err != nil {
		return nil, fmt.Errorf("patch Workspace %s/%s resources: %w", namespace, name, err)
	}
	return a.GetWorkspace(ctx, namespace, name)
}

// workspaceResourcesPatch builds the JSON Patch body - a pure function, so
// the "add, not replace or merge" shape is table-testable with no client.
func workspaceResourcesPatch(resources corev1.ResourceRequirements, env []corev1.EnvVar) ([]byte, error) {
	return json.Marshal([]jsonPatchOp{
		{Op: jsonPatchAddOp, Path: "/spec/resources", Value: resources},
		{Op: jsonPatchAddOp, Path: "/spec/env", Value: env},
	})
}

// UserSpaceMembership is the administrator-editable part of a UserSpace: who
// they work with and whether they may log in. Grouped into a struct because
// the three settings are always changed from the same screen.
// PatchUserSpaceKeys replaces a person's default SSH keys.
//
// Replaces rather than merges: the screen sends the whole list every time, so
// removing a key is a shorter list rather than a second verb. A merge patch on
// a list replaces it wholesale, which is exactly what that needs.
func (a *requestAPI) PatchUserSpaceKeys(ctx context.Context, name string, keys []string) error {
	us := &dwpkv1alpha1.UserSpace{}
	us.Name = name
	body, err := json.Marshal(map[string]any{
		specKey: map[string]any{"sshAuthorizedKeys": keys},
	})
	if err != nil {
		return fmt.Errorf("build key patch for %s: %w", name, err)
	}
	if err := a.workspaceClient.Patch(ctx, us, ctrlclient.RawPatch(types.MergePatchType, body)); err != nil {
		return fmt.Errorf("patch UserSpace keys %s: %w", name, err)
	}
	return nil
}

// PatchUserSpaceOnboardingCompleted stamps the first-login wizard as done.
// Monotonic in intent: the handler only ever calls this once, and setting it
// again to the same effect is harmless, so no read-before-write is needed.
func (a *requestAPI) PatchUserSpaceOnboardingCompleted(ctx context.Context, name string) error {
	us := &dwpkv1alpha1.UserSpace{}
	us.Name = name
	now := metav1.Now()
	body, err := json.Marshal(map[string]any{
		specKey: map[string]any{"onboardingCompletedAt": now},
	})
	if err != nil {
		return fmt.Errorf("build onboarding patch for %s: %w", name, err)
	}
	if err := a.workspaceClient.Patch(ctx, us, ctrlclient.RawPatch(types.MergePatchType, body)); err != nil {
		return fmt.Errorf("patch UserSpace onboarding %s: %w", name, err)
	}
	return nil
}

type UserSpaceMembership struct {
	Name     string
	Role     string
	Disabled bool
}

// membershipPatch is the merge patch a membership change sends. It is a typed
// struct marshalled by encoding/json rather than a formatted string: a list
// cannot be built with %q, and a hand-written patch silently stops matching the
// API the moment a field is added.
type membershipPatch struct {
	Spec membershipPatchSpec `json:"spec"`
}

type membershipPatchSpec struct {
	Role     string `json:"role"`
	Disabled bool   `json:"disabled"`
}

// PatchUserSpaceMembership writes only the membership fields, so it cannot
// disturb the owner, quota or network policy the same object carries.
func (a *requestAPI) PatchUserSpaceMembership(ctx context.Context, membership UserSpaceMembership) error {
	us := &dwpkv1alpha1.UserSpace{}
	us.Name = membership.Name
	body, err := json.Marshal(membershipPatch{Spec: membershipPatchSpec{
		Role:     membership.Role,
		Disabled: membership.Disabled,
	}})
	if err != nil {
		return fmt.Errorf("build membership patch for %s: %w", membership.Name, err)
	}
	// A merge patch replaces the whole projects array, which is what a
	// membership edit means: the form submits the complete set.
	if err := a.workspaceClient.Patch(ctx, us, ctrlclient.RawPatch(types.MergePatchType, body)); err != nil {
		return fmt.Errorf("patch UserSpace %s membership: %w", membership.Name, err)
	}
	return nil
}

// quotaPatch mirrors membershipPatch: typed structs marshalled
// by encoding/json, so adding a field cannot silently produce malformed JSON.
type quotaPatch struct {
	Spec quotaPatchSpec `json:"spec"`
}

type quotaPatchSpec struct {
	Quota dwpkv1alpha1.UserSpaceQuota `json:"quota"`
}

func (a *requestAPI) CreateUserSpace(ctx context.Context, userSpace *dwpkv1alpha1.UserSpace) error {
	if err := a.workspaceClient.Create(ctx, userSpace); err != nil {
		return fmt.Errorf("create UserSpace %s: %w", userSpace.Name, err)
	}
	return nil
}

// DeleteUserSpace removes the tenant boundary. Everything the UserSpace owns -
// the namespace and every workspace and volume in it - is garbage-collected
// with it, so this destroys the user's data as well as their access.
func (a *requestAPI) DeleteUserSpace(ctx context.Context, name string) error {
	us := &dwpkv1alpha1.UserSpace{}
	us.Name = name
	if err := a.workspaceClient.Delete(ctx, us); err != nil {
		return fmt.Errorf("delete UserSpace %s: %w", name, err)
	}
	return nil
}

func (a *requestAPI) PatchUserSpaceQuota(ctx context.Context, name string, quota dwpkv1alpha1.UserSpaceQuota) error {
	us := &dwpkv1alpha1.UserSpace{}
	us.Name = name
	body, err := json.Marshal(quotaPatch{Spec: quotaPatchSpec{Quota: quota}})
	if err != nil {
		return fmt.Errorf("build quota patch for %s: %w", name, err)
	}
	if err := a.workspaceClient.Patch(ctx, us, ctrlclient.RawPatch(types.MergePatchType, body)); err != nil {
		return fmt.Errorf("patch UserSpace %s quota: %w", name, err)
	}
	return nil
}

func (a *requestAPI) ListWorkspaces(ctx context.Context, namespace string) ([]dwpkv1alpha1.Workspace, error) {
	var list dwpkv1alpha1.WorkspaceList
	options := []ctrlclient.ListOption{}
	if namespace != "" {
		options = append(options, ctrlclient.InNamespace(namespace))
	}
	if err := a.workspaceClient.List(ctx, &list, options...); err != nil {
		return nil, fmt.Errorf("list Workspaces: %w", err)
	}
	return list.Items, nil
}

// WorkspaceLogs tails the workspace container with the caller's own token, so
// a reader who may not get pods/log in that namespace is refused by the API
// server rather than by a check reimplemented here (SPEC §8.1).
func (a *requestAPI) WorkspaceLogs(ctx context.Context, namespace, pod string, tailLines int64) (io.ReadCloser, error) {
	return a.kubeClient.CoreV1().Pods(namespace).GetLogs(pod, &corev1.PodLogOptions{
		Container: workspacepkg.ContainerName,
		TailLines: &tailLines,
	}).Stream(ctx)
}

func (a *requestAPI) OpenTerminal(ctx context.Context, req TerminalStreamRequest) error {
	server := gateway.NewServer(a.workspaceClient, a.kubeClient, a.restConfig, nil)
	return server.OpenTerminal(ctx, gateway.TerminalRequest{
		WorkspaceKey: types.NamespacedName{Namespace: req.Namespace, Name: req.Name},
		Term:         req.Term,
		Stdin:        req.Stdin,
		Stdout:       req.Stdout,
		Stderr:       req.Stderr,
		Sizes:        req.Sizes,
	})
}

// authorizedKeys turns one optional key into the list form, dropping a blank
// rather than carrying it as an empty entry.
func authorizedKeys(sshKey string) []string {
	if strings.TrimSpace(sshKey) == "" {
		return nil
	}
	return []string{sshKey}
}

// WorkspaceDraft is a workspace as a form describes it, before it is an object.
//
// A struct rather than eight positional arguments: the form grew from four
// fields to a dozen this round, and a call with eight strings in it is one
// transposition away from creating somebody's workspace with the storage in the
// name field.
type WorkspaceDraft struct {
	Name      string
	Namespace string
	Image     string
	SSHKey    string
	Resources ResourceValues
	Env       []corev1.EnvVar
}

// buildWorkspace renders a draft as the object that will be POSTed.
//
// It is also what the YAML preview renders, deliberately: a preview built by a
// second code path is a preview that can be wrong, and the whole point of
// showing it is that it is what will happen.
func buildWorkspace(draft WorkspaceDraft) (*dwpkv1alpha1.Workspace, error) {
	storage, err := draft.Resources.StorageQuantity()
	if err != nil {
		return nil, err
	}
	requirements, err := draft.Resources.Requirements()
	if err != nil {
		return nil, err
	}
	return &dwpkv1alpha1.Workspace{
		TypeMeta: metav1.TypeMeta{APIVersion: dwpkv1alpha1.GroupVersion.String(), Kind: "Workspace"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      draft.Name,
			Namespace: draft.Namespace,
		},
		Spec: dwpkv1alpha1.WorkspaceSpec{
			ImageRef:  dwpkv1alpha1.WorkspaceImageReference{Name: draft.Image},
			Resources: requirements,
			Storage:   &storage,
			// Left nil when no key was given, never []string{""}. A one-element
			// list holding an empty string is not empty: it would stop the
			// mutating webhook defaulting the owner's keys in, and then fail
			// validation for containing a blank key.
			SSHAuthorizedKeys: authorizedKeys(draft.SSHKey),
			Env:               draft.Env,
			Running:           true,
			Observability: dwpkv1alpha1.WorkspaceObservability{
				LogsEnabled:    true,
				MetricsEnabled: true,
			},
		},
	}, nil
}

// PlatformConfigUpdate is the editable platform settings. Logo is nil for "do
// not touch it", which is different from ClearLogo - a form that submits no
// file must not wipe the existing one.
type PlatformConfigUpdate struct {
	DisplayName     string
	DefaultTheme    string
	SupportEmail    string
	GPUResourceName string
	Logo            *dwpkv1alpha1.PlatformLogo
	ClearLogo       bool
}

func (a *requestAPI) GetPlatformConfig(ctx context.Context) (*dwpkv1alpha1.PlatformConfig, error) {
	config := &dwpkv1alpha1.PlatformConfig{}
	key := ctrlclient.ObjectKey{Name: dwpkv1alpha1.PlatformConfigName}
	if err := a.workspaceClient.Get(ctx, key, config); err != nil {
		return nil, fmt.Errorf("get PlatformConfig: %w", err)
	}
	return config, nil
}

// PatchPlatformConfig writes the settings, creating the singleton if this is
// the first time anyone has changed anything.
//
// Create-or-patch rather than requiring an install-time object: a fresh cluster
// has no PlatformConfig, and every screen already copes with that by falling
// back to the defaults. Shipping an empty one just to have something to patch
// would be an object whose only purpose is to exist.
func (a *requestAPI) PatchPlatformConfig(ctx context.Context, update PlatformConfigUpdate) error {
	spec := map[string]any{
		"displayName":     update.DisplayName,
		"defaultTheme":    update.DefaultTheme,
		"supportEmail":    update.SupportEmail,
		"gpuResourceName": update.GPUResourceName,
	}
	switch {
	case update.ClearLogo:
		spec["logo"] = nil
	case update.Logo != nil:
		spec["logo"] = update.Logo
	}

	body, err := json.Marshal(map[string]any{specKey: spec})
	if err != nil {
		return fmt.Errorf("build PlatformConfig patch: %w", err)
	}

	config := &dwpkv1alpha1.PlatformConfig{
		ObjectMeta: metav1.ObjectMeta{Name: dwpkv1alpha1.PlatformConfigName},
	}
	err = a.workspaceClient.Patch(ctx, config, ctrlclient.RawPatch(types.MergePatchType, body))
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("patch PlatformConfig: %w", err)
	}

	fresh := &dwpkv1alpha1.PlatformConfig{
		TypeMeta:   metav1.TypeMeta{APIVersion: dwpkv1alpha1.GroupVersion.String(), Kind: "PlatformConfig"},
		ObjectMeta: metav1.ObjectMeta{Name: dwpkv1alpha1.PlatformConfigName},
		Spec: dwpkv1alpha1.PlatformConfigSpec{
			DisplayName:     update.DisplayName,
			DefaultTheme:    update.DefaultTheme,
			SupportEmail:    update.SupportEmail,
			GPUResourceName: update.GPUResourceName,
			Logo:            update.Logo,
		},
	}
	if err := a.workspaceClient.Create(ctx, fresh); err != nil {
		return fmt.Errorf("create PlatformConfig: %w", err)
	}
	return nil
}

// ListUserSpaces reads every UserSpace the caller may see.
//
// A cluster-scoped LIST, so RBAC answers it whole: an administrator gets
// everyone, and anybody else gets a 403 rather than a filtered list. That is
// why the screens that call this are the administration screens.
func (a *requestAPI) ListUserSpaces(ctx context.Context) ([]dwpkv1alpha1.UserSpace, error) {
	var list dwpkv1alpha1.UserSpaceList
	if err := a.workspaceClient.List(ctx, &list); err != nil {
		return nil, fmt.Errorf("list UserSpaces: %w", err)
	}
	return list.Items, nil
}

// GetUserSpace reads one by name. Every user holds get on their own by
// resourceName, which is why this works for people who cannot list.
func (a *requestAPI) GetUserSpace(ctx context.Context, name string) (*dwpkv1alpha1.UserSpace, error) {
	userSpace := &dwpkv1alpha1.UserSpace{}
	if err := a.workspaceClient.Get(ctx, ctrlclient.ObjectKey{Name: name}, userSpace); err != nil {
		return nil, fmt.Errorf("get UserSpace %s: %w", name, err)
	}
	return userSpace, nil
}

// ListEvents reads a namespace's events with the caller's own token. The
// per-user Role grants events, so this is one more ordinary read.
func (a *requestAPI) ListEvents(ctx context.Context, namespace string) ([]corev1.Event, error) {
	var list corev1.EventList
	if err := a.workspaceClient.List(ctx, &list, ctrlclient.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("list events in %s: %w", namespace, err)
	}
	return list.Items, nil
}
