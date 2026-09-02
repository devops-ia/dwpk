package ui

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	dwpkv1alpha1 "github.com/devops-ia/dwpk/api/v1alpha1"
	"github.com/devops-ia/dwpk/internal/auth"
	"github.com/devops-ia/dwpk/internal/workspace"
	"golang.org/x/oauth2"
)

const defaultChallengeTTL = 5 * time.Minute

var (
	ErrStateMismatch         = errors.New("login state mismatch")
	ErrLoginChallengeExpired = errors.New("login challenge expired")
	ErrSessionNotFound       = errors.New("session not found")
	errRegistryRequired      = errors.New("provider registry required")
	errSessionsRequired      = errors.New("session store required")
	errTokenMinterRequired   = errors.New("token minter required")
	errOwnerResolverRequired = errors.New("owner resolver required")
	errProviderRequired      = errors.New("provider name required")
	errCodeRequired          = errors.New("authorization code required")
	errStateRequired         = errors.New("login state required")
	errUserSpaceNamespace    = errors.New("userspace namespace required")
	errLocalLoginDisabled    = errors.New("local login is not enabled")
	// ErrUserSpaceDisabled is returned when an administrator has suspended the
	// account. It is distinct from "no UserSpace" so the reason is loggable,
	// though what the user sees stays the same either way.
	ErrUserSpaceDisabled = errors.New("this account is disabled")
)

// ProviderRegistry looks up configured OAuth providers by stable name and
// reports which ones exist, so the login page can offer only those.
type ProviderRegistry interface {
	Get(name auth.Name) (auth.Provider, error)
	Names() []auth.Name
}

// OwnerResolver maps a verified login email to the admin-provisioned UserSpace.
type OwnerResolver interface {
	ResolveByEmail(ctx context.Context, email string) (*dwpkv1alpha1.UserSpace, error)
}

// ServiceAccountTokenMinter mints the per-request Kubernetes credential.
type ServiceAccountTokenMinter interface {
	Mint(ctx context.Context, namespace, serviceAccountName string) (string, time.Time, error)
}

// LocalUserVerifier checks a username/password pair against stored local
// user credentials (§7.8). It is optional: a nil verifier means local login
// is disabled and only OAuth2 providers are offered.
type LocalUserVerifier interface {
	Verify(ctx context.Context, username, password string) (auth.LocalUser, error)
}

// GroupRoleMapping decides a session's role from an OAuth2 provider's group
// claim, so an identity provider's own group management can stand in for
// admin-provisioned per-UserSpace roles (§7.9). Admin groups are checked
// first, then user groups; a match in neither leaves the UserSpace's own
// `spec.role` in charge, so group mapping is additive, never a downgrade an
// admin didn't ask for.
type GroupRoleMapping struct {
	AdminGroups []string
	UserGroups  []string
}

// roleFor reports the mapped role for the given claim groups, and whether any
// mapping matched at all.
func (m GroupRoleMapping) roleFor(groups []string) (string, bool) {
	if anyGroupMatches(groups, m.AdminGroups) {
		return dwpkv1alpha1.UserSpaceRoleAdmin, true
	}
	if anyGroupMatches(groups, m.UserGroups) {
		return dwpkv1alpha1.UserSpaceRoleUser, true
	}
	return "", false
}

func anyGroupMatches(groups, configured []string) bool {
	for _, want := range configured {
		for _, have := range groups {
			if want == have {
				return true
			}
		}
	}
	return false
}

// LoginFlowDeps groups constructor dependencies so the API stays readable.
type LoginFlowDeps struct {
	Registry      ProviderRegistry
	Sessions      *auth.SessionStore
	TokenMinter   ServiceAccountTokenMinter
	OwnerResolver OwnerResolver
	// LocalUsers enables username/password login alongside OAuth2 when set
	// (§7.8). Leave nil to disable local login entirely.
	LocalUsers LocalUserVerifier
	// GroupRoles maps a configured provider to the group names that grant
	// admin or user access (§7.9). A provider absent from this map, or a
	// login whose groups match neither list, keeps the UserSpace's own role
	// untouched.
	GroupRoles   map[auth.Name]GroupRoleMapping
	ChallengeTTL time.Duration
	Now          func() time.Time
}

// BeginLoginResult gives the future HTTP layer both redirect target and state to persist.
type BeginLoginResult struct {
	RedirectURL string
	Challenge   LoginChallenge
}

// LoginChallenge is the short-lived state a callback must present back unchanged.
type LoginChallenge struct {
	State     string
	Nonce     string
	ExpiresAt time.Time
	// CodeVerifier is PKCE's proof of possession (RFC 7636): generated fresh
	// per login attempt here, sent to the provider only as its S256 hash in
	// AuthURL, and presented in the clear only once, in Exchange, by the same
	// party that started this login.
	CodeVerifier string
}

