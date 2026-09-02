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
	"strconv"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	dwpkv1alpha1 "github.com/devops-ia/dwpk/api/v1alpha1"
)

func testImage() *dwpkv1alpha1.WorkspaceImage {
	return &dwpkv1alpha1.WorkspaceImage{
		ObjectMeta: metav1.ObjectMeta{Name: "python-3.12"},
		Spec: dwpkv1alpha1.WorkspaceImageSpec{
			Image:           "example.com/python:3.12",
			ImagePullPolicy: corev1.PullIfNotPresent,
			HomePath:        "/home/dev",
			RunAsUser:       1000,
			Command:         []string{"sleep", "infinity"}, // as the CRD default writes it

			Placement: &dwpkv1alpha1.WorkspacePlacement{
				NodeSelector: map[string]string{"workload": "worker"},
				Tolerations:  []corev1.Toleration{{Key: "gpu", Operator: corev1.TolerationOpExists}},
			},
		},
	}
}

func testWorkspace() *dwpkv1alpha1.Workspace {
	// storage is set because the CRD defaults it by the time a reconciler builds
	// anything.
	storage := resource.MustParse("30Gi")
	return &dwpkv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: "dev", Namespace: "dwpk-alice"},
		Spec: dwpkv1alpha1.WorkspaceSpec{
			ImageRef: dwpkv1alpha1.WorkspaceImageReference{Name: "python-3.12"},
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")},
			},
			Storage: &storage,
			Running: true,
		},
	}
}

func TestBuildStatefulSet(t *testing.T) {
	ws, img := testWorkspace(), testImage()
	storage := resource.MustParse("5Gi")
	ws.Spec.Storage = &storage

	sts := BuildStatefulSet(ws, img, nil)

	if got := *sts.Spec.Replicas; got != 1 {
		t.Errorf("replicas = %d, want 1 for a running workspace", got)
	}
	if sts.Spec.ServiceName != ServiceName(ws) {
		t.Errorf("serviceName = %q, want %q", sts.Spec.ServiceName, ServiceName(ws))
	}
	if got := sts.Labels[dwpkv1alpha1.WorkspaceLabel]; got != ws.Name {
		t.Errorf("workspace label = %q, want %q", got, ws.Name)
	}
	if got := sts.Spec.Template.Labels[dwpkv1alpha1.WorkspaceLabel]; got != ws.Name {
		t.Errorf("pod workspace label = %q, want %q", got, ws.Name)
	}

	pod := sts.Spec.Template.Spec
	if pod.ServiceAccountName != ServiceAccountName {
		t.Errorf("serviceAccountName = %q, want %q", pod.ServiceAccountName, ServiceAccountName)
	}
	if *pod.SecurityContext.RunAsUser != img.Spec.RunAsUser {
		t.Errorf("runAsUser = %d, want %d", *pod.SecurityContext.RunAsUser, img.Spec.RunAsUser)
	}
	if pod.NodeSelector["workload"] != "worker" || len(pod.Tolerations) != 1 {
		t.Errorf("placement not carried over: %+v %+v", pod.NodeSelector, pod.Tolerations)
	}

	c := pod.Containers[0]
	if c.Image != img.Spec.Image || c.ImagePullPolicy != img.Spec.ImagePullPolicy {
		t.Errorf("image = %q/%q, want %q/%q", c.Image, c.ImagePullPolicy, img.Spec.Image, img.Spec.ImagePullPolicy)
	}
	if got := c.Resources.Requests.Cpu().String(); got != "2" {
		t.Errorf("cpu request = %q, want the large size's 2", got)
	}
	if strings.Join(c.Command, " ") != "sleep infinity" {
		t.Errorf("command = %v, want the image's", c.Command)
	}
	if c.VolumeMounts[0].MountPath != img.Spec.HomePath {
		t.Errorf("home mounted at %q, want %q", c.VolumeMounts[0].MountPath, img.Spec.HomePath)
	}

	vct := sts.Spec.VolumeClaimTemplates[0]
	if got := vct.Spec.Resources.Requests[corev1.ResourceStorage]; got.Cmp(storage) != 0 {
		t.Errorf("volumeClaimTemplate storage = %s, want %s", got.String(), storage.String())
	}
}

func TestBuildStatefulSetSuspended(t *testing.T) {
	ws, img := testWorkspace(), testImage()
	ws.Spec.Running = false

	sts := BuildStatefulSet(ws, img, nil)

	if got := *sts.Spec.Replicas; got != 0 {
		t.Errorf("replicas = %d, want 0 for a suspended workspace", got)
	}
	// Suspend is a replica count, so the volumeClaimTemplate is unchanged and
	// the home directory survives.
	if len(sts.Spec.VolumeClaimTemplates) != 1 {
		t.Errorf("volumeClaimTemplates = %d, want the home volume kept", len(sts.Spec.VolumeClaimTemplates))
	}
}

