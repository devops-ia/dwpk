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
	"fmt"
	"strconv"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	dwpkv1alpha1 "github.com/devops-ia/dwpk/api/v1alpha1"
)

const (
	// ContainerName is the single container in a workspace pod. The gateway
	// execs into it by name.
	ContainerName = "workspace"

	// HomeVolumeName is the volumeClaimTemplate name, so the PVC that holds the
	// home directory is `home-<workspace>-0` and stays findable by hand.
	HomeVolumeName = "home"

	// LogsEnabledAnnotation drives whether the cluster's log-shipping sidecar
	// injector attaches to this pod (SPEC §4.3 observability field).
	LogsEnabledAnnotation = "dwpk.devops-ia.io/logs-enabled"

	// MetricsEnabledLabel is matched by the PodMonitor selector that scrapes
	// workspace pods (SPEC §4.3 observability field).
	MetricsEnabledLabel = "dwpk.devops-ia.io/metrics-enabled"
)

// HomeClaimName is the PVC holding the home directory.
//
// A StatefulSet names a volumeClaimTemplate's claim
// <template>-<statefulset>-<ordinal>, and a workspace has exactly one replica.
// Derived rather than discovered because the deletion path needs the name after
// the StatefulSet is already gone.
func HomeClaimName(ws *dwpkv1alpha1.Workspace) string {
	return HomeVolumeName + "-" + ws.Name + "-0"
}

// PodName is the pod a Workspace runs in. A StatefulSet numbers its pods from
// zero, so with one replica the name is derivable rather than discoverable.
func PodName(ws *dwpkv1alpha1.Workspace) string { return ws.Name + "-0" }

// SSHUser is the username this workspace answers to on the gateway, and the
// left-hand side of status.endpoint.
//
// It is <user>-<workspace> rather than <workspace> because a workspace name is
// only unique within its namespace: two people may both have one called "dev",
// and the gateway matches the SSH username across the whole cluster. The old
// scheme made that collision unresolvable.
//
// The gateway compares against this computed value rather than splitting the
// username on "-": both halves may contain hyphens, so parsing it back is
// guesswork and this is not.
func SSHUser(ws *dwpkv1alpha1.Workspace) string {
	// A UserSpace may name its own namespace, in which case there is no prefix
	// to strip and the namespace itself becomes the user half. Still unique,
	// still the same on both sides of the connection.
	return strings.TrimPrefix(ws.Namespace, dwpkv1alpha1.NamespacePrefix) + "-" + ws.Name
}

// ServiceName is the headless Service that gives the pod stable DNS.
func ServiceName(ws *dwpkv1alpha1.Workspace) string { return ws.Name }

// Selector labels both the StatefulSet's pod selector and everything the
// Workspace owns. It is immutable on a StatefulSet, so it stays minimal.
func Selector(ws *dwpkv1alpha1.Workspace) map[string]string {
	return map[string]string{dwpkv1alpha1.WorkspaceLabel: ws.Name}
}

// PodMeta returns the annotations and labels the observability toggles add on
// top of Selector, kept separate so the pod selector itself never changes
// shape based on a mutable spec field (a StatefulSet's selector is immutable).
func PodMeta(ws *dwpkv1alpha1.Workspace) (annotations, labels map[string]string) {
	annotations = map[string]string{
		LogsEnabledAnnotation: strconv.FormatBool(ws.Spec.Observability.LogsEnabled),
	}
	labels = map[string]string{
		MetricsEnabledLabel: strconv.FormatBool(ws.Spec.Observability.MetricsEnabled),
	}
	for k, v := range Selector(ws) {
		labels[k] = v
	}
	return annotations, labels
}

// BuildService is the headless Service a StatefulSet requires. It carries no
// ports: nothing in the pod listens (§6.2 - the gateway bridges over
// pods/exec), and the Service exists only to give `<name>-0.<svc>` DNS.
func BuildService(ws *dwpkv1alpha1.Workspace) *corev1.Service {
	return &corev1.Service{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Service"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      ServiceName(ws),
			Namespace: ws.Namespace,
			Labels:    Selector(ws),
		},
		Spec: corev1.ServiceSpec{
			ClusterIP: corev1.ClusterIPNone,
			Selector:  Selector(ws),
			// So DNS resolves the pod while it is still starting.
			PublishNotReadyAddresses: true,
		},
	}
}

