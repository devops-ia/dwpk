package registry

import (
	"testing"
	"time"
)

func imageAt(repo, tag string, pushed time.Time) RemoteImage {
	return RemoteImage{Repository: repo, Tag: tag, Reference: repo + ":" + tag, PushedAt: pushed}
}

func TestSelectFiltersByRepositoryIncludeExclude(t *testing.T) {
	t.Parallel()

	images := []RemoteImage{
		imageAt("dwpk/python", "latest", time.Now()),
		imageAt("dwpk/scratch", "latest", time.Now()),
		imageAt("other/tool", "latest", time.Now()),
	}
	sel, err := CompileSelection([]string{"^dwpk/.*"}, []string{"^dwpk/scratch.*"}, "latest", nil, 1)
	if err != nil {
		t.Fatalf("CompileSelection() error = %v", err)
	}

	got := Select(images, sel)
	if len(got) != 1 || got[0].Repository != "dwpk/python" {
		t.Fatalf("Select() = %+v, want only dwpk/python (exclude wins over include)", got)
	}
}

func TestSelectEmptyIncludeMeansEverything(t *testing.T) {
	t.Parallel()

	images := []RemoteImage{
		imageAt("a/one", "latest", time.Now()),
		imageAt("b/two", "latest", time.Now()),
	}
	sel, err := CompileSelection(nil, nil, "latest", nil, 1)
	if err != nil {
		t.Fatalf("CompileSelection() error = %v", err)
	}

	got := Select(images, sel)
	if len(got) != 2 {
		t.Fatalf("Select() = %+v, want both repositories with no include filter", got)
	}
}

func TestSelectTagModeLatestPicksNewestPushByPushTime(t *testing.T) {
	t.Parallel()

	now := time.Now()
	images := []RemoteImage{
		imageAt("dwpk/python", "3.11", now.Add(-time.Hour)),
		imageAt("dwpk/python", "3.12", now),
	}
	sel, err := CompileSelection(nil, nil, "latest", nil, 1)
	if err != nil {
		t.Fatalf("CompileSelection() error = %v", err)
	}

	got := Select(images, sel)
	if len(got) != 1 || got[0].Tag != "3.12" {
		t.Fatalf("Select() = %+v, want only the newest-pushed tag 3.12", got)
	}
}

func TestSelectTagModePatternMatchesAndLimits(t *testing.T) {
	t.Parallel()

	now := time.Now()
	images := []RemoteImage{
		imageAt("dwpk/python", "3.10", now.Add(-2*time.Hour)),
		imageAt("dwpk/python", "3.11", now.Add(-time.Hour)),
		imageAt("dwpk/python", "3.12", now),
		imageAt("dwpk/python", "latest", now),
	}
	sel, err := CompileSelection(nil, nil, "pattern", []string{`^3\.\d+$`}, 2)
	if err != nil {
		t.Fatalf("CompileSelection() error = %v", err)
	}

	got := Select(images, sel)
	if len(got) != 2 {
		t.Fatalf("Select() returned %d images, want 2 (limit)", len(got))
	}
	if got[0].Tag != "3.12" || got[1].Tag != "3.11" {
		t.Fatalf("Select() = %+v, want [3.12, 3.11] newest first, \"latest\" excluded by pattern", got)
	}
}

func TestCompileSelectionRejectsInvalidPattern(t *testing.T) {
	t.Parallel()

	if _, err := CompileSelection([]string{"("}, nil, "latest", nil, 1); err == nil {
		t.Fatal("CompileSelection() with an invalid include pattern: want error, got nil")
	}
}
