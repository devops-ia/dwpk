package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"golang.org/x/oauth2"
)

const (
	defaultGitHubAuthorizeURL = "https://github.com/login/oauth/authorize"
	defaultGitHubTokenURL     = "https://github.com/login/oauth/access_token"
	defaultGitHubAPIURL       = "https://api.github.com"
)

var (
	errGitHubClientIDRequired          = errors.New("client ID required")
	errGitHubSecretRequired            = errors.New("client secret required")
	errGitHubRedirectRequired          = errors.New("redirect URL required")
	errGitHubAccessTokenRequired       = errors.New("access token required")
	errGitHubPrimaryVerifiedEmailError = errors.New("primary verified email required")
)

type GitHubConfig struct {
	ClientID     string
	ClientSecret string
	RedirectURL  string
	AuthorizeURL string
	TokenURL     string
	APIURL       string
	Scopes       []string
	HTTPClient   *http.Client
}

type githubProvider struct {
	oauth2Config *oauth2.Config
	apiURL       string
	httpClient   *http.Client
}

type githubUser struct {
	Email string `json:"email"`
}

type githubEmail struct {
	Email    string `json:"email"`
	Primary  bool   `json:"primary"`
	Verified bool   `json:"verified"`
}

func NewGitHubProvider(cfg GitHubConfig) (Provider, error) {
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("build GitHub provider: %w", err)
	}

	return &githubProvider{
		oauth2Config: &oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			RedirectURL:  cfg.RedirectURL,
			Endpoint: oauth2.Endpoint{
				AuthURL:  firstNonEmpty(cfg.AuthorizeURL, defaultGitHubAuthorizeURL),
				TokenURL: firstNonEmpty(cfg.TokenURL, defaultGitHubTokenURL),
			},
			Scopes: githubScopes(cfg.Scopes),
		},
		apiURL:     strings.TrimRight(firstNonEmpty(cfg.APIURL, defaultGitHubAPIURL), "/"),
		httpClient: cfg.HTTPClient,
	}, nil
}

func (p *githubProvider) AuthURL(state, _, _ string) string {
	return p.oauth2Config.AuthCodeURL(state)
}

func (p *githubProvider) Exchange(ctx context.Context, code, _ string) (*oauth2.Token, error) {
	tok, err := p.oauth2Config.Exchange(withHTTPClient(ctx, p.httpClient), code)
	if err != nil {
		return nil, fmt.Errorf("exchange GitHub authorization code: %w", err)
	}

	return tok, nil
}

func (p *githubProvider) Claims(ctx context.Context, tok *oauth2.Token) (Claims, error) {
	if tok == nil || strings.TrimSpace(tok.AccessToken) == "" {
		return Claims{}, fmt.Errorf("read GitHub claims: %w", errGitHubAccessTokenRequired)
	}

	ctx = withHTTPClient(ctx, p.httpClient)
	if _, err := p.user(ctx, tok.AccessToken); err != nil {
		return Claims{}, fmt.Errorf("read GitHub claims: %w", err)
	}

	emails, err := p.emails(ctx, tok.AccessToken)
	if err != nil {
		return Claims{}, fmt.Errorf("read GitHub claims: %w", err)
	}

	email, err := primaryVerifiedEmail(emails)
	if err != nil {
		return Claims{}, fmt.Errorf("read GitHub claims: %w", err)
	}

	return Claims{Email: email}, nil
}

func primaryVerifiedEmail(emails []githubEmail) (string, error) {
	for _, email := range emails {
		if email.Primary && email.Verified {
			if value := strings.TrimSpace(email.Email); value != "" {
				return value, nil
			}
		}
	}

	return "", errGitHubPrimaryVerifiedEmailError
}

func (p *githubProvider) user(ctx context.Context, accessToken string) (githubUser, error) {
	var user githubUser
	if err := p.get(ctx, accessToken, "/user", &user); err != nil {
		return githubUser{}, fmt.Errorf("fetch GitHub user: %w", err)
	}

	return user, nil
}

func (p *githubProvider) emails(ctx context.Context, accessToken string) ([]githubEmail, error) {
	var emails []githubEmail
	if err := p.get(ctx, accessToken, "/user/emails", &emails); err != nil {
		return nil, fmt.Errorf("fetch GitHub user emails: %w", err)
	}

	return emails, nil
}

func (p *githubProvider) get(ctx context.Context, accessToken, path string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.apiURL+path, nil)
	if err != nil {
		return fmt.Errorf("build request %s: %w", path, err)
	}

	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := p.client().Do(req)
	if err != nil {
		return fmt.Errorf("send request %s: %w", path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: unexpected status %s", path, resp.Status)
	}

	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		return fmt.Errorf("decode response %s: %w", path, err)
	}

	return nil
}

func (p *githubProvider) client() *http.Client {
	if p.httpClient != nil {
		return p.httpClient
	}

	return http.DefaultClient
}

func githubScopes(scopes []string) []string {
	if len(scopes) == 0 {
		return []string{"user:email"}
	}

	return append([]string(nil), scopes...)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}

	return ""
}

func (c GitHubConfig) validate() error {
	switch {
	case strings.TrimSpace(c.ClientID) == "":
		return errGitHubClientIDRequired
	case strings.TrimSpace(c.ClientSecret) == "":
		return errGitHubSecretRequired
	case strings.TrimSpace(c.RedirectURL) == "":
		return errGitHubRedirectRequired
	default:
		return nil
	}
}
