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
	"os"
	"path/filepath"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/util/yaml"
)

// The sample and example manifests must still decode into the current types.
//
// They rot silently: a field removed from a type is simply gone, and a YAML
// naming it keeps sitting in the chart looking authoritative. kubectl decodes
// strictly, so such a file does not "mostly work" - it fails outright, and the
// first person to find out is somebody following the documentation.
//
// Three rounds of schema deletions left every WorkspaceImage and Workspace
// sample unappliable, which is what this test exists to stop repeating.
func TestShippedManifestsMatchTheCurrentSchema(t *testing.T) {
	t.Parallel()

	roots := []string{filepath.Join("..", "..", "config", "samples")}
	// The chart's example CRs live in the helm-dwpk repository, which is not
	// checked out beside every clone of this one. DWPK_CHART_EXAMPLES points at
	// them when it is - the CRD sync workflow sets it, so CI still covers the
	// shipped chart examples on every api/v1alpha1 change.
	if dir := os.Getenv("DWPK_CHART_EXAMPLES"); dir != "" {
		roots = append(roots, dir)
	}

	// Strict decoding into the Go type is the same check kubectl performs, and
	// it is the half that catches a field the CRD no longer has.
	into := map[string]func() any{
		"WorkspaceImage": func() any { return &WorkspaceImage{} },
		"Workspace":      func() any { return &Workspace{} },
		"UserSpace":      func() any { return &UserSpace{} },
		"ImageRegistry":  func() any { return &ImageRegistry{} },
		"Project":        nil, // deleted; a sample naming it must not survive
		"AccessRequest":  nil,
	}

	checked := 0
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			t.Fatalf("read %s: %v", root, err)
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") ||
				entry.Name() == "kustomization.yaml" {
				continue
			}
			path := filepath.Join(root, entry.Name())
			raw, err := os.ReadFile(path) // #nosec G304 -- a fixed directory of repo files
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}

			kind := kindOf(string(raw))
			builder, known := into[kind]
			if !known {
				continue
			}
			if builder == nil {
				t.Errorf("%s declares %s, a kind that no longer exists", path, kind)
				continue
			}
			if err := yaml.UnmarshalStrict(raw, builder()); err != nil {
				t.Errorf("%s does not match the current schema: %v", path, err)
				continue
			}
			checked++
		}
	}

	// A test that silently checks nothing is worse than no test: if the sample
	// directories move, this must fail rather than pass quietly. config/samples
	// alone carries three; the chart examples raise the floor when they are
	// present, so adding the directory cannot quietly check nothing either.
	floor := 4
	if len(roots) > 1 {
		floor = 6
	}
	if checked < floor {
		t.Fatalf("only %d manifests were checked, want at least %d; the sample directories have moved", checked, floor)
	}
}

func kindOf(manifest string) string {
	for line := range strings.SplitSeq(manifest, "\n") {
		if rest, found := strings.CutPrefix(line, "kind:"); found {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}