// CompleteLoginRequest carries callback inputs without a long parameter list.
type CompleteLoginRequest struct {
	Provider  auth.Name
	Code      string
	State     string
	Challenge LoginChallenge
	// SessionTTL is the idle window the session gets. Zero means the store's
	// default, which is what an unticked "remember me" asks for.
	SessionTTL time.Duration
}

// LocalLoginRequest carries a username/password attempt. It is a struct rather
// than three parameters so the remember-me window can ride along.
type LocalLoginRequest struct {
	Username   string
	Password   string
	SessionTTL time.Duration
}

// SessionIdentity is the resolved Kubernetes identity behind one UI session.
type SessionIdentity struct {
	Email              string
	UserSpaceName      string
	UserSpaceNamespace string
	Role               string
	// OnboardingPending mirrors the absence of UserSpaceSpec.OnboardingCompletedAt as of
	// login (or the last onboarding-complete action, which refreshes the
	// cached session identity directly rather than waiting for a re-login).
	// The first-login wizard redirect in withSession reads this instead of
	// fetching the UserSpace on every request. Zero value (false) means "not
	// pending" so identities built without this field (tests predating the
	// wizard, internal bookkeeping) are never redirected by surprise.
	OnboardingPending bool
	// LocalLogin records that this session authenticated with a username and
	// password rather than through an identity provider. Only a local login
	// may change a password here: an external provider owns its own
	// credentials, and one account can hold both, so "does a local user exist
	// for this email" is a different question from "how did this person sign
	// in". The zero value is the safe branch - an identity built without
	// setting it is treated as an external login and shown no password form.
	LocalLogin bool
}

// UserSpaceAccessDeniedError keeps provisioning problems distinct from backend failures.
type UserSpaceAccessDeniedError struct {
	Email string
	Err   error
}

func (e *UserSpaceAccessDeniedError) Error() string {
	return fmt.Sprintf("login denied for %q: %v", e.Email, e.Err)
}

func (e *UserSpaceAccessDeniedError) Unwrap() error {
	if e == nil {
		return nil
	}

	return e.Err
}

// NonceValidatingProvider is an optional extension for providers that can prove
// the returned token matches the original login nonce.
type NonceValidatingProvider interface {
	ValidateNonce(ctx context.Context, tok *oauth2.Token, expectedNonce string) error
}

// LoginFlow wires OAuth login to UserSpace resolution and per-request token minting.
type LoginFlow struct {
	registry      ProviderRegistry
	sessions      *auth.SessionStore
	tokenMinter   ServiceAccountTokenMinter
	ownerResolver OwnerResolver
	localUsers    LocalUserVerifier
	groupRoles    map[auth.Name]GroupRoleMapping
	challengeTTL  time.Duration
	now           func() time.Time

	mu         sync.Mutex
	identities map[string]SessionIdentity
}

func NewLoginFlow(deps LoginFlowDeps) (*LoginFlow, error) {
	if deps.Registry == nil {
		return nil, errRegistryRequired
	}
	if deps.Sessions == nil {
		return nil, errSessionsRequired
	}
	if deps.TokenMinter == nil {
		return nil, errTokenMinterRequired
	}
	if deps.OwnerResolver == nil {
		return nil, errOwnerResolverRequired
	}
	if deps.ChallengeTTL <= 0 {
		deps.ChallengeTTL = defaultChallengeTTL
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}

	return &LoginFlow{
		registry:      deps.Registry,
		sessions:      deps.Sessions,
		tokenMinter:   deps.TokenMinter,
		ownerResolver: deps.OwnerResolver,
		localUsers:    deps.LocalUsers,
		groupRoles:    deps.GroupRoles,
		challengeTTL:  deps.ChallengeTTL,
		now:           deps.Now,
		identities:    make(map[string]SessionIdentity),
	}, nil
}

func (f *LoginFlow) BeginLogin(provider auth.Name) (BeginLoginResult, error) {
	resolvedProvider, err := f.provider(provider)
	if err != nil {
		return BeginLoginResult{}, err
	}

	challenge, err := f.newChallenge()
	if err != nil {
		return BeginLoginResult{}, fmt.Errorf("begin login for %q: %w", provider, err)
	}

	return BeginLoginResult{
		RedirectURL: resolvedProvider.AuthURL(challenge.State, challenge.Nonce, challenge.CodeVerifier),
		Challenge:   challenge,
	}, nil
}

