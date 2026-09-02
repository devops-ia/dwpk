package ui

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	dwpkv1alpha1 "github.com/devops-ia/dwpk/api/v1alpha1"
	"github.com/devops-ia/dwpk/internal/auth"
	"github.com/devops-ia/dwpk/internal/workspace"
	"golang.org/x/oauth2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestLoginFlowSuccess(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC().Round(time.Second)
	sessions := auth.NewSessionStore(time.Hour)
	provider := &fakeProvider{
		token:  &oauth2.Token{AccessToken: "provider-token"},
		claims: auth.Claims{Email: "alice@example.com"},
	}
	resolver := &fakeOwnerResolver{userSpace: newUserSpace()}
	minter := &fakeTokenMinter{
		token:     "minted-token",
		expiresAt: now.Add(30 * time.Minute),
	}

	flow, err := NewLoginFlow(LoginFlowDeps{
		Registry:      fakeRegistry{providers: map[auth.Name]auth.Provider{auth.ProviderGitHub: provider}},
		Sessions:      sessions,
		TokenMinter:   minter,
		OwnerResolver: resolver,
		ChallengeTTL:  5 * time.Minute,
		Now: func() time.Time {
			return now
		},
	})
	if err != nil {
		t.Fatalf("NewLoginFlow() error = %v", err)
	}

	begin, err := flow.BeginLogin(auth.ProviderGitHub)
	if err != nil {
		t.Fatalf("BeginLogin() error = %v", err)
	}
	if begin.RedirectURL == "" {
		t.Fatal("BeginLogin() redirect URL = empty, want value")
	}
	if begin.Challenge.State == "" {
		t.Fatal("BeginLogin() state = empty, want value")
	}
	if begin.Challenge.Nonce == "" {
		t.Fatal("BeginLogin() nonce = empty, want value")
	}
	if !begin.Challenge.ExpiresAt.Equal(now.Add(5 * time.Minute)) {
		t.Fatalf("BeginLogin() expiry = %v, want %v", begin.Challenge.ExpiresAt, now.Add(5*time.Minute))
	}
	if !strings.Contains(begin.RedirectURL, begin.Challenge.State) {
		t.Fatalf("BeginLogin() redirect URL %q missing state %q", begin.RedirectURL, begin.Challenge.State)
	}
	if !strings.Contains(begin.RedirectURL, begin.Challenge.Nonce) {
		t.Fatalf("BeginLogin() redirect URL %q missing nonce %q", begin.RedirectURL, begin.Challenge.Nonce)
	}

	sessionID, err := flow.CompleteLogin(context.Background(), CompleteLoginRequest{
		Provider:  auth.ProviderGitHub,
		Code:      "auth-code",
		State:     begin.Challenge.State,
		Challenge: begin.Challenge,
	})
	if err != nil {
		t.Fatalf("CompleteLogin() error = %v", err)
	}
	if sessionID == "" {
		t.Fatal("CompleteLogin() session ID = empty, want value")
	}

	session, ok := sessions.Get(sessionID)
	if !ok {
		t.Fatal("SessionStore.Get() found = false, want true")
	}
	if session.Claims.Email != "alice@example.com" {
		t.Fatalf("session email = %q, want %q", session.Claims.Email, "alice@example.com")
	}

	// The provider owns this account's credentials, so the profile screen must
	// not offer to change a password - even if a local account of the same
	// name also exists.
	identity, err := flow.SessionIdentity(sessionID)
	if err != nil {
		t.Fatalf("SessionIdentity() error = %v", err)
	}
	if identity.LocalLogin {
		t.Fatal("SessionIdentity().LocalLogin = true after a provider login, want false")
	}

	token, err := flow.MintTokenForSession(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("MintTokenForSession() error = %v", err)
	}
	if token != "minted-token" {
		t.Fatalf("MintTokenForSession() token = %q, want %q", token, "minted-token")
	}
	if minter.namespace != "dwpk-alice" {
		t.Fatalf("MintTokenForSession() namespace = %q, want %q", minter.namespace, "dwpk-alice")
	}
	// A browser session must mint for the session account, never for the one
	// the user's workspace pod runs as.
	if minter.serviceAccount == workspace.ServiceAccountName {
		t.Fatal("MintTokenForSession() minted for the workspace pod ServiceAccount")
	}
	if minter.serviceAccount != workspace.SessionServiceAccountName {
		t.Fatalf("MintTokenForSession() service account = %q, want %q", minter.serviceAccount, workspace.SessionServiceAccountName)
	}

	refreshed, ok := sessions.Get(sessionID)
	if !ok {
		t.Fatal("SessionStore.Get() after mint found = false, want true")
	}
	if refreshed.Token != "minted-token" {
		t.Fatalf("session token = %q, want %q", refreshed.Token, "minted-token")
	}
	if !refreshed.TokenExpiresAt.Equal(now.Add(30 * time.Minute)) {
		t.Fatalf("session token expiry = %v, want %v", refreshed.TokenExpiresAt, now.Add(30*time.Minute))
	}
}