func TestBuildStatefulSetKeepsAnImagesOwnCommand(t *testing.T) {
	img := testImage()
	img.Spec.Command = []string{"/usr/local/bin/init", "--foreground"}

	got := BuildStatefulSet(testWorkspace(), img, nil).Spec.Template.Spec.Containers[0].Command
	if strings.Join(got, " ") != "/usr/local/bin/init --foreground" {
		t.Errorf("command = %v, want the image's own entrypoint", got)
	}
}

// Placement comes from the entry, and the workspace overrides it wholesale.
// Merging the two produces a pod asking for every label at once, which
// schedules nowhere and says only "0/N nodes are available".
func TestWorkspacePlacementReplacesTheEntrys(t *testing.T) {
	ws, img := testWorkspace(), testImage()
	ws.Spec.NodeSelector = map[string]string{"workload": "gpu"}

	spec := BuildStatefulSet(ws, img, nil).Spec.Template.Spec
	if len(spec.NodeSelector) != 1 || spec.NodeSelector["workload"] != "gpu" {
		t.Errorf("nodeSelector = %v, want only the workspace's own", spec.NodeSelector)
	}
	// Untouched fields keep the entry's value rather than being blanked.
	if len(spec.Tolerations) != 1 {
		t.Errorf("tolerations = %v, want the catalog entry's kept", spec.Tolerations)
	}
}

// Extra volumes are appended after home, never in place of it: the home volume
// is what makes stopping non-destructive.
func TestExtraVolumesAreAppendedAfterHome(t *testing.T) {
	ws, img := testWorkspace(), testImage()
	ws.Spec.Volumes = []corev1.Volume{{
		Name:         "config",
		VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{}},
	}}
	ws.Spec.VolumeMounts = []corev1.VolumeMount{{Name: "config", MountPath: "/etc/app"}}
	ws.Spec.Env = []corev1.EnvVar{{Name: "TOKEN", Value: "x"}}

	container := BuildStatefulSet(ws, img, nil).Spec.Template.Spec.Containers[0]
	if container.VolumeMounts[0].Name != HomeVolumeName {
		t.Errorf("first mount is %q, want home", container.VolumeMounts[0].Name)
	}
	last := container.VolumeMounts[len(container.VolumeMounts)-1]
	if last.Name != "config" {
		t.Errorf("last mount = %v, want the workspace's own config mount", last)
	}
	if len(container.Env) != 1 || container.Env[0].Name != "TOKEN" {
		t.Errorf("env = %v, want the workspace's own", container.Env)
	}
}

func TestPodCarriesTheCatalogEntrysPullSecret(t *testing.T) {
	ws, img := testWorkspace(), testImage()
	img.Spec.ImagePullSecretRef = &corev1.LocalObjectReference{Name: "ecr-pull"}

	spec := BuildStatefulSet(ws, img, nil).Spec.Template.Spec
	if len(spec.ImagePullSecrets) != 1 || spec.ImagePullSecrets[0].Name != "ecr-pull" {
		t.Errorf("imagePullSecrets = %v, want [ecr-pull]", spec.ImagePullSecrets)
	}
}

func TestPodHasNoPullSecretWhenTheCatalogEntryNamesNone(t *testing.T) {
	spec := BuildStatefulSet(testWorkspace(), testImage(), nil).Spec.Template.Spec
	if len(spec.ImagePullSecrets) != 0 {
		t.Errorf("imagePullSecrets = %v, want none", spec.ImagePullSecrets)
	}
}

func TestPodMountsGitSSHSecretWhenPresent(t *testing.T) {
	ws, img := testWorkspace(), testImage()
	ref := &corev1.LocalObjectReference{Name: dwpkv1alpha1.GitSSHKeysSecretName}

	spec := BuildStatefulSet(ws, img, ref).Spec.Template.Spec
	container := spec.Containers[0]

	mounts := container.VolumeMounts
	last := mounts[len(mounts)-1]
	if last.Name != gitSSHVolumeName || last.MountPath != dwpkv1alpha1.GitSSHMountPath || !last.ReadOnly {
		t.Errorf("last mount = %+v, want a read-only %s mount at %s", last, gitSSHVolumeName, dwpkv1alpha1.GitSSHMountPath)
	}

	var gotEnv string
	for _, e := range container.Env {
		if e.Name == "GIT_SSH_COMMAND" {
			gotEnv = e.Value
		}
	}
	wantEnv := "ssh -F " + dwpkv1alpha1.GitSSHMountPath + "/config"
	if gotEnv != wantEnv {
		t.Errorf("GIT_SSH_COMMAND = %q, want %q", gotEnv, wantEnv)
	}

	found := false
	for _, v := range spec.Volumes {
		if v.Name == gitSSHVolumeName {
			found = true
			if v.Secret == nil || v.Secret.SecretName != dwpkv1alpha1.GitSSHKeysSecretName {
				t.Errorf("volume %q secret = %+v, want SecretName %s", gitSSHVolumeName, v.Secret, dwpkv1alpha1.GitSSHKeysSecretName)
			}
			// A Secret volume's files are owned by root regardless of who the
			// pod runs as; only the group follows PodSecurityContext.FSGroup.
			// 0400 has no group-read bit, so a non-root workspace (the common
			// case) could never read its own key - it needs 0440.
			if v.Secret == nil || v.Secret.DefaultMode == nil || *v.Secret.DefaultMode != 0o440 {
				t.Errorf("defaultMode = %v, want 0440 so the workspace's own uid (via fsGroup) can read the key",
					v.Secret.DefaultMode)
			}
		}
	}
	if !found {
		t.Errorf("volumes = %v, want a %s volume", spec.Volumes, gitSSHVolumeName)
	}
}