// BuildStatefulSet is the whole workload: one replica when the workspace is
// running, zero when it is suspended, and a volumeClaimTemplate that keeps the
// home directory across both.
// gitSSHKeysRef is nil when the reconciler found no
// dwpkv1alpha1.GitSSHKeysSecretName Secret in the workspace's namespace, or a
// reference to it when it did - the same optional-reference shape
// img.Spec.ImagePullSecretRef already uses below, rather than a bare bool
func BuildStatefulSet(ws *dwpkv1alpha1.Workspace, img *dwpkv1alpha1.WorkspaceImage, gitSSHKeysRef *corev1.LocalObjectReference) *appsv1.StatefulSet {

	replicas := int32(0)
	if ws.Spec.Running {
		replicas = 1
	}

	return &appsv1.StatefulSet{
		TypeMeta: metav1.TypeMeta{APIVersion: "apps/v1", Kind: "StatefulSet"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      ws.Name,
			Namespace: ws.Namespace,
			Labels:    Selector(ws),
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas:            ptr.To(replicas),
			ServiceName:         ServiceName(ws),
			Selector:            &metav1.LabelSelector{MatchLabels: Selector(ws)},
			PodManagementPolicy: appsv1.OrderedReadyPodManagement,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: func() metav1.ObjectMeta {
					annotations, labels := PodMeta(ws)
					return metav1.ObjectMeta{Annotations: annotations, Labels: labels}
				}(),
				Spec: podSpec(ws, img, gitSSHKeysRef),
			},
			VolumeClaimTemplates: []corev1.PersistentVolumeClaim{{
				ObjectMeta: metav1.ObjectMeta{
					Name:   HomeVolumeName,
					Labels: Selector(ws),
				},
				Spec: corev1.PersistentVolumeClaimSpec{
					AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					Resources: corev1.VolumeResourceRequirements{
						// Non-nil by the time the reconciler builds anything: the
						// mutating webhook defaults it from the image (§7.3).
						Requests: corev1.ResourceList{corev1.ResourceStorage: *ws.Spec.Storage},
					},
				},
			}},
		},
	}
}

// containerSecurityContext states the container's privileges rather than
// leaving them to Kubernetes' defaults, which permit privilege escalation and
// hand every container the runtime's full default capability set.
//
// Capabilities are dropped and the seccomp profile is set in both branches:
// allowRoot grants root inside the container, and nothing more - it is not a
// reason to also hand out NET_RAW, DAC_OVERRIDE or SETUID.
func containerSecurityContext(img *dwpkv1alpha1.WorkspaceImage) *corev1.SecurityContext {
	seccomp := &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}
	capabilities := &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}
	if img.Spec.AllowRoot {
		return &corev1.SecurityContext{
			AllowPrivilegeEscalation: ptr.To(true),
			RunAsNonRoot:             ptr.To(false),
			Privileged:               ptr.To(false),
			Capabilities:             capabilities,
			SeccompProfile:           seccomp,
		}
	}
	return &corev1.SecurityContext{
		AllowPrivilegeEscalation: ptr.To(false),
		Privileged:               ptr.To(false),
		Capabilities:             capabilities,
		SeccompProfile:           seccomp,
	}
}

// workspaceUser is the uid the pod runs as: 0 when the catalog entry allows
// root, the entry's runAsUser otherwise.
func workspaceUser(img *dwpkv1alpha1.WorkspaceImage) int64 {
	if img.Spec.AllowRoot {
		return 0
	}
	return img.Spec.RunAsUser
}

