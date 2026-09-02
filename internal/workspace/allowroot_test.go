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

package workspace

import (
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

// The ordinary case, and the one worth pinning: leaving the container's
// privileges unstated is not the same as stating them. Kubernetes permits
// privilege escalation by default, so the false branch has to say so out loud.
func TestOrdinaryImageForbidsPrivilegeEscalation(t *testing.T) {
	t.Parallel()
	img := testImage()

	pod := podSpec(testWorkspace(), img, nil)
	security := pod.Containers[0].SecurityContext
	if security == nil {
		t.Fatal("container has no SecurityContext; Kubernetes then permits privilege escalation")
	}
	if security.AllowPrivilegeEscalation == nil || *security.AllowPrivilegeEscalation {
		t.Error("allowPrivilegeEscalation is not explicitly false")
	}
	if *pod.SecurityContext.RunAsUser != img.Spec.RunAsUser {
		t.Errorf("runAsUser = %d, want the image's %d",
			*pod.SecurityContext.RunAsUser, img.Spec.RunAsUser)
	}
	assertDropsAllCapabilitiesAndSeccomp(t, security)
}

// allowRoot grants root in the container. The half that matters is what it does
// NOT grant: privileged is what reaches the node, and no branch may set it.
func TestAllowRootGivesUIDZeroAndNeverPrivileged(t *testing.T) {
	t.Parallel()
	img := testImage()
	img.Spec.AllowRoot = true

	pod := podSpec(testWorkspace(), img, nil)
	if got := *pod.SecurityContext.RunAsUser; got != 0 {
		t.Errorf("runAsUser = %d, want 0", got)
	}
	if got := *pod.SecurityContext.FSGroup; got != 0 {
		t.Errorf("fsGroup = %d, want 0 - the home volume must be writable by the user that owns it", got)
	}

	security := pod.Containers[0].SecurityContext
	if security.AllowPrivilegeEscalation == nil || !*security.AllowPrivilegeEscalation {
		t.Error("allowPrivilegeEscalation is not true, so sudo will not work")
	}
	if security.Privileged == nil || *security.Privileged {
		t.Error("privileged is set; allowRoot must never reach the node")
	}
	assertDropsAllCapabilitiesAndSeccomp(t, security)
}

// A pod on a shared node runs with the runtime's full default capability set
// unless something drops it - allowRoot granting uid 0 in the container is not
// a reason to also leave NET_RAW, DAC_OVERRIDE or SETUID in reach.
func assertDropsAllCapabilitiesAndSeccomp(t *testing.T, security *corev1.SecurityContext) {
	t.Helper()
	if security.Capabilities == nil || !slices.Contains(security.Capabilities.Drop, corev1.Capability("ALL")) {
		t.Errorf("capabilities.drop = %+v, want [ALL]", security.Capabilities)
	}
	if security.SeccompProfile == nil || security.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Errorf("seccompProfile = %+v, want RuntimeDefault", security.SeccompProfile)
	}
}

// allowRoot wins over runAsUser rather than being ignored beside it. An entry
// carrying both is a contradiction, and picking the weaker one would leave an
// administrator believing they granted root when they had not.
func TestAllowRootOverridesRunAsUser(t *testing.T) {
	t.Parallel()
	img := testImage()
	img.Spec.RunAsUser = 1000
	img.Spec.AllowRoot = true

	if got := workspaceUser(img); got != 0 {
		t.Errorf("workspaceUser = %d, want 0", got)
	}
}