func TestPodHasNoGitSSHMountWhenSecretAbsent(t *testing.T) {
	spec := BuildStatefulSet(testWorkspace(), testImage(), nil).Spec.Template.Spec
	container := spec.Containers[0]

	for _, m := range container.VolumeMounts {
		if m.Name == gitSSHVolumeName {
			t.Errorf("volumeMounts = %v, want no %s mount", container.VolumeMounts, gitSSHVolumeName)
		}
	}
	for _, e := range container.Env {
		if e.Name == "GIT_SSH_COMMAND" {
			t.Errorf("env has GIT_SSH_COMMAND=%q, want none", e.Value)
		}
	}
	for _, v := range spec.Volumes {
		if v.Name == gitSSHVolumeName {
			t.Errorf("volumes = %v, want no %s volume", spec.Volumes, gitSSHVolumeName)
		}
	}
}

func TestBuildServiceIsHeadless(t *testing.T) {
	svc := BuildService(testWorkspace())
	if svc.Spec.ClusterIP != corev1.ClusterIPNone {
		t.Errorf("clusterIP = %q, want None", svc.Spec.ClusterIP)
	}
	if svc.Spec.Selector[dwpkv1alpha1.WorkspaceLabel] != "dev" {
		t.Errorf("selector = %v, want the workspace label", svc.Spec.Selector)
	}
}

func TestPodMeta(t *testing.T) {
	for _, tc := range []struct {
		name           string
		logsEnabled    bool
		metricsEnabled bool
	}{
		{"both enabled", true, true},
		{"both disabled", false, false},
		{"logs only", true, false},
		{"metrics only", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ws := testWorkspace()
			ws.Spec.Observability = dwpkv1alpha1.WorkspaceObservability{
				LogsEnabled:    tc.logsEnabled,
				MetricsEnabled: tc.metricsEnabled,
			}

			annotations, labels := PodMeta(ws)

			wantLogs := strconv.FormatBool(tc.logsEnabled)
			if got := annotations[LogsEnabledAnnotation]; got != wantLogs {
				t.Errorf("logs annotation = %q, want %q", got, wantLogs)
			}
			wantMetrics := strconv.FormatBool(tc.metricsEnabled)
			if got := labels[MetricsEnabledLabel]; got != wantMetrics {
				t.Errorf("metrics label = %q, want %q", got, wantMetrics)
			}
			if got := labels[dwpkv1alpha1.WorkspaceLabel]; got != ws.Name {
				t.Errorf("workspace label = %q, want %q - PodMeta must not drop Selector's labels", got, ws.Name)
			}
		})
	}
}

func TestBuildStatefulSetAppliesObservabilityToPodTemplate(t *testing.T) {
	ws, img := testWorkspace(), testImage()
	ws.Spec.Observability = dwpkv1alpha1.WorkspaceObservability{LogsEnabled: true, MetricsEnabled: false}

	sts := BuildStatefulSet(ws, img, nil)

	if got := sts.Spec.Template.Annotations[LogsEnabledAnnotation]; got != "true" {
		t.Errorf("pod template logs annotation = %q, want \"true\"", got)
	}
	if got := sts.Spec.Template.Labels[MetricsEnabledLabel]; got != "false" {
		t.Errorf("pod template metrics label = %q, want \"false\"", got)
	}
	// The pod selector must stay derived from Selector alone - it is immutable
	// on a StatefulSet, so it must never vary with a mutable spec field.
	if got := sts.Spec.Selector.MatchLabels[dwpkv1alpha1.WorkspaceLabel]; got != ws.Name {
		t.Errorf("statefulset selector = %v, want the workspace label only", sts.Spec.Selector.MatchLabels)
	}
}