func podSpec(ws *dwpkv1alpha1.Workspace, img *dwpkv1alpha1.WorkspaceImage, gitSSHKeysRef *corev1.LocalObjectReference) corev1.PodSpec {
	uid := workspaceUser(img)
	spec := corev1.PodSpec{
		// Requirement 7: kubectl from inside the workspace reaches this
		// namespace and nowhere else, via the RoleBinding the UserSpace owns.
		ServiceAccountName: ServiceAccountName,
		SecurityContext: &corev1.PodSecurityContext{
			RunAsUser: ptr.To(uid),
			// The home PVC is mounted by the kubelet as root; without an fsGroup
			// a non-root workspace cannot write to its own home directory.
			FSGroup: ptr.To(uid),
		},
		Containers: []corev1.Container{{
			Name:            ContainerName,
			Image:           img.Spec.Image,
			ImagePullPolicy: img.Spec.ImagePullPolicy,
			Resources:       ws.Spec.Resources,
			Command:         img.Spec.Command,
			WorkingDir:      img.Spec.HomePath,
			SecurityContext: containerSecurityContext(img),
			VolumeMounts:    []corev1.VolumeMount{{Name: HomeVolumeName, MountPath: img.Spec.HomePath}},
		}},
	}
	applyPasswdFix(&spec, img, uid)
	applyPlacement(&spec, ws, img)
	applyExtras(&spec, ws, img, gitSSHKeysRef)
	return spec
}

// applyPasswdFix ensures the container's uid resolves to a real user through
// the operating system's own user database, not just $HOME and $USER.
//
// Kubernetes happily runs a container as any numeric uid, but the base
// image's own /etc/passwd was built for whichever users its Dockerfile
// created - a catalog entry naming an arbitrary spec.runAsUser very often
// names one with no entry at all. Most of a workspace does not notice: a
// shell prompt and $HOME are plain environment variables. getpwuid(3) is the
// one call that goes through the passwd database instead, and OpenSSH's ssh
// client makes it on every invocation to resolve the local username and home
// directory before doing anything else - "No user exists for uid 1000" is
// its own fatal error when that lookup comes back empty, which breaks `git
// clone` over ssh (and anything else getpwuid-dependent) for exactly the
// images most likely to be added to a catalog. Already surveyed
// this: four of seven ordinary base images ship no passwd entry for uid 1000.
// Requiring every catalog image to carry a matching one would turn "add an
// image" into a coordination problem; this fixes it once, in the platform.
//
// uid 0 is exempt: root has a passwd entry in essentially every base image,
// and $HOME=/root needs no lookup to find.
//
// The fix is an init container - it needs root to write /etc/passwd, which
// the workspace container itself is deliberately never granted - that copies
// the image's own passwd/group files into a shared emptyDir, appends an entry
// for the running uid if one is not already there, and the main container
// mounts the result over its own /etc/passwd and /etc/group. musl (Alpine)
// and glibc both consult /etc/passwd directly with no further configuration
// needed.
func applyPasswdFix(spec *corev1.PodSpec, img *dwpkv1alpha1.WorkspaceImage, uid int64) {
	if uid == 0 {
		return
	}
	spec.InitContainers = append(spec.InitContainers, corev1.Container{
		Name:            passwdFixContainerName,
		Image:           img.Spec.Image,
		ImagePullPolicy: img.Spec.ImagePullPolicy,
		Command:         []string{"/bin/sh", "-c", passwdFixScript},
		Args:            []string{"fix-passwd", strconv.FormatInt(uid, 10), img.Spec.HomePath},
		SecurityContext: &corev1.SecurityContext{
			RunAsUser:                ptr.To(int64(0)),
			RunAsNonRoot:             ptr.To(false),
			AllowPrivilegeEscalation: ptr.To(false),
			Privileged:               ptr.To(false),
		},
		VolumeMounts: []corev1.VolumeMount{{Name: passwdFixVolumeName, MountPath: passwdFixMountPath}},
	})
	spec.Volumes = append(spec.Volumes, corev1.Volume{
		Name:         passwdFixVolumeName,
		VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
	})
	container := &spec.Containers[0]
	container.VolumeMounts = append(container.VolumeMounts,
		corev1.VolumeMount{Name: passwdFixVolumeName, MountPath: "/etc/passwd", SubPath: "passwd", ReadOnly: true},
		corev1.VolumeMount{Name: passwdFixVolumeName, MountPath: "/etc/group", SubPath: "group", ReadOnly: true},
	)
}

const (
	passwdFixContainerName = "fix-passwd"
	passwdFixVolumeName    = "passwd-fix"
	passwdFixMountPath     = "/dwpk-etc"
)

