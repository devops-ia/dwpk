package ui

import (
	"context"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"time"

	"github.com/a-h/templ"
	"github.com/devops-ia/dwpk/internal/auth"
)

type SessionAuthenticator interface {
	BeginLogin(provider auth.Name) (BeginLoginResult, error)
	CompleteLogin(ctx context.Context, req CompleteLoginRequest) (string, error)
	CompleteLocalLogin(ctx context.Context, req LocalLoginRequest) (string, error)
	LocalAuthEnabled() bool
	ConfiguredProviders() []auth.Name
	MintTokenForSession(ctx context.Context, sessionID string) (string, error)
	Logout(sessionID string) error
	SessionIdentity(sessionID string) (SessionIdentity, error)
	MarkOnboardingCompleted(sessionID string)
}

// APITokenStore is the subset of auth.TokenStore the REST API needs, kept
// as an interface here so the UI package does not depend on how tokens are
// persisted.
type APITokenStore interface {
	Issue(ctx context.Context, grant auth.TokenGrant) (auth.TokenRecord, error)
	Lookup(ctx context.Context, plaintext string) (auth.TokenRecord, error)
	List(ctx context.Context, kind auth.TokenKind, subjectNamespace string) ([]auth.TokenRecord, error)
	Revoke(ctx context.Context, secretName string) error
}

// LocalUserAdmin is the management half of the local user store, used only
// by the admin endpoints and screen. Nil disables both.
type LocalUserAdmin interface {
	Create(ctx context.Context, username, plaintextPassword, owner string) (auth.LocalUser, error)
	List(ctx context.Context) ([]auth.LocalUser, error)
	Delete(ctx context.Context, secretName string) error
	// FindByOwner and SetPassword serve the profile screen, where a person acts
	// on their own credential rather than on someone else's.
	FindByOwner(ctx context.Context, owner string) ([]auth.LocalUser, error)
	SetPassword(ctx context.Context, username, currentPassword, newPassword string) error
	// ResetPassword sets a password without the old one. Reaching it requires
	// having redeemed a reset token, which is the authorisation.
	ResetPassword(ctx context.Context, username, newPassword string) error
}

// PasswordResets issues and redeems the one-time links an administrator hands
// to someone who cannot sign in. Nil disables the feature, which is what
// happens when local auth is off — there is no password to reset.
type PasswordResets interface {
	Issue(ctx context.Context, username string) (string, time.Time, error)
	Redeem(ctx context.Context, token string) (string, error)
}

type ServerConfig struct {
	LoginFlow      SessionAuthenticator
	ClientFactory  APIClientFactory
	GatewayHost    string
	ListenAddress  string
	BasePath       string
	SessionTTL     time.Duration
	CookieSecure   bool
	IconHTTPClient *http.Client
	// APITokens and TokenMinter together enable Bearer dwpk_ token auth on
	// /api/v1. Leaving either nil restricts the API to cookie sessions.
	APITokens   APITokenStore
	TokenMinter ServiceAccountTokenMinter
	// LocalUsers enables the local user admin endpoints and screen.
	LocalUsers LocalUserAdmin
	// PlatformReader reads the platform settings with the UI's own credential,
	// which the login page needs before there is a session. Nil leaves every
	// screen on the built-in defaults.
	PlatformReader PlatformReader
	// PasswordResets enables the reset-link flow. Meaningless without
	// LocalUsers, since only a local login has a password here.
	PasswordResets PasswordResets
	// Runtime is what this process was started with, shown read-only on the
	// Global settings screen. It holds no secret; see RuntimeSettings.
	Runtime RuntimeSettings
}

