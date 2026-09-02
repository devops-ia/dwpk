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
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	dwpkv1alpha1 "github.com/devops-ia/dwpk/api/v1alpha1"
)

// Names of the cluster-scoped objects a role grant owns. Both are derived from
// the UserSpace name so garbage collection removes them with it.
const (
	adminBindingPrefix   = "dwpk-administrator-"
	managerRolePrefix    = "dwpk-project-manager-"
	projectsResource     = "projects"
	userSpacesResource   = "userspaces"
	projectStatusResourc = "projects/status"
)

// buildAdministratorBinding grants the platform-wide admin ClusterRoles to a
// UserSpace whose role is "administrator".
//
// The subject is the session ServiceAccount, never the workspace one: an
// administrator's container must not inherit their authority.
//
// The role names are configuration rather than constants because they carry the
// Helm release prefix. The manager holds `bind` on exactly these names, which is
// what lets it create a binding to rights it does not itself hold.
func buildAdministratorBinding(us *dwpkv1alpha1.UserSpace, clusterRole string) *rbacv1.ClusterRoleBinding {
	return &rbacv1.ClusterRoleBinding{
		TypeMeta: metav1.TypeMeta{APIVersion: rbacAPIVersion, Kind: clusterRoleBindingKind},
		ObjectMeta: metav1.ObjectMeta{
			Name:   adminBindingPrefix + clusterRole + "-" + us.Name,
			Labels: map[string]string{userSpaceLabel: us.Name},
		},
		Subjects: []rbacv1.Subject{sessionSubject(us)},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     clusterRoleKind,
			Name:     clusterRole,
		},
	}
}
