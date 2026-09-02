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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// PlatformConfigName is the only name this object may have.
//
// A singleton, enforced by CEL on metadata.name. "The settings" is one thing,
// and two PlatformConfigs would mean the platform has two names and the answer
// depends on which one a reader happens to list first.
const PlatformConfigName = "cluster"

// DefaultPlatformName is what the platform is called before anyone renames it.
const DefaultPlatformName = "dwpk"

// PlatformLogo is an image shown as the brand mark and the favicon.
type PlatformLogo struct {
	// contentType is the image's media type, used verbatim in the response that
	// serves it.
	// +kubebuilder:validation:Enum="image/png";"image/svg+xml";"image/jpeg";"image/webp"
	// +required
	ContentType string `json:"contentType"`

	// data is the image itself, base64 in the serialised object as any []byte
	// is.
	//
	// It lives on the object rather than in a ConfigMap so the settings are one
	// thing to read, write and back up. The size cap is what keeps that
	// reasonable: 120x120 of PNG is a few kilobytes, and the ceiling here is
	// generous enough for a retina asset while staying far below what makes an
	// etcd object awkward.
	// +kubebuilder:validation:MaxLength=131072
	// +required
	Data []byte `json:"data"`
}

// PlatformConfigSpec is what an administrator can change about the platform
// itself, as opposed to about the people and workspaces in it.
type PlatformConfigSpec struct {
	// displayName is what the platform calls itself in titles and in the
	// sidebar. Empty means "dwpk".
	// +kubebuilder:validation:MaxLength=64
	// +optional
	DisplayName string `json:"displayName,omitempty"`

	// logo is the brand mark, reused as the favicon. Unset falls back to the
	// built-in mark.
	// +optional
	Logo *PlatformLogo `json:"logo,omitempty"`

	// defaultTheme is the appearance for anyone who has not chosen one.
	//
	// It is a default, not an enforcement: a viewer's own choice still wins.
	// Taking somebody's chosen theme away from them is not a setting, it is a
	// bug they cannot fix.
	// +kubebuilder:validation:Enum=system;light;dark
	// +kubebuilder:default=system
	// +optional
	DefaultTheme string `json:"defaultTheme,omitempty"`

	// gpuResourceName is the extended resource a GPU is requested as.
	//
	// It is configuration because the name is the vendor's, not Kubernetes':
	// nvidia.com/gpu, amd.com/gpu, or a MIG slice like nvidia.com/mig-1g.5gb.
	// This is the name the GPU quota counts and the one the workspace form
	// offers; a workspace may still name a different extended resource, and
	// that one simply is not counted by the GPU quota.
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:default="nvidia.com/gpu"
	// +optional
	GPUResourceName string `json:"gpuResourceName,omitempty"`

	// supportEmail is who to contact when the platform will not let somebody in.
	//
	// It lives here rather than being looked up because the only screens that
	// need it are the ones nobody has signed in to yet. The sign-in page holds
	// no token, so it cannot list UserSpaces to find an administrator; a
	// disabled account would otherwise be told it is disabled and nothing else.
	// +kubebuilder:validation:MaxLength=254
	// +optional
	SupportEmail string `json:"supportEmail,omitempty"`
}

// PlatformConfigStatus is the observed state of PlatformConfig.
type PlatformConfigStatus struct {
	// conditions represent the current state of the PlatformConfig resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=pfc
// +kubebuilder:validation:XValidation:rule="self.metadata.name == 'cluster'",message="the platform configuration is a singleton and must be named 'cluster'"
// +kubebuilder:printcolumn:name="Name",type=string,JSONPath=`.spec.displayName`
// +kubebuilder:printcolumn:name="Theme",type=string,JSONPath=`.spec.defaultTheme`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// PlatformConfig is the Schema for the platformconfigs API.
type PlatformConfig struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of PlatformConfig
	// +required
	Spec PlatformConfigSpec `json:"spec"`

	// status defines the observed state of PlatformConfig
	// +optional
	Status PlatformConfigStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// PlatformConfigList contains a list of PlatformConfig
type PlatformConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []PlatformConfig `json:"items"`
}

// Name is what to call the platform: the configured name, or the default.
//
// A method rather than a stored default so an object that has never been
// written still answers, and so clearing the field goes back to "dwpk" instead
// of leaving the screen blank.
func (p *PlatformConfig) Name() string {
	if p == nil || p.Spec.DisplayName == "" {
		return DefaultPlatformName
	}
	return p.Spec.DisplayName
}

// Theme is the default appearance, or "system" when none is set. Nil-safe: the
// settings object is optional, and every screen reads this on every render.
func (p *PlatformConfig) Theme() string {
	if p == nil || p.Spec.DefaultTheme == "" {
		return "system"
	}
	return p.Spec.DefaultTheme
}

// Support is the address to contact about access, or "" when none is set. The
// caller omits the sentence rather than printing a blank one.
func (p *PlatformConfig) Support() string {
	if p == nil {
		return ""
	}
	return p.Spec.SupportEmail
}

// DefaultGPUResourceName is what a GPU is requested as when nothing says
// otherwise. It is the overwhelmingly common one, and the field exists so a
// cluster with different hardware is configuration rather than a fork.
const DefaultGPUResourceName = "nvidia.com/gpu"

// GPUResource is the extended resource name a GPU is requested as. Nil-safe,
// like every other reader here: the settings object is optional.
func (p *PlatformConfig) GPUResource() string {
	if p == nil || p.Spec.GPUResourceName == "" {
		return DefaultGPUResourceName
	}
	return p.Spec.GPUResourceName
}

// HasLogo reports whether there is an image to serve.
func (p *PlatformConfig) HasLogo() bool {
	return p != nil && p.Spec.Logo != nil && len(p.Spec.Logo.Data) > 0
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &PlatformConfig{}, &PlatformConfigList{})
		return nil
	})
}