type Server struct {
	loginFlow      SessionAuthenticator
	clientFactory  APIClientFactory
	gatewayHost    string
	listenAddress  string
	basePath       string
	sessionTTL     time.Duration
	cookieSecure   bool
	challengeStore *ChallengeStore
	csrfStore      *CSRFStore
	iconHTTPClient *http.Client
	apiTokens      APITokenStore
	tokenMinter    ServiceAccountTokenMinter
	localUsers     LocalUserAdmin
	passwordResets PasswordResets
	platformReader PlatformReader
	platform       platformCache
	runtime        RuntimeSettings
	now            func() time.Time
	handler        http.Handler
}

func NewServer(cfg ServerConfig) (*Server, error) {
	if cfg.LoginFlow == nil {
		return nil, fmt.Errorf("login flow required")
	}
	if cfg.ClientFactory == nil {
		return nil, fmt.Errorf("client factory required")
	}
	if cfg.ListenAddress == "" {
		cfg.ListenAddress = ":8080"
	}
	if cfg.SessionTTL <= 0 {
		cfg.SessionTTL = 15 * time.Minute
	}
	if cfg.IconHTTPClient == nil {
		cfg.IconHTTPClient = http.DefaultClient
	}

	server := &Server{
		loginFlow:      cfg.LoginFlow,
		clientFactory:  cfg.ClientFactory,
		gatewayHost:    cfg.GatewayHost,
		listenAddress:  cfg.ListenAddress,
		basePath:       normalizeBasePath(cfg.BasePath),
		sessionTTL:     cfg.SessionTTL,
		cookieSecure:   cfg.CookieSecure,
		challengeStore: NewChallengeStore(time.Now),
		csrfStore:      NewCSRFStore(),
		iconHTTPClient: cfg.IconHTTPClient,
		apiTokens:      cfg.APITokens,
		tokenMinter:    cfg.TokenMinter,
		localUsers:     cfg.LocalUsers,
		passwordResets: cfg.PasswordResets,
		platformReader: cfg.PlatformReader,
		runtime:        cfg.Runtime,
		now:            time.Now,
	}
	server.handler = server.routes()
	return server, nil
}

func (s *Server) Handler() http.Handler {
	return s.handler
}