// A provider's group claim can grant admin access even when the
// admin-provisioned UserSpace itself carries no role, or the ordinary "user"
// role - this is the whole point of group mapping (§7.9).
func TestLoginFlowGroupClaimGrantsAdminRole(t *testing.T) {
	t.Parallel()

	sessions := auth.NewSessionStore(time.Hour)
	provider := &fakeProvider{
		token:  &oauth2.Token{AccessToken: "provider-token"},
		claims: auth.Claims{Email: "alice@example.com", Groups: []string{"engineering", "paas-admins"}},
	}
	resolver := &fakeOwnerResolver{userSpace: newUserSpace()}

	flow, err := NewLoginFlow(LoginFlowDeps{
		Registry:      fakeRegistry{providers: map[auth.Name]auth.Provider{auth.ProviderEntraID: provider}},
		Sessions:      sessions,
		TokenMinter:   &fakeTokenMinter{token: "minted-token", expiresAt: time.Now().Add(time.Hour)},
		OwnerResolver: resolver,
		GroupRoles: map[auth.Name]GroupRoleMapping{
			auth.ProviderEntraID: {AdminGroups: []string{"paas-admins"}, UserGroups: []string{"engineering"}},
		},
	})
	if err != nil {
		t.Fatalf("NewLoginFlow() error = %v", err)
	}

	begin, err := flow.BeginLogin(auth.ProviderEntraID)
	if err != nil {
		t.Fatalf("BeginLogin() error = %v", err)
	}
	sessionID, err := flow.CompleteLogin(context.Background(), CompleteLoginRequest{
		Provider:  auth.ProviderEntraID,
		Code:      "auth-code",
		State:     begin.Challenge.State,
		Challenge: begin.Challenge,
	})
	if err != nil {
		t.Fatalf("CompleteLogin() error = %v", err)
	}

	identity, err := flow.SessionIdentity(sessionID)
	if err != nil {
		t.Fatalf("SessionIdentity() error = %v", err)
	}
	if identity.Role != dwpkv1alpha1.UserSpaceRoleAdmin {
		t.Fatalf("role = %q, want %q (admin group takes priority over user group)", identity.Role, dwpkv1alpha1.UserSpaceRoleAdmin)
	}
}