// passwdFixScript runs as $0=fix-passwd $1=uid $2=home. grep -E anchors the
// uid to the passwd/group third field exactly, so uid 1 never matches a line
// for uid 100 in passing.
var passwdFixScript = fmt.Sprintf(`set -e
uid="$1"
home="$2"
cp /etc/passwd %[1]s/passwd
grep -qE "^[^:]*:[^:]*:${uid}:" %[1]s/passwd || echo "workspace:x:${uid}:0:workspace:${home}:/bin/sh" >> %[1]s/passwd
if [ -f /etc/group ]; then cp /etc/group %[1]s/group; else : > %[1]s/group; fi
grep -qE "^[^:]*:[^:]*:${uid}:" %[1]s/group || echo "workspace:x:${uid}:" >> %[1]s/group
chmod 0644 %[1]s/passwd %[1]s/group
`, passwdFixMountPath)

// applyPlacement starts from the catalog entry's placement and lets the
// workspace replace any part of it.
//
// Replace rather than merge, and the difference matters. Two node selectors
// merged into one is a pod asking for every label at once, which schedules on
// nothing and reports only "0/N nodes are available". Replacing means the
// workspace's answer is the whole answer, and an unset field keeps the entry's.
func applyPlacement(spec *corev1.PodSpec, ws *dwpkv1alpha1.Workspace, img *dwpkv1alpha1.WorkspaceImage) {
	if p := img.Spec.Placement; p != nil {
		spec.NodeSelector = p.NodeSelector
		spec.Tolerations = p.Tolerations
		spec.Affinity = p.Affinity
	}
	if len(ws.Spec.NodeSelector) > 0 {
		spec.NodeSelector = ws.Spec.NodeSelector
	}
	if len(ws.Spec.Tolerations) > 0 {
		spec.Tolerations = ws.Spec.Tolerations
	}
	if ws.Spec.Affinity != nil {
		spec.Affinity = ws.Spec.Affinity
	}
}

// applyExtras adds the workspace's own environment and volumes, and the
// catalog entry's pull secret.
//
// The home volume and its mount are appended to rather than replaced: admission
// refuses a workspace that names either of them, so the two lists cannot
// collide here. Doing it in this order also means a mount the user added can
// never come out ahead of home in the list and shadow it.
func applyExtras(
	spec *corev1.PodSpec, ws *dwpkv1alpha1.Workspace, img *dwpkv1alpha1.WorkspaceImage,
	gitSSHKeysRef *corev1.LocalObjectReference,
) {
	container := &spec.Containers[0]
	container.Env = append(container.Env, ws.Spec.Env...)
	container.EnvFrom = append(container.EnvFrom, ws.Spec.EnvFrom...)
	container.VolumeMounts = append(container.VolumeMounts, ws.Spec.VolumeMounts...)
	spec.Volumes = append(spec.Volumes, ws.Spec.Volumes...)
	if ref := img.Spec.ImagePullSecretRef; ref != nil {
		spec.ImagePullSecrets = []corev1.LocalObjectReference{*ref}
	}
	// Appended last, after home (podSpec) and the workspace's own extras
	// above: nothing here can come ahead of home in VolumeMounts.
	if gitSSHKeysRef != nil {
		container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{
			Name:      gitSSHVolumeName,
			MountPath: dwpkv1alpha1.GitSSHMountPath,
			ReadOnly:  true,
		})
		container.Env = append(container.Env, corev1.EnvVar{
			Name:  "GIT_SSH_COMMAND",
			Value: "ssh -F " + dwpkv1alpha1.GitSSHMountPath + "/config",
		})
		spec.Volumes = append(spec.Volumes, corev1.Volume{
			Name: gitSSHVolumeName,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: gitSSHKeysRef.Name,
					// A Secret volume's files are always owned by root, whoever the
					// pod runs as - only their group follows
					// PodSecurityContext.FSGroup (podSpec sets it to the same uid as
					// RunAsUser). 0400 has no group-read bit, so a non-root
					// workspace could never read its own key: ssh would fail with
					// "Permission denied" before ever reaching the network. 0440
					// grants the group (this workspace's own uid) read access and
					// nothing else - no world access, and no group/other write bit
					// for ssh's own "unprotected private key" check to reject.
					DefaultMode: ptr.To(int32(0o440)),
				},
			},
		})
	}
}

// gitSSHVolumeName is the pod-local volume name for the mounted git-ssh
// Secret - distinct from HomeVolumeName, so the two can never collide.
const gitSSHVolumeName = "git-ssh-keys"
