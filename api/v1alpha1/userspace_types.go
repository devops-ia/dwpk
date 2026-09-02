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
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// UserSpaceQuota is the budget for one user's namespace. The floor is always
// zero - there is no configurable minimum, only the maximum below.
type UserSpaceQuota struct {
	// cpu is the total CPU the user's workspaces may request.
	// +required
	CPU resource.Quantity `json:"cpu"`

	// memory is the total memory the user's workspaces may request.
	// +required
	Memory resource.Quantity `json:"memory"`

	// storage is the total PVC capacity the user may claim.
	// +required
	Storage resource.Quantity `json:"storage"`

	// gpu is how many GPUs the user's running workspaces may hold at once.
	//
	// Counted against the platform's configured GPU resource name
	// (PlatformConfig.spec.gpuResourceName, nvidia.com/gpu by default). A
	// workspace naming some other extended resource is not counted here - there
	// is no way to know what an arbitrary device means.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default=0
	// +optional
	GPU int32 `json:"gpu,omitempty"`

	// workspaces is how many Workspaces the user may have RUNNING at once.
	//
	// Running, not existing: a stopped workspace is a directory, not a machine.
	// Stopped ones are unbounded in number and bounded in size, because their
	// PVCs still count against storage above.
	//
	// This is why there is no count/workspaces entry in the namespace
	// ResourceQuota any more. Kubernetes counts objects, not running ones, so
	// the two could not mean the same thing - and a hard limit that disagrees
	// with the message the user is shown is worse than no hard limit.
	// +kubebuilder:validation:Minimum=0
	// +required
	Workspaces int32 `json:"workspaces"`
}

// UserSpaceSpec defines the desired state of UserSpace.
type UserSpaceSpec struct {
	// owner is the Kubernetes username of the person this space belongs to.
	// It is immutable: the namespace and RoleBinding are built from it, and
	// rebinding them to a different person is a new UserSpace, not an edit.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="owner is immutable"
	// +required
	Owner string `json:"owner"`

	// username is what this person types to sign in. It defaults to
	// metadata.name and exists so a login need not equal the object name or the
	// RBAC subject: "a.moreno" can own "alejandra.moreno@example.com" and live
	// in a UserSpace called "alejandra".
	//
	// Unique across UserSpaces, enforced at admission - two people with one
	// login is an authentication bug, not a naming preference.
	// +kubebuilder:validation:MaxLength=63
	// +optional
	Username string `json:"username,omitempty"`

	// email is where to reach this person. It is deliberately not owner: an
	// identity provider's subject is often not a mailbox, and forcing them to
	// be equal made every UserSpace claim an address that might not exist.
	// Nothing authenticates against it.
	// +optional
	Email string `json:"email,omitempty"`

	// namespace is where this user's workspaces are created. Unset means
	// "dwpk-" + metadata.name, which is what every UserSpace got before this
	// field existed.
	//
	// Immutable: the namespace holds the person's workspaces and home volumes,
	// so changing it would abandon them rather than move them.
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="namespace is immutable"
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// sshAuthorizedKeys are this person's default public keys. A Workspace
	// created without any of its own inherits them at admission.
	//
	// Inherited once, not kept in sync: adding a key here does not rewrite
	// existing workspaces, and removing one does not revoke access to a machine
	// somebody may be working on. Editing a Workspace's own list is how you
	// change what a running workspace trusts.
	//
	// Every entry must parse as an OpenSSH authorized_keys line, which is
	// checked at admission - a key that only fails when the gateway cannot match
	// it fails at the worst possible moment.
	// +kubebuilder:validation:MaxItems=32
	// +kubebuilder:validation:items:MaxLength=2048
	// +listType=set
	// +optional
	SSHAuthorizedKeys []string `json:"sshAuthorizedKeys,omitempty"`

	// quota is the budget applied to the user's namespace.
	// +required
	Quota UserSpaceQuota `json:"quota"`

	// networkPolicy selects the egress posture of the user's namespace.
	// +kubebuilder:validation:Enum=Isolated;ClusterEgress
	// +required
	NetworkPolicy string `json:"networkPolicy"`

	// role is the user's authority:
	//
	//   user  their own workspaces, in their own namespace
	//   admin the whole platform
	//
	// Setting "admin" grants cluster-wide rights, so admission restricts it to
	// requesters who already hold them (§7.4). Without that check anyone able
	// to edit a UserSpace could promote themselves.
	// +kubebuilder:validation:Enum=user;admin
	// +kubebuilder:default=user
	// +optional
	Role string `json:"role,omitempty"`

	// disabled blocks the owner from logging in without deleting their
	// UserSpace. Their namespace, workspaces and volumes are left intact, so
	// this is how an admin suspends someone - leaving, going on
	// extended leave, an account under investigation - without destroying
	// work that may still be needed.
	// +kubebuilder:default=false
	// +optional
	Disabled bool `json:"disabled,omitempty"`

	// onboardingCompletedAt records when this person finished the first-login
	// walkthrough (SSH key, catalog, first workspace). Unset means the UI
	// still redirects them to it on every login; once set, the redirect never
	// fires again for them. Self-service - the person stamps it themselves by
	// finishing the wizard - and monotonic: nothing unsets it once written.
	// +optional
	OnboardingCompletedAt *metav1.Time `json:"onboardingCompletedAt,omitempty"`
}

// UserSpaceStatus defines the observed state of UserSpace.
type UserSpaceStatus struct {
	// namespace is the namespace the controller reconciled for this user.
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// state is a coarse summary of the conditions below.
	// +optional
	State string `json:"state,omitempty"`

	// observedGeneration is the metadata.generation this status was computed from.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// conditions represent the current state of the UserSpace resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Owner",type=string,JSONPath=`.spec.owner`
// +kubebuilder:printcolumn:name="Namespace",type=string,JSONPath=`.status.namespace`
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.status.state`
// +kubebuilder:printcolumn:name="Workspaces",type=integer,JSONPath=`.spec.quota.workspaces`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// UserSpace is the Schema for the userspaces API
type UserSpace struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of UserSpace
	// +required
	Spec UserSpaceSpec `json:"spec"`

	// status defines the observed state of UserSpace
	// +optional
	Status UserSpaceStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// UserSpaceList contains a list of UserSpace
type UserSpaceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []UserSpace `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &UserSpace{}, &UserSpaceList{})
		return nil
	})
}