// A group claim matching neither list leaves the UserSpace's own role in
// charge - group mapping only adds access, it never silently downgrades a
// role an admin set directly on the UserSpace.
func TestLoginFlowUnmatchedGroupsKeepUserSpaceRole(t *testing.T) {
	t.Parallel()

	sessions := auth.NewSessionStore(time.Hour)
	provider := &fakeProvider{
		token:  &oauth2.Token{AccessToken: "provider-token"},
		claims: auth.Claims{Email: "alice@example.com", Groups: []string{"marketing"}},
	}
	userSpace := newUserSpace()
	userSpace.Spec.Role = dwpkv1alpha1.UserSpaceRoleAdmin
	resolver := &fakeOwnerResolver{userSpace: userSpace}

	flow, err := NewLoginFlow(LoginFlowDeps{
		Registry:      fakeRegistry{providers: map[auth.Name]auth.Provider{auth.ProviderEntraID: provider}},
		Sessions:      sessions,
		TokenMinter:   &fakeTokenMinter{token: "minted-token", expiresAt: time.Now().Add(time.Hour)},
		OwnerResolver: resolver,
		GroupRoles: map[auth.Name]GroupRoleMapping{
			auth.ProviderEntraID: {AdminGroups: []string{"paas-admins"}, UserGroups: []string{"engineering"}},
		},
	})
	if err != nil {
		t.Fatalf("NewLoginFlow() error = %v", err)
	}

	begin, err := flow.BeginLogin(auth.ProviderEntraID)
	if err != nil {
		t.Fatalf("BeginLogin() error = %v", err)
	}
	sessionID, err := flow.CompleteLogin(context.Background(), CompleteLoginRequest{
		Provider:  auth.ProviderEntraID,
		Code:      "auth-code",
		State:     begin.Challenge.State,
		Challenge: begin.Challenge,
	})
	if err != nil {
		t.Fatalf("CompleteLogin() error = %v", err)
	}

	identity, err := flow.SessionIdentity(sessionID)
	if err != nil {
		t.Fatalf("SessionIdentity() error = %v", err)
	}
	if identity.Role != dwpkv1alpha1.UserSpaceRoleAdmin {
		t.Fatalf("role = %q, want the UserSpace's own %q role preserved", identity.Role, dwpkv1alpha1.UserSpaceRoleAdmin)
	}
}

func TestLoginFlowCompleteLoginRejectsInvalidState(t *testing.T) {
	t.Parallel()

	flow := newTestLoginFlow(t, loginFlowFixture{})
	begin, err := flow.BeginLogin(auth.ProviderGitHub)
	if err != nil {
		t.Fatalf("BeginLogin() error = %v", err)
	}

	_, err = flow.CompleteLogin(context.Background(), CompleteLoginRequest{
		Provider:  auth.ProviderGitHub,
		Code:      "auth-code",
		State:     "wrong-state",
		Challenge: begin.Challenge,
	})
	if !errors.Is(err, ErrStateMismatch) {
		t.Fatalf("CompleteLogin() error = %v, want ErrStateMismatch", err)
	}
}

func TestLoginFlowCompleteLoginRejectsUserSpaceResolutionFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want error
	}{
		{name: "no matching userspace", err: fmt.Errorf("resolve: %w", auth.ErrNoUserSpace), want: auth.ErrNoUserSpace},
		{name: "multiple matching userspaces", err: fmt.Errorf("resolve: %w", auth.ErrMultipleUserSpaces), want: auth.ErrMultipleUserSpaces},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			flow := newTestLoginFlow(t, loginFlowFixture{resolverErr: tt.err})
			begin, err := flow.BeginLogin(auth.ProviderGitHub)
			if err != nil {
				t.Fatalf("BeginLogin() error = %v", err)
			}

			_, err = flow.CompleteLogin(context.Background(), CompleteLoginRequest{
				Provider:  auth.ProviderGitHub,
				Code:      "auth-code",
				State:     begin.Challenge.State,
				Challenge: begin.Challenge,
			})
			if err == nil {
				t.Fatal("CompleteLogin() error = nil, want denial error")
			}

			var denied *UserSpaceAccessDeniedError
			if !errors.As(err, &denied) {
				t.Fatalf("CompleteLogin() error = %T, want *UserSpaceAccessDeniedError", err)
			}
			if !errors.Is(err, tt.want) {
				t.Fatalf("CompleteLogin() error = %v, want wrapped %v", err, tt.want)
			}
		})
	}
}

