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
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// WorkspacePlacement constrains where a workspace pod may be scheduled.
type WorkspacePlacement struct {
	// nodeSelector restricts pods to nodes carrying these labels.
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// tolerations let pods schedule onto tainted nodes.
	// +optional
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`

	// affinity is the full scheduling expression, for what nodeSelector cannot
	// say: "prefer these nodes", "spread across zones", "not next to that pod".
	// +optional
	Affinity *corev1.Affinity `json:"affinity,omitempty"`
}

// WorkspaceImageSpec defines the desired state of WorkspaceImage.
type WorkspaceImageSpec struct {
	// displayName is the catalog title shown to users. Optional: an entry with
	// none is titled by its object name (see Title), so a catalog whose entries
	// are already named "python-3.13" need not repeat itself.
	// +optional
	DisplayName string `json:"displayName,omitempty"`

	// description is the catalog blurb.
	// +optional
	Description string `json:"description,omitempty"`

	// icon is a URL to the catalog icon.
	// +optional
	Icon string `json:"icon,omitempty"`

	// tags group catalog entries for browsing.
	// +optional
	Tags []string `json:"tags,omitempty"`

	// maintainer is who to contact about this entry.
	// +optional
	Maintainer string `json:"maintainer,omitempty"`

	// deprecated hides the entry from the catalog without deleting it.
	//
	// Read it through IsDeprecated rather than directly: deprecateAt below can
	// make an entry deprecated without this field ever being written.
	// +kubebuilder:default=false
	// +optional
	Deprecated bool `json:"deprecated,omitempty"`

	// deprecateAt schedules the deprecation above for a date.
	//
	// Before it, the catalog card warns how long is left and the entry works
	// normally. From it, the entry behaves exactly as deprecated: true. No
	// controller watches the clock - IsDeprecated compares the date when asked,
	// so there is no reconcile to miss and no drift between the two fields.
	// +optional
	DeprecateAt *metav1.Time `json:"deprecateAt,omitempty"`

	// image is the container image reference the workspace pod runs.
	//
	// MinLength because "required" alone still admits the empty string, and a
	// catalog entry with no image is one every Workspace pointing at it fails
	// on. The edit path patches this field, so an empty value has to be
	// impossible at the API server rather than merely unlikely in the UI.
	// +kubebuilder:validation:MinLength=1
	// +required
	Image string `json:"image"`

	// imagePullPolicy is the pull policy for that image.
	// +kubebuilder:validation:Enum=Always;IfNotPresent;Never
	// +kubebuilder:default=IfNotPresent
	// +optional
	ImagePullPolicy corev1.PullPolicy `json:"imagePullPolicy,omitempty"`

	// shell is the login shell the gateway execs.
	// +kubebuilder:default=/bin/bash
	// +optional
	Shell string `json:"shell,omitempty"`

	// homePath is where the workspace PVC is mounted.
	// +kubebuilder:default=/home/dev
	// +optional
	HomePath string `json:"homePath,omitempty"`

	// runAsUser is the uid the workspace container runs as.
	// +kubebuilder:default=1000
	// +optional
	RunAsUser int64 `json:"runAsUser,omitempty"`

	// allowRoot runs the workspace container as uid 0 and permits privilege
	// escalation, so sudo, apt and anything else needing root work inside it.
	// It overrides runAsUser.
	//
	// This is root in the container, never on the node: privileged stays false
	// and no capabilities are added, in either state. An image with this set can
	// still not read another pod's filesystem or reach the kubelet's credentials.
	//
	// Only an administrator may set it, which is plain RBAC: writing a catalog
	// entry at all is an administrator's verb.
	// +kubebuilder:default=false
	// +optional
	AllowRoot bool `json:"allowRoot,omitempty"`

	// command is what the container runs. Nothing in a workspace listens (§6.2 -
	// the gateway bridges over pods/exec), so the default just idles and keeps
	// the pod alive. Set it explicitly for an image whose own entrypoint blocks.
	// +kubebuilder:default={"sleep","infinity"}
	// +optional
	Command []string `json:"command,omitempty"`

	// placement constrains scheduling of workspace pods using this image.
	// +optional
	Placement *WorkspacePlacement `json:"placement,omitempty"`

	// imagePullSecretRef names a pull Secret for a private image. dwpk
	// replicates the Secret of this name from the manager's own namespace into
	// every user namespace (§4.6), so this field only ever needs to carry a
	// name, never credentials of its own.
	// +optional
	ImagePullSecretRef *corev1.LocalObjectReference `json:"imagePullSecretRef,omitempty"`
}

// WorkspaceImageStatus defines the observed state of WorkspaceImage.
type WorkspaceImageStatus struct {
	// conditions represent the current state of the WorkspaceImage resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Display Name",type=string,JSONPath=`.spec.displayName`
// +kubebuilder:printcolumn:name="Image",type=string,JSONPath=`.spec.image`
// +kubebuilder:printcolumn:name="Deprecated",type=boolean,JSONPath=`.spec.deprecated`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// WorkspaceImage is the Schema for the workspaceimages API
type WorkspaceImage struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of WorkspaceImage
	// +required
	Spec WorkspaceImageSpec `json:"spec"`

	// status defines the observed state of WorkspaceImage
	// +optional
	Status WorkspaceImageStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// WorkspaceImageList contains a list of WorkspaceImage
type WorkspaceImageList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []WorkspaceImage `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &WorkspaceImage{}, &WorkspaceImageList{})
		return nil
	})
}

// Title is what to call this catalog entry on screen: its display name, or its
// object name when it has none.
//
// The fallback is computed rather than stored. Writing metadata.name into
// spec.displayName at creation would freeze the label against a later rename,
// and would make "has no display name" indistinguishable from "is called the
// same as its name".
func (i *WorkspaceImage) Title() string {
	if i.Spec.DisplayName != "" {
		return i.Spec.DisplayName
	}
	return i.Name
}

// IsDeprecated folds the flag and the scheduled date into one answer, so no
// caller has to remember that there are two ways to be deprecated.
func (i *WorkspaceImage) IsDeprecated(now time.Time) bool {
	if i.Spec.Deprecated {
		return true
	}
	return i.Spec.DeprecateAt != nil && !now.Before(i.Spec.DeprecateAt.Time)
}

// DeprecationNotice is the warning an entry's card carries while its scheduled
// date is still ahead: "Deprecates today", "tomorrow", or "in 12 days".
//
// The second return is what callers gate on, NOT the day count. Gating on
// "days > 0" is what made the warning vanish in the final twenty-four hours -
// the integer division floors to zero on the last day, which is the one day
// anybody needed to see it.
//
// It is empty and false once the date has passed: the entry is then simply
// deprecated, and says so with the other badge instead of counting down to a
// moment that is behind it.
func (i *WorkspaceImage) DeprecationNotice(now time.Time) (string, bool) {
	if i.Spec.Deprecated || i.Spec.DeprecateAt == nil {
		return "", false
	}
	remaining := i.Spec.DeprecateAt.Sub(now)
	if remaining <= 0 {
		return "", false
	}
	switch days := int(remaining.Hours() / 24); days {
	case 0:
		return "Deprecates today", true
	case 1:
		return "Deprecates tomorrow", true
	default:
		return fmt.Sprintf("Deprecates in %d days", days), true
	}
}
