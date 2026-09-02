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

package controller

import (
	"regexp"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	dwpkv1alpha1 "github.com/devops-ia/dwpk/api/v1alpha1"
	"github.com/devops-ia/dwpk/internal/registry"
)

// buildSyncedWorkspaceImage is the pure desired-state builder for one synced
// catalog entry: a RemoteImage plus the ImageRegistry that produced it, in,
// a WorkspaceImage out.
func buildSyncedWorkspaceImage(reg *dwpkv1alpha1.ImageRegistry, remote registry.RemoteImage) *dwpkv1alpha1.WorkspaceImage {
	return &dwpkv1alpha1.WorkspaceImage{
		TypeMeta: metav1.TypeMeta{APIVersion: dwpkv1alpha1.SchemeGroupVersion.String(), Kind: "WorkspaceImage"},
		ObjectMeta: metav1.ObjectMeta{
			Name:   syncedImageName(reg.Name, remote),
			Labels: map[string]string{dwpkv1alpha1.ImageRegistryLabel: reg.Name},
		},
		Spec: dwpkv1alpha1.WorkspaceImageSpec{
			DisplayName:        remote.Repository + ":" + remote.Tag,
			Image:              remote.Reference,
			ImagePullSecretRef: reg.Spec.ImagePullSecretRef,
		},
	}
}

var nonDNSLabelChars = regexp.MustCompile(`[^a-z0-9-]+`)

// syncedImageName is registry-prefixed so two ImageRegistrys can each sync a
// repository of the same name without fighting over one WorkspaceImage -
// SetControllerReference refuses a second owner outright, so a collision
// here would be a hard error, not silently-wrong data.
func syncedImageName(registryName string, remote registry.RemoteImage) string {
	name := registryName + "-" + remote.Repository + "-" + remote.Tag
	name = strings.ToLower(name)
	name = nonDNSLabelChars.ReplaceAllString(name, "-")
	name = strings.Trim(name, "-")
	// DNS-1123 subdomain names top out at 253, but a name this long is already
	// unreadable; keep it well inside the limit rather than exactly at it.
	const maxLen = 200
	if len(name) > maxLen {
		name = strings.Trim(name[:maxLen], "-")
	}
	return name
}