func TestLoginFlowRejectsUnknownOrExpiredSessions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		prepare func(t *testing.T) (*LoginFlow, string)
	}{
		{
			name: "unknown session",
			prepare: func(t *testing.T) (*LoginFlow, string) {
				t.Helper()
				return newTestLoginFlow(t, loginFlowFixture{}), "missing"
			},
		},
		{
			name: "expired session",
			prepare: func(t *testing.T) (*LoginFlow, string) {
				t.Helper()
				sessions := auth.NewSessionStore(time.Nanosecond)
				flow, err := NewLoginFlow(LoginFlowDeps{
					Registry:      fakeRegistry{providers: map[auth.Name]auth.Provider{auth.ProviderGitHub: &fakeProvider{token: &oauth2.Token{AccessToken: "provider-token"}, claims: auth.Claims{Email: "alice@example.com"}}}},
					Sessions:      sessions,
					TokenMinter:   &fakeTokenMinter{token: "minted-token", expiresAt: time.Now().Add(time.Minute)},
					OwnerResolver: &fakeOwnerResolver{userSpace: newUserSpace()},
				})
				if err != nil {
					t.Fatalf("NewLoginFlow() error = %v", err)
				}
				begin, err := flow.BeginLogin(auth.ProviderGitHub)
				if err != nil {
					t.Fatalf("BeginLogin() error = %v", err)
				}
				sessionID, err := flow.CompleteLogin(context.Background(), CompleteLoginRequest{
					Provider:  auth.ProviderGitHub,
					Code:      "auth-code",
					State:     begin.Challenge.State,
					Challenge: begin.Challenge,
				})
				if err != nil {
					t.Fatalf("CompleteLogin() error = %v", err)
				}
				time.Sleep(time.Millisecond)
				return flow, sessionID
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			flow, sessionID := tt.prepare(t)

			if _, err := flow.MintTokenForSession(context.Background(), sessionID); !errors.Is(err, ErrSessionNotFound) {
				t.Fatalf("MintTokenForSession() error = %v, want ErrSessionNotFound", err)
			}
			if err := flow.Logout(sessionID); !errors.Is(err, ErrSessionNotFound) {
				t.Fatalf("Logout() error = %v, want ErrSessionNotFound", err)
			}
		})
	}
}

func TestLoginFlowLogoutRemovesSession(t *testing.T) {
	t.Parallel()

	flow := newTestLoginFlow(t, loginFlowFixture{})
	begin, err := flow.BeginLogin(auth.ProviderGitHub)
	if err != nil {
		t.Fatalf("BeginLogin() error = %v", err)
	}
	sessionID, err := flow.CompleteLogin(context.Background(), CompleteLoginRequest{
		Provider:  auth.ProviderGitHub,
		Code:      "auth-code",
		State:     begin.Challenge.State,
		Challenge: begin.Challenge,
	})
	if err != nil {
		t.Fatalf("CompleteLogin() error = %v", err)
	}

	if err := flow.Logout(sessionID); err != nil {
		t.Fatalf("Logout() error = %v", err)
	}
	if _, ok := flow.sessions.Get(sessionID); ok {
		t.Fatal("SessionStore.Get() found session after logout, want false")
	}
	if _, err := flow.MintTokenForSession(context.Background(), sessionID); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("MintTokenForSession() error = %v, want ErrSessionNotFound", err)
	}
}

type loginFlowFixture struct {
	resolverErr error
}

