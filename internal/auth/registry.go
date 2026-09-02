package auth

import (
	"fmt"
	"slices"
)

type ProviderConfig struct {
	IssuerURL    string
	ClientID     string
	ClientSecret string
	RedirectURL  string
}

type RegistryConfig struct {
	Providers map[Name]ProviderConfig
}

type Registry struct {
	providers map[Name]Provider
}

func NewRegistry(cfg RegistryConfig) (*Registry, error) {
	providers := make(map[Name]Provider, len(cfg.Providers))
	for name, providerCfg := range cfg.Providers {
		if !name.Valid() {
			return nil, fmt.Errorf("build provider registry: invalid provider %q", name)
		}

		provider, err := newConfiguredProvider(name, providerCfg)
		if err != nil {
			return nil, fmt.Errorf("build provider registry for %q: %w", name, err)
		}

		providers[name] = provider
	}

	return &Registry{providers: providers}, nil
}

// Names returns the providers that are actually configured, sorted so the
// login page renders them in a stable order.
//
// The registry is the only sound source of truth for "configured": a
// half-configured provider fails NewRegistry outright, so anything present
// here was built successfully. Reading the config map instead would report a
// provider that the process refused to start with.
func (r *Registry) Names() []Name {
	if r == nil {
		return nil
	}

	names := make([]Name, 0, len(r.providers))
	for name := range r.providers {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func (r *Registry) Get(name Name) (Provider, error) {
	if !name.Valid() {
		return nil, fmt.Errorf("get provider %q: invalid provider name", name)
	}
	if r == nil {
		return nil, fmt.Errorf("get provider %q: provider not configured", name)
	}

	provider, ok := r.providers[name]
	if !ok {
		return nil, fmt.Errorf("get provider %q: provider not configured", name)
	}

	return provider, nil
}

func newConfiguredProvider(name Name, cfg ProviderConfig) (Provider, error) {
	if name == ProviderGitHub {
		return NewGitHubProvider(GitHubConfig{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			RedirectURL:  cfg.RedirectURL,
		})
	}

	return NewOIDCProvider(OIDCConfig{
		IssuerURL:    cfg.IssuerURL,
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		RedirectURL:  cfg.RedirectURL,
	})
}
