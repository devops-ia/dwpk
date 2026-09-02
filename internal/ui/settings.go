package ui

import (
	"bytes"
	"fmt"
	"image"
	"io"
	"net/http"
	"strings"

	// Registered for their decoders only: image.DecodeConfig needs a format to
	// have been registered before it can read one.
	_ "image/jpeg"
	_ "image/png"

	dwpkv1alpha1 "github.com/devops-ia/dwpk/api/v1alpha1"
)

// maxLogoPixels is the largest logo accepted, per the request. It is checked on
// upload rather than scaled: a platform that silently resizes a logo produces a
// blurry mark and no explanation of why.
const maxLogoPixels = 120

// maxLogoBytes caps what is stored on the object. Comfortably above a 120x120
// PNG and far below anything that makes an etcd object awkward.
const maxLogoBytes = 128 << 10

// csrfFormMaxMemory is the memory cap used whenever a multipart form is
// parsed to read its CSRF token - shared with the middleware's early parse
// (middleware.go:requestCSRFToken) so the settings handler's own
// r.ParseMultipartForm call below agrees with the one CSRF validation
// already performed, rather than risking two different limits reading the
// same body two different ways.
const csrfFormMaxMemory = maxLogoBytes + (1 << 20)

// SettingsData backs Administration → Global.
type SettingsData struct {
	Session RequestSession
	Config  *dwpkv1alpha1.PlatformConfig
	Runtime RuntimeSettings
	Error   string
	Notice  string
}

func (s *Server) handleAdminSettings(w http.ResponseWriter, r *http.Request) {
	session, ok := s.requireUserSpaceAdmin(w, r)
	if !ok {
		return
	}
	s.renderSettings(w, r, session, "", doneNotice(r))
}

func (s *Server) renderSettings(w http.ResponseWriter, r *http.Request, session RequestSession, errorMessage, notice string) {
	api, err := s.clientFactory.ForToken(session.Token)
	if err != nil {
		s.writeErrorPage(w, r, err)
		return
	}
	// A missing singleton is the normal state before anyone has changed
	// anything, so it renders the defaults rather than an error.
	//
	// Substituted with an empty object rather than left nil. The accessors on
	// the type are nil-safe, but the form reads spec fields directly to fill its
	// inputs - and a nil pointer there panics the handler. That is the state of
	// every cluster nobody has visited this screen on yet, which is the worst
	// possible thing for it to be a crash.
	config, _ := api.GetPlatformConfig(r.Context())
	if config == nil {
		config = &dwpkv1alpha1.PlatformConfig{}
	}

	status := http.StatusOK
	if errorMessage != "" {
		status = http.StatusBadRequest
	}
	s.renderAuthedPage(w, r, status, session, "Global", AdminSettingsPage(SettingsData{
		Session: session,
		Config:  config,
		Runtime: s.runtime,
		Error:   errorMessage,
		Notice:  notice,
	}))
}

func (s *Server) handleAdminUpdateSettings(w http.ResponseWriter, r *http.Request) {
	session, ok := s.requireUserSpaceAdmin(w, r)
	if !ok {
		return
	}
	api, err := s.clientFactory.ForToken(session.Token)
	if err != nil {
		s.writeErrorPage(w, r, err)
		return
	}
	// The multipart limit is the byte cap plus room for the other fields; a
	// larger body is refused before it is read into memory.
	if err := r.ParseMultipartForm(csrfFormMaxMemory); err != nil {
		s.renderSettings(w, r, session, "That upload is too large.", "")
		return
	}

	update := PlatformConfigUpdate{
		DisplayName:     strings.TrimSpace(r.FormValue("displayName")),
		DefaultTheme:    strings.TrimSpace(r.FormValue("defaultTheme")),
		SupportEmail:    strings.TrimSpace(r.FormValue("supportEmail")),
		GPUResourceName: strings.TrimSpace(r.FormValue("gpuResourceName")),
		ClearLogo:       r.FormValue("clearLogo") != "",
	}

	if file, _, err := r.FormFile("logo"); err == nil {
		defer file.Close() //nolint:errcheck // read-only upload

		logo, err := readLogo(file)
		if err != nil {
			s.renderSettings(w, r, session, err.Error(), "")
			return
		}
		update.Logo = logo
	}

	if err := api.PatchPlatformConfig(r.Context(), update); err != nil {
		s.renderSettings(w, r, session, err.Error(), "")
		return
	}
	s.invalidatePlatformConfig()
	http.Redirect(w, r, s.path(donePath("/admin/settings", "settings")), http.StatusSeeOther)
}

