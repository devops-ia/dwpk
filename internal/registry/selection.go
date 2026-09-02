package registry

import (
	"fmt"
	"regexp"
	"slices"
)

// Selection is the compiled form of an ImageRegistrySpec.Sync: RE2 patterns
// pre-compiled once per sync rather than per repository, and the tag policy
// as plain Go values so Select needs no Kubernetes types at all.
type Selection struct {
	Include     []*regexp.Regexp
	Exclude     []*regexp.Regexp
	TagPattern  []*regexp.Regexp
	TagIsLatest bool
	TagLimit    int
}

// Select applies include/exclude to repository names and the tag policy
// within each surviving repository, returning the images to sync - newest
// first within a repository, capped at TagLimit.
func Select(images []RemoteImage, sel Selection) []RemoteImage {
	byRepo := map[string][]RemoteImage{}
	var order []string
	for _, image := range images {
		if !repositoryMatches(image.Repository, sel) {
			continue
		}
		if _, seen := byRepo[image.Repository]; !seen {
			order = append(order, image.Repository)
		}
		byRepo[image.Repository] = append(byRepo[image.Repository], image)
	}

	result := make([]RemoteImage, 0, len(images))
	for _, repo := range order {
		result = append(result, selectTags(byRepo[repo], sel)...)
	}
	return result
}

func repositoryMatches(repository string, sel Selection) bool {
	for _, pattern := range sel.Exclude {
		if pattern.MatchString(repository) {
			return false
		}
	}
	if len(sel.Include) == 0 {
		return true
	}
	for _, pattern := range sel.Include {
		if pattern.MatchString(repository) {
			return true
		}
	}
	return false
}

// selectTags narrows one repository's tags to the ones the tag policy keeps,
// newest first, capped at TagLimit.
func selectTags(images []RemoteImage, sel Selection) []RemoteImage {
	matching := make([]RemoteImage, 0, len(images))
	for _, image := range images {
		if !sel.TagIsLatest && !tagMatches(image.Tag, sel.TagPattern) {
			continue
		}
		matching = append(matching, image)
	}
	slices.SortFunc(matching, func(a, b RemoteImage) int {
		return b.PushedAt.Compare(a.PushedAt)
	})

	limit := sel.TagLimit
	if limit <= 0 {
		limit = 1
	}
	if len(matching) > limit {
		matching = matching[:limit]
	}
	return matching
}

func tagMatches(tag string, patterns []*regexp.Regexp) bool {
	for _, pattern := range patterns {
		if pattern.MatchString(tag) {
			return true
		}
	}
	return false
}

// CompileSelection turns the CR's plain-string RE2 patterns into a Selection,
// taking plain values rather than the CRD type so this package stays free of
// a Kubernetes API import. tagMode is TagSelectorModeLatest or
// TagSelectorModePattern (api/v1alpha1's constants); the caller passes the
// string so this package need not import that package either.
func CompileSelection(include, exclude []string, tagMode string, tagPatterns []string, tagLimit int32) (Selection, error) {
	compiledInclude, err := compileAll(include)
	if err != nil {
		return Selection{}, fmt.Errorf("compile include patterns: %w", err)
	}
	compiledExclude, err := compileAll(exclude)
	if err != nil {
		return Selection{}, fmt.Errorf("compile exclude patterns: %w", err)
	}
	compiledTags, err := compileAll(tagPatterns)
	if err != nil {
		return Selection{}, fmt.Errorf("compile tag patterns: %w", err)
	}
	return Selection{
		Include:     compiledInclude,
		Exclude:     compiledExclude,
		TagPattern:  compiledTags,
		TagIsLatest: tagMode != "pattern",
		TagLimit:    int(tagLimit),
	}, nil
}

func compileAll(patterns []string) ([]*regexp.Regexp, error) {
	compiled := make([]*regexp.Regexp, 0, len(patterns))
	for _, pattern := range patterns {
		re, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("%q: %w", pattern, err)
		}
		compiled = append(compiled, re)
	}
	return compiled, nil
}
