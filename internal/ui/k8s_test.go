package ui

import (
	"encoding/json"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// A JSON Patch "add", not a merge patch: ResourceValues.Requirements omits
// empty Limits/Requests keys and WorkspaceSpec.Env is omitempty, so a merge
// patch built from an edit that removes the GPU or an env var would leave
// the old value in place - RFC 7396 only ever merges object keys, it never
// deletes one that's absent from the patch body. "add" rather than
// "replace": replace errors if the path doesn't already exist, which a
// workspace created with no env vars never has.
func TestWorkspaceResourcesPatchAddsBothPaths(t *testing.T) {
	t.Parallel()

	resources := corev1.ResourceRequirements{
		Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")},
	}
	env := []corev1.EnvVar{{Name: "FOO", Value: "bar"}}

	body, err := workspaceResourcesPatch(resources, env)
	if err != nil {
		t.Fatalf("workspaceResourcesPatch() error = %v", err)
	}

	var ops []jsonPatchOp
	if err := json.Unmarshal(body, &ops); err != nil {
		t.Fatalf("patch body is not valid JSON: %v", err)
	}
	if len(ops) != 2 {
		t.Fatalf("got %d ops, want 2", len(ops))
	}
	for _, op := range ops {
		if op.Op != "add" {
			t.Errorf("op for %s = %q, want \"add\"", op.Path, op.Op)
		}
	}
	if ops[0].Path != "/spec/resources" {
		t.Errorf("ops[0].Path = %q, want /spec/resources", ops[0].Path)
	}
	if ops[1].Path != "/spec/env" {
		t.Errorf("ops[1].Path = %q, want /spec/env", ops[1].Path)
	}
}

// An edit that clears every env var must still replace the field with an
// empty value, not omit the op entirely - omitting it would leave the old
// list in place.
func TestWorkspaceResourcesPatchClearsEnv(t *testing.T) {
	t.Parallel()

	body, err := workspaceResourcesPatch(corev1.ResourceRequirements{}, nil)
	if err != nil {
		t.Fatalf("workspaceResourcesPatch() error = %v", err)
	}

	var ops []jsonPatchOp
	if err := json.Unmarshal(body, &ops); err != nil {
		t.Fatalf("patch body is not valid JSON: %v", err)
	}
	if len(ops) != 2 {
		t.Fatalf("got %d ops, want 2 (env must still be present, even when empty)", len(ops))
	}
}

// imageRegistryMergePatch always sends every editable field, even zero
// values: RoleARN, Include/Exclude and ImagePullSecretRef must be clearable,
// and a merge patch that omits a cleared field would leave the old value in
// place (RFC 7396 only ever merges object keys).
func TestImageRegistryMergePatchSendsClearedFields(t *testing.T) {
	t.Parallel()

	body, err := imageRegistryMergePatch(ImageRegistryEdit{
		Name:               "team-ecr",
		Region:             "eu-west-1",
		RoleARN:            "", // cleared
		Include:            nil,
		TagMode:            "latest",
		TagLimit:           1,
		ImagePullSecretRef: nil, // cleared
	})
	if err != nil {
		t.Fatalf("imageRegistryMergePatch() error = %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("patch body is not valid JSON: %v", err)
	}
	spec, ok := decoded["spec"].(map[string]any)
	if !ok {
		t.Fatalf("patch body has no spec object: %s", body)
	}
	if _, present := spec["imagePullSecretRef"]; !present {
		t.Error(`"imagePullSecretRef" is missing from the patch - a cleared reference must still be sent as null, not omitted`)
	}
	if spec["imagePullSecretRef"] != nil {
		t.Errorf("imagePullSecretRef = %v, want null", spec["imagePullSecretRef"])
	}
	aws, ok := spec["aws"].(map[string]any)
	if !ok {
		t.Fatalf("patch body has no spec.aws object: %s", body)
	}
	if roleARN, present := aws["roleArn"]; !present || roleARN != "" {
		t.Errorf(`aws.roleArn = %v (present=%v), want "" and present`, roleARN, present)
	}
}