// readLogo validates an uploaded image and returns it.
//
// The dimensions are read from the image itself rather than trusted from the
// form, because the form is whatever the browser was asked to send. SVG is the
// exception: it has no pixel dimensions to measure, so only its size is capped.
func readLogo(file io.Reader) (*dwpkv1alpha1.PlatformLogo, error) {
	// One byte over the cap so a file exactly at the limit is accepted and
	// anything larger is detectably truncated rather than silently accepted.
	data, err := io.ReadAll(io.LimitReader(file, maxLogoBytes+1))
	if err != nil {
		return nil, errUpload("could not read the uploaded file")
	}
	if len(data) > maxLogoBytes {
		return nil, errUpload("that image is larger than 128KB")
	}
	if len(data) == 0 {
		return nil, errUpload("that file is empty")
	}

	contentType := detectLogoType(data)
	if contentType == "" {
		return nil, errUpload("only PNG, JPEG, WebP and SVG images are accepted")
	}

	if contentType != "image/svg+xml" {
		config, _, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			return nil, errUpload("that file is not a readable image")
		}
		if config.Width > maxLogoPixels || config.Height > maxLogoPixels {
			return nil, errUpload(fmt.Sprintf(
				"the logo must be at most %dx%d; that one is %dx%d",
				maxLogoPixels, maxLogoPixels, config.Width, config.Height))
		}
	}

	return &dwpkv1alpha1.PlatformLogo{ContentType: contentType, Data: data}, nil
}

// detectLogoType decides the media type from the bytes.
//
// The browser's declared Content-Type is deliberately not consulted: it is
// whatever the client chose to send, and trusting it would let a file be stored
// and served back under a type it is not.
func detectLogoType(data []byte) string {
	sniffed := http.DetectContentType(data)
	switch {
	case strings.HasPrefix(sniffed, "image/png"):
		return "image/png"
	case strings.HasPrefix(sniffed, "image/jpeg"):
		return "image/jpeg"
	case strings.HasPrefix(sniffed, "image/webp"):
		return "image/webp"
	case strings.Contains(sniffed, "xml") || strings.HasPrefix(strings.TrimSpace(string(data)), "<svg"):
		// DetectContentType reports SVG as text/xml, so the root element is
		// what identifies it.
		if strings.Contains(string(data[:min(len(data), 1024)]), "<svg") {
			return "image/svg+xml"
		}
	}
	return ""
}

// handleLogo serves the platform logo, and is also the favicon.
//
// A route rather than a data: URI because the CSP is default-src 'self', which
// blocks data: as an image source. Unauthenticated: a favicon is fetched by the
// browser on the login page, before anyone has signed in.
func (s *Server) handleLogo(w http.ResponseWriter, r *http.Request) {
	config := s.platformConfig(r)
	if !config.HasLogo() {
		// No logo is not an error worth a page. The pages fall back to the
		// built-in mark, and a favicon request simply gets nothing.
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", config.Spec.Logo.ContentType)
	w.Header().Set("Cache-Control", "public, max-age=300")
	_, _ = w.Write(config.Spec.Logo.Data)
}

func errUpload(message string) error {
	return apiError{status: http.StatusBadRequest, message: message}
}