func (f *LoginFlow) CompleteLogin(ctx context.Context, req CompleteLoginRequest) (string, error) {
	provider, err := f.provider(req.Provider)
	if err != nil {
		return "", err
	}
	if err := validateCompleteLoginRequest(f.now(), req); err != nil {
		return "", fmt.Errorf("complete login for %q: %w", req.Provider, err)
	}

	tok, err := provider.Exchange(ctx, req.Code, req.Challenge.CodeVerifier)
	if err != nil {
		return "", fmt.Errorf("complete login for %q: exchange code: %w", req.Provider, err)
	}
	if err := f.validateNonce(ctx, provider, tok, req.Challenge.Nonce); err != nil {
		return "", fmt.Errorf("complete login for %q: validate nonce: %w", req.Provider, err)
	}

	claims, err := provider.Claims(ctx, tok)
	if err != nil {
		return "", fmt.Errorf("complete login for %q: read claims: %w", req.Provider, err)
	}

	userSpace, err := f.ownerResolver.ResolveByEmail(ctx, claims.Email)
	if err != nil {
		return "", wrapOwnerResolutionError(claims.Email, err)
	}

	roleOverride, _ := f.groupRoles[req.Provider].roleFor(claims.Groups)

	identity, err := sessionIdentityFrom(userSpace, claims, roleOverride)
	if err != nil {
		return "", fmt.Errorf("complete login for %q: %w", req.Provider, err)
	}

	sessionID, err := f.sessions.CreateWithTTL(claims, req.SessionTTL)
	if err != nil {
		return "", fmt.Errorf("complete login for %q: create session: %w", req.Provider, err)
	}

	f.storeIdentity(sessionID, identity)
	return sessionID, nil
}

// CompleteLocalLogin verifies a username/password pair against LocalUsers
// and creates a session that behaves like an OAuth2 login (§7.8), so the rest
// of the stack (RBAC, per-request tokens, session TTL) doesn't need to know
// which login method was used. The one exception is LocalLogin, which the
// profile screen reads to decide whether a password can be changed here at
// all.
func (f *LoginFlow) CompleteLocalLogin(ctx context.Context, req LocalLoginRequest) (string, error) {
	if f.localUsers == nil {
		return "", errLocalLoginDisabled
	}

	username := req.Username
	localUser, err := f.localUsers.Verify(ctx, username, req.Password)
	if err != nil {
		return "", fmt.Errorf("complete local login for %q: %w", username, err)
	}

	claims := auth.Claims{Email: localUser.Owner}
	userSpace, err := f.ownerResolver.ResolveByEmail(ctx, claims.Email)
	if err != nil {
		return "", wrapOwnerResolutionError(claims.Email, err)
	}

	identity, err := sessionIdentityFrom(userSpace, claims, "")
	if err != nil {
		return "", fmt.Errorf("complete local login for %q: %w", username, err)
	}

	identity.LocalLogin = true

	sessionID, err := f.sessions.CreateWithTTL(claims, req.SessionTTL)
	if err != nil {
		return "", fmt.Errorf("complete local login for %q: create session: %w", username, err)
	}

	f.storeIdentity(sessionID, identity)
	return sessionID, nil
}

// LocalAuthEnabled reports whether username/password login is configured, so
// the login page knows whether to render the local login form.
func (f *LoginFlow) LocalAuthEnabled() bool {
	return f.localUsers != nil
}

// ConfiguredProviders lists the OAuth2 providers an operator actually set up.
// The login page offers exactly these: showing a provider that was never
// configured only produces an error page when someone picks it.
func (f *LoginFlow) ConfiguredProviders() []auth.Name {
	return f.registry.Names()
}

func (f *LoginFlow) MintTokenForSession(ctx context.Context, sessionID string) (string, error) {
	identity, err := f.sessionIdentity(sessionID)
	if err != nil {
		return "", err
	}

	// The session ServiceAccount, never the workspace one: a browser session
	// must not share an identity with the user's container.
	token, expiresAt, err := f.tokenMinter.Mint(ctx, identity.UserSpaceNamespace, workspace.SessionServiceAccountName)
	if err != nil {
		return "", fmt.Errorf("mint token for session %q: %w", sessionID, err)
	}
	if err := f.sessions.Refresh(sessionID, token, expiresAt); err != nil {
		f.deleteIdentity(sessionID)
		return "", fmt.Errorf("mint token for session %q: refresh session: %w", sessionID, err)
	}

	return token, nil
}

// Logout revokes the browser session only; workspace lifecycle stays on spec.running.
func (f *LoginFlow) Logout(sessionID string) error {
	if _, err := f.sessionIdentity(sessionID); err != nil {
		return err
	}

	f.sessions.Delete(sessionID)
	f.deleteIdentity(sessionID)
	return nil
}

func (f *LoginFlow) SessionIdentity(sessionID string) (SessionIdentity, error) {
	return f.sessionIdentity(sessionID)
}

