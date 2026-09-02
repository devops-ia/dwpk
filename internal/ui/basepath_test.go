package ui

import (
	"context"
	"testing"
)

func TestNormalizeBasePath(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"":         "",
		"/":        "",
		"dwpk":     "/dwpk",
		"/dwpk":    "/dwpk",
		"/dwpk/":   "/dwpk",
		"/dwpk///": "/dwpk",
	}
	for in, want := range cases {
		if got := normalizeBasePath(in); got != want {
			t.Errorf("normalizeBasePath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLinkPathPrefixesWithBasePath(t *testing.T) {
	t.Parallel()
	ctx := withBasePath(context.Background(), "/dwpk")
	if got := linkPath(ctx, "/w/dev"); got != "/dwpk/w/dev" {
		t.Errorf("linkPath = %q, want /dwpk/w/dev", got)
	}
}

func TestLinkPathWithoutBasePath(t *testing.T) {
	t.Parallel()
	if got := linkPath(context.Background(), "/w/dev"); got != "/w/dev" {
		t.Errorf("linkPath = %q, want /w/dev", got)
	}
}

func TestBasePathOfDefaultsToEmpty(t *testing.T) {
	t.Parallel()
	if got := basePathOf(context.Background()); got != "" {
		t.Errorf("basePathOf() = %q, want empty", got)
	}
}
