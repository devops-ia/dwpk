package ui

import (
	"context"
	"net/http"

	dwpkv1alpha1 "github.com/devops-ia/dwpk/api/v1alpha1"
)

// themeCookieName carries the viewer's explicit light/dark choice.
//
// It is deliberately not HttpOnly: it holds a display preference rather than a
// credential, and the toggle in app.js has to write it. It is also the only way
// the server can know the preference before rendering - the CSP forbids inline
// script (§8.5), so nothing can set the theme from inside the document before
// first paint. Reading the cookie and stamping the attribute server-side is
// what keeps the page from flashing the wrong theme on load.
const themeCookieName = "dwpk_ui_theme"

const (
	themeLight = "light"
	themeDark  = "dark"
)

type themeContextKey struct{}

func withTheme(ctx context.Context, theme string) context.Context {
	return context.WithValue(ctx, themeContextKey{}, theme)
}

// themeOf returns the explicit theme choice, or "" when the viewer has not
// chosen one. Empty means the stylesheet falls back to prefers-color-scheme,
// which is the right default rather than a missing value.
func themeOf(ctx context.Context) string {
	theme, _ := ctx.Value(themeContextKey{}).(string)
	return theme
}

// themeFromRequest reads the preference cookie, ignoring anything that is not
// one of the two known values so a hand-edited cookie cannot inject an
// attribute value into the page.
func themeFromRequest(r *http.Request) string {
	if r == nil {
		return ""
	}
	cookie, err := r.Cookie(themeCookieName)
	if err != nil {
		return ""
	}
	switch cookie.Value {
	case themeLight, themeDark:
		return cookie.Value
	default:
		return ""
	}
}

// renderContext carries everything a template needs that is not a parameter:
// the base path for link generation, the viewer's theme, and the current path
// so the sidebar can mark where they are.
func (s *Server) renderContext(r *http.Request) context.Context {
	ctx := withTheme(withBasePath(context.Background(), s.basePath), themeFromRequest(r))
	if r != nil {
		ctx = withCurrentPath(ctx, r.URL.Path)
		// The platform's own name and logo, so every page can render the brand
		// without each handler having to fetch it. Cached, and nil-safe all the
		// way down: a cluster with no settings object renders the defaults.
		ctx = withPlatform(ctx, s.platformConfig(r))
	}
	return ctx
}

type platformKey struct{}

func withPlatform(ctx context.Context, config *dwpkv1alpha1.PlatformConfig) context.Context {
	return context.WithValue(ctx, platformKey{}, config)
}

// platformOf is what the templates call. It returns a nil *PlatformConfig when
// there are no settings, which every accessor on that type handles.
func platformOf(ctx context.Context) *dwpkv1alpha1.PlatformConfig {
	config, _ := ctx.Value(platformKey{}).(*dwpkv1alpha1.PlatformConfig)
	return config
}

// platformName is the brand string for titles and the sidebar.
func platformName(ctx context.Context) string {
	return platformOf(ctx).Name()
}

// hasPlatformLogo reports whether to render the uploaded mark instead of the
// built-in one.
func hasPlatformLogo(ctx context.Context) bool {
	return platformOf(ctx).HasLogo()
}

type currentPathKey struct{}

func withCurrentPath(ctx context.Context, path string) context.Context {
	return context.WithValue(ctx, currentPathKey{}, path)
}

// currentPathOf is what the sidebar marks as the current page. It is the path
// with the base path already stripped, matching the literals the nav uses.
func currentPathOf(ctx context.Context) string {
	path, _ := ctx.Value(currentPathKey{}).(string)
	return path
}
