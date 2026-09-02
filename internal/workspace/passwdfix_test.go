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
	"testing"
)

// A nominal, non-root uid gets an init container that gives it a passwd
// entry, and the main container mounts the result over its own /etc/passwd
// and /etc/group - without this, ssh (and therefore `git clone` over ssh)
// fails with "No user exists for uid 1000" the moment it calls getpwuid.
func TestNominalUIDGetsAPasswdFixInitContainer(t *testing.T) {
	t.Parallel()
	img := testImage()
	img.Spec.RunAsUser = 1000

	pod := podSpec(testWorkspace(), img, nil)

	if len(pod.InitContainers) != 1 {
		t.Fatalf("initContainers = %d, want 1", len(pod.InitContainers))
	}
	init := pod.InitContainers[0]
	if init.SecurityContext == nil || init.SecurityContext.RunAsUser == nil || *init.SecurityContext.RunAsUser != 0 {
		t.Error("the fix-passwd init container does not run as root, so it cannot write /etc/passwd")
	}
	if init.SecurityContext.Privileged == nil || *init.SecurityContext.Privileged {
		t.Error("the fix-passwd init container must never be privileged")
	}

	container := pod.Containers[0]
	var sawPasswd, sawGroup bool
	for _, m := range container.VolumeMounts {
		if m.MountPath == "/etc/passwd" {
			sawPasswd = true
			if m.SubPath != "passwd" || !m.ReadOnly {
				t.Errorf("/etc/passwd mount = %+v, want subPath passwd, read-only", m)
			}
		}
		if m.MountPath == "/etc/group" {
			sawGroup = true
		}
	}
	if !sawPasswd || !sawGroup {
		t.Errorf("mounts = %v, want /etc/passwd and /etc/group covered", container.VolumeMounts)
	}
}

// uid 0 (allowRoot) needs none of this: root already has a passwd entry in
// essentially every base image, and adding a root-running init container to
// every root-allowed workspace would be pure overhead.
func TestRootWorkspaceGetsNoPasswdFix(t *testing.T) {
	t.Parallel()
	img := testImage()
	img.Spec.AllowRoot = true

	pod := podSpec(testWorkspace(), img, nil)

	if len(pod.InitContainers) != 0 {
		t.Errorf("initContainers = %v, want none for a root workspace", pod.InitContainers)
	}
	for _, m := range pod.Containers[0].VolumeMounts {
		if m.MountPath == "/etc/passwd" || m.MountPath == "/etc/group" {
			t.Errorf("mount %v present on a root workspace, want none", m)
		}
	}
}
