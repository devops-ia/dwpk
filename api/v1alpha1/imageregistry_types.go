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
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// ImageRegistryProviderAWSECR is the only provider Phase 1 implements. The
// field is a plain string, not a Go enum type, matching every other
// enum-valued field in this package - the allowed values live in the CEL/
// kubebuilder markers, not in the type system.
const ImageRegistryProviderAWSECR = "aws-ecr"

// TagSelectorModeLatest and TagSelectorModePattern are RegistrySync.Tags.Mode
// values.
const (
	TagSelectorModeLatest  = "latest"
	TagSelectorModePattern = "pattern"
)

// DefaultSyncIntervalSeconds is how often a registry is polled when
// spec.sync.intervalSeconds is left unset.
const DefaultSyncIntervalSeconds = 900

// AWSRegistry is the AWS ECR-specific half of an ImageRegistry. Only present
// when spec.provider is "aws-ecr" (enforced by CEL below).
type AWSRegistry struct {
	// region is the AWS region the registry lives in.
	// +kubebuilder:validation:MaxLength=32
	// +required
	Region string `json:"region"`

	// registryId is the AWS account ID that owns the registry. Empty means the
	// account the resolved credentials belong to - which is what IRSA, EKS Pod
	// Identity and an instance profile all resolve to without this ever being
	// set. It only needs a value for a registry in a different account.
	// +kubebuilder:validation:MaxLength=12
	// +optional
	RegistryID string `json:"registryId,omitempty"`

	// roleArn is assumed via STS before listing, for a registry in another
	// account reached through a cross-account role rather than a same-account
	// identity. Empty uses the caller's own resolved credentials directly.
	// +kubebuilder:validation:MaxLength=2048
	// +optional
	RoleARN string `json:"roleArn,omitempty"`
}

// TagSelector picks which tag(s) of a repository become catalog entries.
// Kubernetes refuses two WorkspaceImages with the same name, so a repository
// with several matching tags needs one entry per tag - Limit caps how many.
type TagSelector struct {
	// mode picks the newest tag ("latest", by push time) or every tag matching
	// patterns.
	// +kubebuilder:validation:Enum=latest;pattern
	// +kubebuilder:default=latest
	// +optional
	Mode string `json:"mode,omitempty"`

	// patterns are RE2 expressions matched against a tag name. Used only when
	// mode is "pattern"; ignored otherwise.
	// +optional
	Patterns []string `json:"patterns,omitempty"`

	// limit caps how many tags per repository become catalog entries, newest
	// first. A repository with no requested cap could otherwise turn one
	// pattern match into hundreds of entries.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=1
	// +optional
	Limit int32 `json:"limit,omitempty"`
}

// RegistrySync configures how and how often a registry is polled.
type RegistrySync struct {
	// intervalSeconds is how often the registry is re-listed. Requeue-based,
	// not a fixed background tick - see the controller's Reconcile for why a
	// timer is the right tool here.
	// +kubebuilder:validation:Minimum=60
	// +kubebuilder:default=900
	// +optional
	IntervalSeconds int32 `json:"intervalSeconds,omitempty"`

	// include is a list of RE2 patterns matched against a repository name. A
	// repository matching none of them is skipped. Empty means every
	// repository is a candidate.
	// +optional
	Include []string `json:"include,omitempty"`

	// exclude is matched the same way as include, and wins when both match -
	// an explicit exclusion is a stronger statement than a broad include.
	// +optional
	Exclude []string `json:"exclude,omitempty"`

	// tags picks which tag(s) of a matching repository are synced.
	// +optional
	Tags TagSelector `json:"tags,omitzero"`

	// prune deletes a WorkspaceImage this registry previously created once its
	// remote image is gone. Off by default: a registry that is briefly
	// unreachable or briefly returns a short list should not delete a catalog
	// entry somebody may be using.
	// +kubebuilder:default=false
	// +optional
	Prune bool `json:"prune,omitempty"`
}

// ImageRegistrySpec configures one external container registry to sync into
// the catalog (§4.6). Several ImageRegistry objects, including several of the
// same provider, may exist side by side - each syncs and prunes only the
// WorkspaceImages it owns.
type ImageRegistrySpec struct {
	// provider selects which registry API this object talks to. "aws-ecr" is
	// the only value Phase 1 implements; adding a second provider is a new
	// value here plus a new registry.Provider implementation, not a change to
	// this type.
	// +kubebuilder:validation:Enum=aws-ecr
	// +required
	Provider string `json:"provider"`

	// aws configures an AWS ECR registry. Required when provider is "aws-ecr".
	// +optional
	AWS *AWSRegistry `json:"aws,omitempty"`

	// sync configures how the registry is polled and which images are kept.
	// +optional
	Sync RegistrySync `json:"sync,omitzero"`

	// imagePullSecretRef names a Secret, in the manager's own namespace, that
	// dwpk replicates into every user namespace so a private image can
	// actually be pulled. Every WorkspaceImage this registry creates carries
	// the same reference.
	// +optional
	ImagePullSecretRef *corev1.LocalObjectReference `json:"imagePullSecretRef,omitempty"`
}

// ImageRegistryStatus is the observed state of an ImageRegistry.
type ImageRegistryStatus struct {
	// conditions represent the current state of the ImageRegistry resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// observedGeneration is the metadata.generation this status was computed
	// from.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// lastSyncTime is when the registry was last successfully listed.
	// +optional
	LastSyncTime *metav1.Time `json:"lastSyncTime,omitempty"`

	// images is how many WorkspaceImages this registry currently owns.
	// +optional
	Images int32 `json:"images,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:validation:XValidation:rule="self.spec.provider != 'aws-ecr' || has(self.spec.aws)",message="spec.aws is required when spec.provider is aws-ecr"
// +kubebuilder:printcolumn:name="Provider",type=string,JSONPath=`.spec.provider`
// +kubebuilder:printcolumn:name="Images",type=integer,JSONPath=`.status.images`
// +kubebuilder:printcolumn:name="Last Sync",type=date,JSONPath=`.status.lastSyncTime`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// ImageRegistry is the Schema for the imageregistries API.
type ImageRegistry struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of ImageRegistry
	// +required
	Spec ImageRegistrySpec `json:"spec"`

	// status defines the observed state of ImageRegistry
	// +optional
	Status ImageRegistryStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// ImageRegistryList contains a list of ImageRegistry
type ImageRegistryList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []ImageRegistry `json:"items"`
}

// SyncInterval is how often to re-list the registry: the configured value, or
// DefaultSyncIntervalSeconds when unset - the CRD default covers a normal
// create, but a zero-value Go struct built directly (as tests do) has no
// defaulting webhook to fall back on.
func (r *ImageRegistry) SyncInterval() time.Duration {
	seconds := r.Spec.Sync.IntervalSeconds
	if seconds <= 0 {
		seconds = DefaultSyncIntervalSeconds
	}
	return time.Duration(seconds) * time.Second
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &ImageRegistry{}, &ImageRegistryList{})
		return nil
	})
}
