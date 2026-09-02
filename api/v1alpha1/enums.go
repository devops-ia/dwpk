/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

// Status values and networkPolicy values are part of the contract (§4.4): the
// controller, the gateway and the UI compare against these symbols rather than
// against three copies of a string literal that drift silently.

// UserSpace.status.state values (§4.2).
const (
	UserSpaceStatePending = "Pending"
	UserSpaceStateReady   = "Ready"
	UserSpaceStateFailed  = "Failed"
)

// Workspace.status.state values (§4.3).
const (
	WorkspaceStatePending   = "Pending"
	WorkspaceStateStarting  = "Starting"
	WorkspaceStateRunning   = "Running"
	WorkspaceStateSuspended = "Suspended"
	WorkspaceStateFailed    = "Failed"
)

// UserSpace.spec.role values, in increasing order of authority.
//
// There were three. "manager" reached the workspaces of everyone sharing a
// project, and it went when Projects did: without projects there is nothing for
// it to be scoped by, and an unscoped manager is an administrator.
const (
	UserSpaceRoleUser  = "user"
	UserSpaceRoleAdmin = "admin"
)

// UserSpace.spec.networkPolicy values (§4.2).
const (
	NetworkPolicyIsolated      = "Isolated"
	NetworkPolicyClusterEgress = "ClusterEgress"
)

// WorkspaceLabel is set on every object a Workspace owns and carries the
// workspace name. The gateway uses it to scope its exec target.
const WorkspaceLabel = "dwpk.devops-ia.io/workspace"

// RequesterAnnotation carries the Kubernetes username that created the
// Workspace. The mutating webhook stamps it from request.userInfo, which is
// visible at admission and nowhere else (§7.3). An annotation rather than a
// spec field: nobody is allowed to declare it, so it is not desired state.
const RequesterAnnotation = "dwpk.devops-ia.io/requester"

// ImageRegistryLabel is set on every WorkspaceImage an ImageRegistry created,
// carrying the registry's name. It is what a sync's prune pass selects on,
// and what Phase 2's UI groups a registry's entries by.
const ImageRegistryLabel = "dwpk.devops-ia.io/image-registry"

// ForceSyncAnnotation, bumped to any new value, retriggers an ImageRegistry's
// sync immediately instead of waiting for its interval. A plain annotation
// write rather than a dedicated endpoint: the controller's own watch already
// delivers it.
const ForceSyncAnnotation = "dwpk.devops-ia.io/force-sync"

// PullSecretLabel marks a Secret, in the manager's own namespace, as one to
// replicate into every user namespace - the same label-select-one-namespace
// shape internal/auth already uses for local users and API tokens.
const PullSecretLabel = "dwpk.devops-ia.io/pull-secret"

// GitSSHKeysSecretName is the one Secret per user namespace holding that
// person's private SSH keys for git access to their own private
// repositories. A fixed name rather than a reference anywhere: the owner's
// own RBAC already scopes them to their own namespace, so there is nothing a
// Workspace or UserSpace field would add - the WorkspaceReconciler simply
// looks for this one name in ws.Namespace on every reconcile.
const GitSSHKeysSecretName = "dwpk-git-ssh-keys"

// GitSSHMountPath is where the git-ssh Secret is mounted in every workspace
// pod that has one - shared between internal/ui (which writes the generated
// OpenSSH config's IdentityFile paths against it) and internal/workspace
// (which mounts the volume there), so the two can never drift apart. Outside
// the home directory on purpose: the home volume is the user's own
// persistent PVC, and mounting a read-only Secret over any part of it would
// hide whatever the user already has there.
const GitSSHMountPath = "/etc/dwpk/git-ssh"

// GitSSHKeyDataPrefix marks a Secret data entry as one host's private key,
// distinguishing it from the "config" entry holding the generated OpenSSH
// client config - both live in the one GitSSHKeysSecretName Secret. A
// hostname's own character set (letters, digits, "." and "-") already fits a
// Secret data key's allowed charset, so the host name is used as-is rather
// than slugified.
const GitSSHKeyDataPrefix = "key-"

// GitSSHKeyMetaDataPrefix marks a Secret data entry as one host's key
// metadata - "<type> <fingerprint>", plain text - stored unencrypted
// alongside its ciphertext GitSSHKeyDataPrefix entry so the Profile page can
// list keys without ever decrypting one just to render a fingerprint.
const GitSSHKeyMetaDataPrefix = "meta-"

// GitSSHEncryptionKeySecretName is the one Secret, in the operator's own
// namespace rather than any user's, holding the AES-256 key that encrypts
// every GitSSHKeysSecretName entry at rest. Only the UI (to encrypt on
// upload) and the controller (to decrypt on reconcile) ever read it - never
// a workspace pod, and never a namespace owner's own RBAC.
const GitSSHEncryptionKeySecretName = "dwpk-git-ssh-encryption-key"

// GitSSHEncryptionKeySecretDataKey is the one data entry in
// GitSSHEncryptionKeySecretName - the raw 32-byte AES-256 key. Named as a
// constant because both this Go code and the Helm chart that generates the
// Secret (a separate repository) have to agree on it without sharing code.
const GitSSHEncryptionKeySecretDataKey = "key"

// GitSSHKeysRuntimeSecretName is the controller-managed Secret, one per user
// namespace, that a workspace pod actually mounts. GitSSHKeysSecretName (the
// one the UI writes with the caller's own token) holds ciphertext; this one
// holds what the controller decrypted from it, plus the same generated
// "config" entry - kubelet's Secret volume mount cannot decrypt anything
// itself, so something has to produce a plaintext-bearing object for it to
// copy verbatim, and that something is deliberately the controller alone,
// never a workspace pod. Not part of any namespace owner's RBAC: only the
// controller creates, updates or deletes it.
const GitSSHKeysRuntimeSecretName = "dwpk-git-ssh-keys-runtime"