func newTestLoginFlow(t *testing.T, fixture loginFlowFixture) *LoginFlow {
	t.Helper()

	flow, err := NewLoginFlow(LoginFlowDeps{
		Registry: fakeRegistry{providers: map[auth.Name]auth.Provider{auth.ProviderGitHub: &fakeProvider{
			token:  &oauth2.Token{AccessToken: "provider-token"},
			claims: auth.Claims{Email: "alice@example.com"},
		}}},
		Sessions:      auth.NewSessionStore(time.Hour),
		TokenMinter:   &fakeTokenMinter{token: "minted-token", expiresAt: time.Now().Add(time.Minute)},
		OwnerResolver: &fakeOwnerResolver{userSpace: newUserSpace(), err: fixture.resolverErr},
	})
	if err != nil {
		t.Fatalf("NewLoginFlow() error = %v", err)
	}

	return flow
}

type fakeRegistry struct {
	providers map[auth.Name]auth.Provider
}

func (r fakeRegistry) Get(name auth.Name) (auth.Provider, error) {
	provider, ok := r.providers[name]
	if !ok {
		return nil, fmt.Errorf("provider %q not configured", name)
	}

	return provider, nil
}

func (r fakeRegistry) Names() []auth.Name {
	names := make([]auth.Name, 0, len(r.providers))
	for name := range r.providers {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

type fakeProvider struct {
	token         *oauth2.Token
	claims        auth.Claims
	exchangeErr   error
	claimsErr     error
	exchangeCalls int
}

func (p *fakeProvider) AuthURL(state, nonce, _ string) string {
	return "https://provider.example/login?state=" + state + "&nonce=" + nonce
}

func (p *fakeProvider) Exchange(_ context.Context, code, _ string) (*oauth2.Token, error) {
	p.exchangeCalls++
	if strings.TrimSpace(code) == "" {
		return nil, errors.New("code must not be empty")
	}
	if p.exchangeErr != nil {
		return nil, p.exchangeErr
	}

	return p.token, nil
}

func (p *fakeProvider) Claims(_ context.Context, tok *oauth2.Token) (auth.Claims, error) {
	if tok == nil {
		return auth.Claims{}, errors.New("token must not be nil")
	}
	if p.claimsErr != nil {
		return auth.Claims{}, p.claimsErr
	}

	return p.claims, nil
}

type fakeOwnerResolver struct {
	userSpace *dwpkv1alpha1.UserSpace
	err       error
}

func (r *fakeOwnerResolver) ResolveByEmail(_ context.Context, email string) (*dwpkv1alpha1.UserSpace, error) {
	if strings.TrimSpace(email) == "" {
		return nil, errors.New("email must not be empty")
	}
	if r.err != nil {
		return nil, r.err
	}
	if r.userSpace == nil {
		return nil, errors.New("userspace missing")
	}

	return r.userSpace.DeepCopy(), nil
}

type fakeTokenMinter struct {
	token          string
	expiresAt      time.Time
	err            error
	namespace      string
	serviceAccount string
}

func (m *fakeTokenMinter) Mint(_ context.Context, namespace, serviceAccount string) (string, time.Time, error) {
	m.namespace = namespace
	m.serviceAccount = serviceAccount
	if m.err != nil {
		return "", time.Time{}, m.err
	}

	return m.token, m.expiresAt, nil
}

func TestLoginFlowSessionIdentityCarriesOnboardingCompletedFromUserSpace(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC().Round(time.Second)
	stamp := metav1.NewTime(now.Add(-time.Hour))
	userSpace := newUserSpace()
	userSpace.Spec.OnboardingCompletedAt = &stamp

	flow, err := NewLoginFlow(LoginFlowDeps{
		Registry:      fakeRegistry{providers: map[auth.Name]auth.Provider{auth.ProviderGitHub: &fakeProvider{token: &oauth2.Token{AccessToken: "provider-token"}, claims: auth.Claims{Email: "alice@example.com"}}}},
		Sessions:      auth.NewSessionStore(time.Hour),
		TokenMinter:   &fakeTokenMinter{token: "minted-token", expiresAt: now.Add(time.Hour)},
		OwnerResolver: &fakeOwnerResolver{userSpace: userSpace},
		ChallengeTTL:  5 * time.Minute,
		Now:           func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("NewLoginFlow() error = %v", err)
	}

	begin, err := flow.BeginLogin(auth.ProviderGitHub)
	if err != nil {
		t.Fatalf("BeginLogin() error = %v", err)
	}
	sessionID, err := flow.CompleteLogin(context.Background(), CompleteLoginRequest{
		Provider: auth.ProviderGitHub, Code: "auth-code", State: begin.Challenge.State, Challenge: begin.Challenge,
	})
	if err != nil {
		t.Fatalf("CompleteLogin() error = %v", err)
	}

	identity, err := flow.SessionIdentity(sessionID)
	if err != nil {
		t.Fatalf("SessionIdentity() error = %v", err)
	}
	if identity.OnboardingPending {
		t.Fatal("OnboardingPending = true, want false from a UserSpace with onboardingCompletedAt set")
	}
}

// A UserSpace with no onboardingCompletedAt at all - the common case for a
// freshly created account - must not be treated as onboarded.
func TestLoginFlowSessionIdentityDefaultsOnboardingIncomplete(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC().Round(time.Second)
	flow, err := NewLoginFlow(LoginFlowDeps{
		Registry:      fakeRegistry{providers: map[auth.Name]auth.Provider{auth.ProviderGitHub: &fakeProvider{token: &oauth2.Token{AccessToken: "provider-token"}, claims: auth.Claims{Email: "alice@example.com"}}}},
		Sessions:      auth.NewSessionStore(time.Hour),
		TokenMinter:   &fakeTokenMinter{token: "minted-token", expiresAt: now.Add(time.Hour)},
		OwnerResolver: &fakeOwnerResolver{userSpace: newUserSpace()},
		ChallengeTTL:  5 * time.Minute,
		Now:           func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("NewLoginFlow() error = %v", err)
	}

	begin, err := flow.BeginLogin(auth.ProviderGitHub)
	if err != nil {
		t.Fatalf("BeginLogin() error = %v", err)
	}
	sessionID, err := flow.CompleteLogin(context.Background(), CompleteLoginRequest{
		Provider: auth.ProviderGitHub, Code: "auth-code", State: begin.Challenge.State, Challenge: begin.Challenge,
	})
	if err != nil {
		t.Fatalf("CompleteLogin() error = %v", err)
	}

	identity, err := flow.SessionIdentity(sessionID)
	if err != nil {
		t.Fatalf("SessionIdentity() error = %v", err)
	}
	if !identity.OnboardingPending {
		t.Fatal("OnboardingPending = false, want true for a fresh UserSpace")
	}

	// MarkOnboardingCompleted flips the cached identity without a re-login.
	flow.MarkOnboardingCompleted(sessionID)
	identity, err = flow.SessionIdentity(sessionID)
	if err != nil {
		t.Fatalf("SessionIdentity() after mark error = %v", err)
	}
	if identity.OnboardingPending {
		t.Fatal("OnboardingPending = true after MarkOnboardingCompleted, want false")
	}
}

func newUserSpace() *dwpkv1alpha1.UserSpace {
	return &dwpkv1alpha1.UserSpace{
		ObjectMeta: metav1.ObjectMeta{Name: "alice"},
		Spec:       dwpkv1alpha1.UserSpaceSpec{Owner: "alice@example.com"},
		Status:     dwpkv1alpha1.UserSpaceStatus{Namespace: "dwpk-alice"},
	}
}

type fakeLocalUserVerifier struct {
	user auth.LocalUser
	err  error
}

func (v *fakeLocalUserVerifier) Verify(_ context.Context, username, password string) (auth.LocalUser, error) {
	if v.err != nil {
		return auth.LocalUser{}, v.err
	}
	return v.user, nil
}

func TestLoginFlowLocalAuthDisabledByDefault(t *testing.T) {
	t.Parallel()

	flow, err := NewLoginFlow(LoginFlowDeps{
		Registry:      fakeRegistry{providers: map[auth.Name]auth.Provider{}},
		Sessions:      auth.NewSessionStore(time.Hour),
		TokenMinter:   &fakeTokenMinter{},
		OwnerResolver: &fakeOwnerResolver{},
	})
	if err != nil {
		t.Fatalf("NewLoginFlow() error = %v", err)
	}

	if flow.LocalAuthEnabled() {
		t.Fatal("LocalAuthEnabled() = true without LocalUsers configured, want false")
	}
	if _, err := flow.CompleteLocalLogin(context.Background(), LocalLoginRequest{Username: "alice", Password: "s3cret!"}); !errors.Is(err, errLocalLoginDisabled) {
		t.Fatalf("CompleteLocalLogin() error = %v, want errLocalLoginDisabled", err)
	}
}

func TestLoginFlowCompleteLocalLoginSuccess(t *testing.T) {
	t.Parallel()

	sessions := auth.NewSessionStore(time.Hour)
	resolver := &fakeOwnerResolver{userSpace: newUserSpace()}
	verifier := &fakeLocalUserVerifier{user: auth.LocalUser{Username: "alice", Owner: "alice@example.com"}}

	flow, err := NewLoginFlow(LoginFlowDeps{
		Registry:      fakeRegistry{providers: map[auth.Name]auth.Provider{}},
		Sessions:      sessions,
		TokenMinter:   &fakeTokenMinter{},
		OwnerResolver: resolver,
		LocalUsers:    verifier,
	})
	if err != nil {
		t.Fatalf("NewLoginFlow() error = %v", err)
	}

	if !flow.LocalAuthEnabled() {
		t.Fatal("LocalAuthEnabled() = false with LocalUsers configured, want true")
	}

	sessionID, err := flow.CompleteLocalLogin(context.Background(), LocalLoginRequest{Username: "alice", Password: "s3cret!"})
	if err != nil {
		t.Fatalf("CompleteLocalLogin() error = %v", err)
	}
	if sessionID == "" {
		t.Fatal("CompleteLocalLogin() session ID = empty, want value")
	}

	session, ok := sessions.Get(sessionID)
	if !ok {
		t.Fatal("SessionStore.Get() found = false, want true")
	}
	if session.Claims.Email != "alice@example.com" {
		t.Fatalf("session email = %q, want %q", session.Claims.Email, "alice@example.com")
	}

	identity, err := flow.SessionIdentity(sessionID)
	if err != nil {
		t.Fatalf("SessionIdentity() error = %v", err)
	}
	if identity.UserSpaceNamespace != "dwpk-alice" {
		t.Fatalf("SessionIdentity().UserSpaceNamespace = %q, want dwpk-alice", identity.UserSpaceNamespace)
	}
	// The profile screen reads this to decide whether a password can be
	// changed there, so a local login has to say so.
	if !identity.LocalLogin {
		t.Fatal("SessionIdentity().LocalLogin = false, want true after a local login")
	}
}

func TestLoginFlowCompleteLocalLoginInvalidCredentials(t *testing.T) {
	t.Parallel()

	flow, err := NewLoginFlow(LoginFlowDeps{
		Registry:      fakeRegistry{providers: map[auth.Name]auth.Provider{}},
		Sessions:      auth.NewSessionStore(time.Hour),
		TokenMinter:   &fakeTokenMinter{},
		OwnerResolver: &fakeOwnerResolver{},
		LocalUsers:    &fakeLocalUserVerifier{err: auth.ErrInvalidCredentials},
	})
	if err != nil {
		t.Fatalf("NewLoginFlow() error = %v", err)
	}

	if _, err := flow.CompleteLocalLogin(context.Background(), LocalLoginRequest{Username: "alice", Password: "wrong-password"}); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("CompleteLocalLogin() error = %v, want auth.ErrInvalidCredentials", err)
	}
}
