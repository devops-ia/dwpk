package ui

import (
	"net/http"
	"strings"
)

// handleOpenAPISpec serves the embedded OpenAPI document. It is public, no
// login required, so it can be fetched or shared outside the platform (an
// external tool checking the spec, a link in an issue, a colleague who isn't
// a dwpk user yet) - it is read-only documentation, not a control surface.
func (s *Server) handleOpenAPISpec(w http.ResponseWriter, r *http.Request) {
	data, err := Assets.ReadFile("assets/openapi.yaml")
	if err != nil {
		http.Error(w, "openapi spec unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.Write(data) //nolint:errcheck // best-effort write of a static asset
}

// apiDocsPage is the whole page: Swagger UI's own JS renders everything else
// once it fetches the spec. Kept as a plain string, rather than a templ
// component, because it carries none of the session/theme/nav chrome every
// other page needs - it is deliberately public and content-free besides the
// one embedded viewer.
//
// The bootstrap is a separate file, not a <script> block here. The handler's
// CSP has no script-src exemption, so an inline script is refused and the
// viewer never starts - which is exactly how this page came to render blank.
// The spec URL travels to it by data attribute, since a static file cannot
// know the base path.
const apiDocsPage = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>dwpk API reference</title>
<link rel="stylesheet" href="{{BASE}}/assets/vendor/swagger-ui.css">
<style>body { margin: 0; }</style>
</head>
<body>
<div id="swagger-ui" data-spec-url="{{BASE}}/api/v1/openapi.yaml"></div>
<script src="{{BASE}}/assets/vendor/swagger-ui-bundle.js"></script>
<script src="{{BASE}}/assets/vendor/swagger-ui-standalone-preset.js"></script>
<script src="{{BASE}}/assets/api-docs.js"></script>
</body>
</html>
`

// handleAPIDocsPage serves the interactive "try it out" API viewer. Swagger
// UI (not Redoc) was chosen specifically so a visitor can exercise a real
// request and see the real response, not just read a schema - that testing
// capability was the point of the request this page answers.
//
// Swagger UI renders some of its own layout through inline style attributes,
// and draws its expand arrows as `data:` SVG URLs from within the vendored
// stylesheet; the platform's default `default-src 'self'` CSP blocks both.
// This handler sets a page-scoped CSP allowing them, rather than loosening the
// header for the rest of the application. A `data:` image cannot execute, so
// the exemption costs nothing beyond this page.
//
// Scripts get no such exemption, and must not: everything this page runs is
// served from `assets/`, including its own bootstrap. If you find yourself
// adding an inline script here, move it to a file instead - the page will
// render blank otherwise, silently, with only a console message to say why.
func (s *Server) handleAPIDocsPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Security-Policy",
		"default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; "+
			"frame-ancestors 'none'; base-uri 'self'; form-action 'self'")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	page := strings.ReplaceAll(apiDocsPage, "{{BASE}}", s.basePath)
	w.Write([]byte(page)) //nolint:errcheck // best-effort write of a static page
}