func (f *LoginFlow) provider(name auth.Name) (auth.Provider, error) {
	if !name.Valid() {
		return nil, fmt.Errorf("provider lookup: %w", errProviderRequired)
	}

	provider, err := f.registry.Get(name)
	if err != nil {
		return nil, fmt.Errorf("provider lookup for %q: %w", name, err)
	}

	return provider, nil
}

func (f *LoginFlow) newChallenge() (LoginChallenge, error) {
	state, err := auth.GenerateSessionID()
	if err != nil {
		return LoginChallenge{}, fmt.Errorf("generate state: %w", err)
	}
	nonce, err := auth.GenerateSessionID()
	if err != nil {
		return LoginChallenge{}, fmt.Errorf("generate nonce: %w", err)
	}

	return LoginChallenge{
		State:        state,
		Nonce:        nonce,
		ExpiresAt:    f.now().Add(f.challengeTTL),
		CodeVerifier: oauth2.GenerateVerifier(),
	}, nil
}

func (f *LoginFlow) validateNonce(ctx context.Context, provider auth.Provider, tok *oauth2.Token, nonce string) error {
	validator, ok := provider.(NonceValidatingProvider)
	if !ok {
		return nil
	}

	return validator.ValidateNonce(ctx, tok, nonce)
}

func (f *LoginFlow) storeIdentity(sessionID string, identity SessionIdentity) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.identities[sessionID] = identity
}

// MarkOnboardingCompleted flips the cached session identity's onboarding
// flag so the sidebar's "Get started" entry disappears on the very next
// request. Without this it would linger until the session expired and the
// person logged in again, since the identity cache is only populated at login.
func (f *LoginFlow) MarkOnboardingCompleted(sessionID string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	identity, ok := f.identities[sessionID]
	if !ok {
		return
	}
	identity.OnboardingPending = false
	f.identities[sessionID] = identity
}

func (f *LoginFlow) sessionIdentity(sessionID string) (SessionIdentity, error) {
	if _, ok := f.sessions.Get(sessionID); !ok {
		f.deleteIdentity(sessionID)
		return SessionIdentity{}, fmt.Errorf("session %q: %w", sessionID, ErrSessionNotFound)
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	identity, ok := f.identities[sessionID]
	if !ok {
		return SessionIdentity{}, fmt.Errorf("session %q: identity missing", sessionID)
	}

	return identity, nil
}

func (f *LoginFlow) deleteIdentity(sessionID string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	delete(f.identities, sessionID)
}

func validateCompleteLoginRequest(now time.Time, req CompleteLoginRequest) error {
	switch {
	case !req.Provider.Valid():
		return errProviderRequired
	case req.Code == "":
		return errCodeRequired
	case req.State == "":
		return errStateRequired
	case req.Challenge.ExpiresAt.IsZero() || !req.Challenge.ExpiresAt.After(now):
		return ErrLoginChallengeExpired
	case req.Challenge.State != req.State:
		return ErrStateMismatch
	default:
		return nil
	}
}

func wrapOwnerResolutionError(email string, err error) error {
	if errors.Is(err, auth.ErrNoUserSpace) || errors.Is(err, auth.ErrMultipleUserSpaces) {
		return &UserSpaceAccessDeniedError{Email: email, Err: err}
	}

	return fmt.Errorf("resolve UserSpace for %q: %w", email, err)
}

// sessionIdentityFrom resolves the session's role from the group-mapped
// override when one matched during login, falling back to the UserSpace's
// own spec.role otherwise (empty roleOverride means "no group mapping
// matched" — see GroupRoleMapping.roleFor).
func sessionIdentityFrom(userSpace *dwpkv1alpha1.UserSpace, claims auth.Claims, roleOverride string) (SessionIdentity, error) {
	if userSpace == nil {
		return SessionIdentity{}, errors.New("resolved userspace required")
	}
	if userSpace.Status.Namespace == "" {
		return SessionIdentity{}, errUserSpaceNamespace
	}
	// Checked here rather than in each caller so every login path — OAuth2 and
	// local alike — is covered by the one gate.
	if userSpace.Spec.Disabled {
		return SessionIdentity{}, &UserSpaceAccessDeniedError{Email: claims.Email, Err: ErrUserSpaceDisabled}
	}

	role := userSpace.Spec.EffectiveRole()
	if roleOverride != "" {
		role = roleOverride
	}

	return SessionIdentity{
		Email:              claims.Email,
		UserSpaceName:      userSpace.Name,
		UserSpaceNamespace: userSpace.Status.Namespace,
		Role:               role,
		OnboardingPending:  userSpace.Spec.OnboardingCompletedAt == nil,
	}, nil
}