func (s *Server) ListenAndServe() error {
	return http.ListenAndServe(s.listenAddress, s.handler)
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	assets, err := fs.Sub(Assets, "assets")
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		})
	}
	mux.Handle("GET /assets/", http.StripPrefix("/assets/", http.FileServer(http.FS(assets))))
	mux.HandleFunc("GET /login", s.handleLoginPicker)
	mux.HandleFunc("GET /login/{provider}", s.handleBeginLogin)
	mux.HandleFunc("POST /login/local", s.handleLocalLogin)
	// Unauthenticated: a favicon is fetched on the login page.
	mux.HandleFunc("GET /logo", s.handleLogo)
	mux.HandleFunc("GET /reset/{token}", s.handleResetForm)
	mux.HandleFunc("POST /reset", s.handleResetSubmit)
	mux.HandleFunc("GET /callback/{provider}", s.handleCompleteLogin)
	mux.HandleFunc("POST "+apiVersion+"/login", s.handleAPILocalLogin)
	// Public, no login: read-only API documentation, not a control surface.
	mux.HandleFunc("GET /api/docs", s.handleAPIDocsPage)
	mux.HandleFunc("GET "+apiVersion+"/openapi.yaml", s.handleOpenAPISpec)
	mux.Handle(apiVersion+"/", s.apiRoutes())

	protected := http.NewServeMux()
	protected.HandleFunc("GET /", s.handleDashboard)
	protected.HandleFunc("GET "+catalogPath, s.handleCatalog)
	protected.HandleFunc("GET /workspace-images/{name}/icon", s.handleWorkspaceImageIcon)
	protected.HandleFunc("GET /new", s.handleNewWorkspaceForm)
	protected.HandleFunc("POST /new", s.handleCreateWorkspace)
	// The YAML preview. A POST because it carries the whole form, and a
	// fragment because the form must not be re-rendered under the cursor.
	protected.HandleFunc("POST /new/preview", s.handleWorkspacePreview)
	protected.HandleFunc("GET /w/{name}", s.handleWorkspacePage)
	protected.HandleFunc("GET /w/{name}/status", s.handleWorkspaceStatus)
	protected.HandleFunc("POST /w/{name}/start", s.handleStartWorkspace)
	protected.HandleFunc("POST /w/{name}/stop", s.handleStopWorkspace)
	protected.HandleFunc("POST /w/{name}/resources", s.handleUpdateWorkspaceResources)
	protected.HandleFunc("POST /w/{name}/delete", s.handleDeleteWorkspace)
	protected.HandleFunc("GET /w/{name}/events", s.handleWorkspaceEvents)
	protected.HandleFunc("GET /w/{name}/logs", s.handleWorkspaceLogs)
	protected.HandleFunc("GET /w/{name}/terminal", s.handleTerminalWindow)
	protected.HandleFunc("GET /w/{name}/terminal/ws", s.handleTerminalWebSocket)
	protected.HandleFunc("GET /admin/overview", s.handleAdminOverview)
	protected.HandleFunc("GET /admin/settings", s.handleAdminSettings)
	protected.HandleFunc("POST /admin/settings", s.handleAdminUpdateSettings)
	protected.HandleFunc("GET /admin/quota", s.handleAdminQuota)
	// The UserSpaces screen merged into /admin/users. Without this the pattern
	// falls through to "GET /" and a bookmarked link quietly shows the catalog.
	protected.HandleFunc("GET /admin/userspaces", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, s.path("/admin/users"), http.StatusMovedPermanently)
	})
	protected.HandleFunc("POST /admin/userspaces", s.handleAdminCreateUserSpace)
	protected.HandleFunc("POST /admin/userspaces/{name}/membership", s.handleAdminUpdateMembership)
	protected.HandleFunc("POST /admin/userspaces/{name}/delete", s.handleAdminDeleteUserSpace)
	protected.HandleFunc("POST /admin/quota/{name}", s.handleAdminUpdateQuota)
	protected.HandleFunc("GET /admin/workspaces", s.handleAdminWorkspaces)
	protected.HandleFunc("POST /admin/workspaces", s.handleAdminCreateWorkspace)
	protected.HandleFunc("POST /admin/workspaces/{namespace}/{name}/start", s.handleAdminStartWorkspace)
	protected.HandleFunc("POST /admin/workspaces/{namespace}/{name}/stop", s.handleAdminStopWorkspace)
	protected.HandleFunc("POST /admin/workspaces/{namespace}/{name}/delete", s.handleAdminDeleteWorkspace)
	protected.HandleFunc("GET "+catalogAdminPath, s.handleAdminCatalog)
	protected.HandleFunc("POST /admin/catalog", s.handleAdminCreateWorkspaceImage)
	protected.HandleFunc("POST /admin/catalog/{name}/update", s.handleAdminUpdateWorkspaceImage)
	protected.HandleFunc("POST /admin/catalog/{name}/delete", s.handleAdminDeleteWorkspaceImage)
	protected.HandleFunc("GET "+registriesAdminPath, s.handleAdminRegistries)
	protected.HandleFunc("POST /admin/registries", s.handleAdminCreateImageRegistry)
	protected.HandleFunc("POST /admin/registries/{name}/update", s.handleAdminUpdateImageRegistry)
	protected.HandleFunc("POST /admin/registries/{name}/delete", s.handleAdminDeleteImageRegistry)
	protected.HandleFunc("POST /admin/registries/{name}/force-sync", s.handleAdminForceSyncImageRegistry)
	protected.HandleFunc("GET /admin/users", s.handleAdminPeople)
	protected.HandleFunc("POST /admin/users", s.handleAdminCreateLocalUser)
	protected.HandleFunc("POST /admin/users/{name}/delete", s.handleAdminDeleteLocalUser)
	protected.HandleFunc("POST /admin/users/{name}/reset", s.handleAdminIssueReset)
	protected.HandleFunc("GET /profile", s.handleProfile)
	protected.HandleFunc("POST /profile/password", s.handleChangePassword)
	protected.HandleFunc("POST /profile/keys", s.handleProfileAddKey)
	protected.HandleFunc("POST /profile/keys/delete", s.handleProfileRemoveKey)
	protected.HandleFunc("POST /profile/git-ssh-keys", s.handleProfileAddGitSSHKey)
	protected.HandleFunc("POST /profile/git-ssh-keys/delete", s.handleProfileRemoveGitSSHKey)
	protected.HandleFunc("POST /profile/tokens", s.handleProfileIssueToken)
	protected.HandleFunc("POST /profile/tokens/{name}/delete", s.handleProfileRevokeToken)
	protected.HandleFunc("POST /logout", s.handleLogout)
	protected.HandleFunc("GET "+onboardingPath, s.handleOnboarding)
	protected.HandleFunc("POST "+onboardingCompletePath, s.handleOnboardingComplete)

	mux.Handle("/", s.withSecurityHeaders(s.withSession(protected)))
	root := s.withSecurityHeaders(mux)
	if s.basePath == "" {
		return root
	}
	return http.StripPrefix(s.basePath, root)
}

