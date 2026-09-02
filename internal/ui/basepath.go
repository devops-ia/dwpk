package ui

import "context"

// basePathContextKey is the context key used to carry the configured base
// path into templ components so rendered links stay correct when the UI is
// served behind a reverse proxy under a non-root path (e.g. "/dwpk").
type basePathContextKey struct{}

// withBasePath returns a context carrying basePath for link generation.
func withBasePath(ctx context.Context, basePath string) context.Context {
	return context.WithValue(ctx, basePathContextKey{}, basePath)
}

// basePathOf returns the base path stored in ctx, or "" if none was set.
func basePathOf(ctx context.Context) string {
	bp, _ := ctx.Value(basePathContextKey{}).(string)
	return bp
}

// linkPath joins the request's base path with an absolute in-app path
// (which must start with "/"), used by templ components to build hrefs.
func linkPath(ctx context.Context, path string) string {
	return basePathOf(ctx) + path
}

// normalizeBasePath trims a trailing slash and ensures a leading slash for
// non-empty base paths; "" and "/" both mean "no base path".
func normalizeBasePath(basePath string) string {
	if basePath == "" || basePath == "/" {
		return ""
	}
	if basePath[0] != '/' {
		basePath = "/" + basePath
	}
	for len(basePath) > 1 && basePath[len(basePath)-1] == '/' {
		basePath = basePath[:len(basePath)-1]
	}
	return basePath
}
