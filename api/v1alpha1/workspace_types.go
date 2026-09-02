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

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// WorkspaceImageReference names the catalog entry a Workspace runs.
type WorkspaceImageReference struct {
	// name is the name of the cluster-scoped WorkspaceImage.
	// +kubebuilder:validation:MinLength=1
	// +required
	Name string `json:"name"`
}

// WorkspaceSpec defines the desired state of Workspace.
//
// size and storage are optional here because the mutating webhook fills them
// from the referenced WorkspaceImage, which CRD defaults cannot read (SPEC §7.2).
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.storage) || (has(self.storage) && self.storage == oldSelf.storage)",message="storage is immutable; PVCs cannot shrink"
// The prefix check covers every type OpenSSH defines: ssh-rsa and ssh-ed25519,
// the three ecdsa-sha2-nistp curves, and the two sk-* security-key forms. It
// used to be startsWith('ssh-') alone, which silently rejected ECDSA and every
// hardware key.
//
// It is a shape check only - CEL cannot decode base64 or parse a key blob. The
// validating webhook does that properly with the same parser the gateway
// matches against, so this catches the obvious mistake early and the webhook
// catches the real one.
// +kubebuilder:validation:XValidation:rule="!has(self.sshAuthorizedKeys) || self.sshAuthorizedKeys.all(k, k.startsWith('ssh-') || k.startsWith('ecdsa-sha2-') || k.startsWith('sk-'))",message="each key must be an OpenSSH public key: ssh-rsa, ssh-ed25519, ecdsa-sha2-nistp256/384/521 or an sk- security key"
// +kubebuilder:validation:XValidation:rule="!self.running || (has(self.sshAuthorizedKeys) && size(self.sshAuthorizedKeys) > 0)",message="a running workspace needs at least one authorized key"
type WorkspaceSpec struct {
	// imageRef selects the catalog entry this workspace runs.
	// +required
	ImageRef WorkspaceImageReference `json:"imageRef"`

	// resources are the container requests and limits.
	//
	// Typed by the person creating the workspace rather than picked from a named
	// size. Sizes were a menu the administrator had to keep in step with what
	// people actually wanted; the quota is the real constraint, and it applies
	// whatever shape the request is. The validating webhook checks the totals
	// against the owner's quota, and the namespace ResourceQuota is the backstop.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitzero"`

	// storage is the size of the home PVC. Immutable once set: a PVC cannot
	// shrink, and growing one is a StorageClass capability we do not require.
	// +kubebuilder:default="10Gi"
	// +optional
	Storage *resource.Quantity `json:"storage,omitempty"`

	// nodeSelector, tolerations and affinity override the catalog entry's
	// placement for this one workspace.
	//
	// Override, not merge. Merged node selectors are how you get a pod that
	// schedules nowhere with nothing to read that says why: the entry asks for
	// one label, the workspace asks for another, and no node carries both. An
	// unset field here leaves the entry's value alone; a set one replaces it.
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// +optional
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`

	// +optional
	Affinity *corev1.Affinity `json:"affinity,omitempty"`

	// env and envFrom are extra environment for the workspace container.
	// envFrom is how a whole Secret or ConfigMap is pulled in at once.
	// +optional
	Env []corev1.EnvVar `json:"env,omitempty"`

	// +optional
	EnvFrom []corev1.EnvFromSource `json:"envFrom,omitempty"`

	// volumes and volumeMounts are extra storage for the workspace container.
	//
	// Restricted at admission to types that reference objects in the user's own
	// namespace, where they already have rights. hostPath mounts the node's
	// filesystem and is refused unless the catalog entry allows root, because
	// reading /var/lib/kubelet is reading every other pod's secrets.
	//
	// The "home" volume and its mount are reserved: shadowing them would detach
	// somebody's home directory from the PVC that persists it.
	// +optional
	Volumes []corev1.Volume `json:"volumes,omitempty"`

	// +optional
	VolumeMounts []corev1.VolumeMount `json:"volumeMounts,omitempty"`

	// sshAuthorizedKeys are the OpenSSH public keys allowed to connect.
	//
	// The bounds are what let the CEL rule above be costed: Kubernetes estimates
	// a validation rule's cost from the declared maximums, and refuses an
	// unbounded list of unbounded strings as unbudgetable. They earn their place
	// anyway - a public key is under 1KB and nobody has 32 machines.
	// +kubebuilder:validation:MaxItems=32
	// +kubebuilder:validation:items:MaxLength=2048
	// +optional
	SSHAuthorizedKeys []string `json:"sshAuthorizedKeys,omitempty"`

	// running is the stop/start switch. Setting it false suspends the workspace
	// by scaling its StatefulSet to zero; the PVC and its contents survive.
	//
	// No omitempty: with it, a Go client marshalling running=false emits no
	// field at all, the CRD default puts it back to true, and suspend silently
	// does nothing. A YAML author omitting the field still gets the default.
	// +kubebuilder:default=true
	// +optional
	Running bool `json:"running"`

	// idleTimeout is how long without activity before the controller sets
	// running to false.
	// +kubebuilder:default="4h"
	// +optional
	IdleTimeout metav1.Duration `json:"idleTimeout,omitempty"`

	// observability toggles per-workspace logging and metrics collection.
	// +optional
	Observability WorkspaceObservability `json:"observability,omitzero"`
}

// WorkspaceObservability toggles per-workspace logging and metrics collection
// (SPEC §4.3). Both fields default true and carry no omitempty, for the same
// reason Running does: a client marshalling false must produce an explicit
// field rather than one the CRD default silently reinstates as true.
type WorkspaceObservability struct {
	// logsEnabled toggles the log-shipping sidecar annotation on the workspace pod.
	// +kubebuilder:default=true
	// +optional
	LogsEnabled bool `json:"logsEnabled"`

	// metricsEnabled toggles the PodMonitor selector label on the workspace pod.
	// +kubebuilder:default=true
	// +optional
	MetricsEnabled bool `json:"metricsEnabled"`
}

// WorkspaceStatus defines the observed state of Workspace.
type WorkspaceStatus struct {
	// state is a coarse summary of the conditions below.
	// +kubebuilder:validation:Enum=Pending;Starting;Running;Suspended;Failed
	// +optional
	State string `json:"state,omitempty"`

	// endpoint is what the user types into ssh.
	// +optional
	Endpoint string `json:"endpoint,omitempty"`

	// podName is the workspace pod the controller currently owns.
	// +optional
	PodName string `json:"podName,omitempty"`

	// lastActivityTime is the last time the gateway saw a session on this workspace.
	// +optional
	LastActivityTime *metav1.Time `json:"lastActivityTime,omitempty"`

	// observedGeneration is the metadata.generation this status was computed from.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// conditions represent the current state of the Workspace resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Image",type=string,JSONPath=`.spec.imageRef.name`
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.status.state`
// +kubebuilder:printcolumn:name="Endpoint",type=string,JSONPath=`.status.endpoint`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Workspace is the Schema for the workspaces API
type Workspace struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of Workspace
	// +required
	Spec WorkspaceSpec `json:"spec"`

	// status defines the observed state of Workspace
	// +optional
	Status WorkspaceStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// WorkspaceList contains a list of Workspace
type WorkspaceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []Workspace `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &Workspace{}, &WorkspaceList{})
		return nil
	})
}