// path prefixes an in-app absolute path with the configured base path, for
// use in server-issued redirects (which the browser resolves against the
// externally visible URL, always including the base path).
func (s *Server) path(p string) string {
	return s.basePath + p
}

// cookiePath returns the Path attribute for cookies this server issues,
// scoped to the base path so cookies aren't sent to unrelated apps sharing
// the same host outside the base path.
func (s *Server) cookiePath() string {
	if s.basePath == "" {
		return "/"
	}
	return s.basePath
}

func (s *Server) renderAnonymousPage(w http.ResponseWriter, r *http.Request, status int, title string, content templ.Component) {
	s.renderWithStatus(w, r, status, Layout(PageShell{Title: title}, RequestSession{}, content))
}

// renderShell renders any authenticated page whose shell is not the default —
// today that is the pop-out terminal, which is bare and needs xterm.
func (s *Server) renderShell(w http.ResponseWriter, r *http.Request, session RequestSession, shell PageShell, content templ.Component) {
	s.renderWithStatus(w, r, http.StatusOK, Layout(shell, session, content))
}

func (s *Server) renderAuthedPage(w http.ResponseWriter, r *http.Request, status int, session RequestSession, title string, content templ.Component) {
	s.renderWithStatus(w, r, status, Layout(PageShell{Title: title, Authenticated: true}, session, content))
}

// redirectBack returns a non-htmx form POST to the page it came from, so an
// action taken from a card on one screen does not navigate away from it. The
// Referer is trusted only for its path, and only when it points at this app;
// anything else falls back to a page we know exists.
func (s *Server) redirectBack(w http.ResponseWriter, r *http.Request, fallback string) {
	target := s.path(fallback)
	if referer, err := url.Parse(r.Referer()); err == nil && referer.Path != "" {
		if referer.Host == "" || referer.Host == r.Host {
			target = referer.Path
			if referer.RawQuery != "" {
				target += "?" + referer.RawQuery
			}
		}
	}
	// 303 so the browser follows with GET; a 302 after POST is re-POSTed by
	// some clients on reload.
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// renderFragment renders an htmx partial: no layout, and the request context so
// the base path and theme survive into the swapped markup.
func (s *Server) renderFragment(w http.ResponseWriter, r *http.Request, component templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = component.Render(s.renderContext(r), w)
}

// renderWithStatus renders a component with the request-derived context. It
// takes the request rather than using context.Background() because templates
// need the base path and the theme, and the htmx fragment endpoints need the
// same two — rendering a fragment from a bare request context used to drop the
// base path and emit unprefixed links.
func (s *Server) renderWithStatus(w http.ResponseWriter, r *http.Request, status int, component templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_ = component.Render(s.renderContext(r), w)
}
