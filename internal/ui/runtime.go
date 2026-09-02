package ui

import (
	"fmt"
	"strings"
	"time"
)

// RuntimeSettings is the platform configuration worth showing an administrator:
// what this process was started with, so a screen can answer "why is it behaving
// like that" without anyone reading a Deployment.
//
// It carries no secret. Not "carries them redacted" - carries none. The five
// provider client secrets and the bootstrap password never leave the process
// that reads them, so no later edit here can leak one by forgetting to mask it.
// Which providers are configured is the useful half, and a provider only
// registers when its secret is present, so the list says so implicitly.
type RuntimeSettings struct {
	GatewayHost        string
	BaseURL            string
	BasePath           string
	ListenAddress      string
	SessionTTL         time.Duration
	CookieSecure       bool
	LogLevel           string
	LocalAuthEnabled   bool
	LocalAuthNamespace string
	TokenNamespace     string
	// Providers are the OAuth2 providers that were configured completely enough
	// to register.
	Providers []string
	// ProviderDetails is one row per configured provider: its client ID, issuer
	// and redirect URL. The client SECRET has no field here and never will -
	// this struct being unable to hold one is a stronger guarantee than
	// remembering to mask it.
	ProviderDetails []ProviderSettings
	Kubeconfig      string
	Port            string
}

// ProviderSettings is what can be shown about one login provider.
type ProviderSettings struct {
	Name        string
	ClientID    string
	IssuerURL   string
	RedirectURL string
}

// RuntimeRow is one line of the configuration table.
type RuntimeRow struct {
	Name string
	// Value is already display-ready. Empty renders as "not set" rather than as
	// a blank cell, which would not say whether it is unset or unknown.
	Value string
	// Variable is the environment variable that sets it, so the reader knows
	// what to change rather than only what it currently is.
	Variable string
}

// Rows renders the settings in the order somebody debugging would want them:
// how people reach it, how they sign in, then where its state lives.
func (rs RuntimeSettings) Rows() []RuntimeRow {
	providers := strings.Join(rs.Providers, ", ")
	if providers == "" {
		providers = "none"
	}
	rows := make([]RuntimeRow, 0, 13+4*len(rs.ProviderDetails))
	rows = append(rows,
		RuntimeRow{"Gateway host", rs.GatewayHost, "DWPK__UI_GATEWAY_HOST"},
		RuntimeRow{"Base URL", rs.BaseURL, "DWPK__UI_BASE_URL"},
		RuntimeRow{"Base path", rs.BasePath, "DWPK__UI_BASE_PATH"},
		RuntimeRow{"Listen address", rs.ListenAddress, "DWPK__UI_LISTEN_ADDRESS"},
		RuntimeRow{"Session lifetime", rs.SessionTTL.String(), "DWPK__UI_SESSION_TTL"},
		RuntimeRow{"Secure cookies", yesNo(rs.CookieSecure), "DWPK__UI_COOKIE_SECURE"},
		RuntimeRow{"Log level", rs.LogLevel, "DWPK__UI_LOG_LEVEL"},
		RuntimeRow{"Login providers", providers, "DWPK__UI_PROVIDER_<NAME>_*"},
		RuntimeRow{"Local login", yesNo(rs.LocalAuthEnabled), "DWPK__UI_LOCAL_AUTH_ENABLED"},
		RuntimeRow{"Local user namespace", rs.LocalAuthNamespace, "DWPK__UI_LOCAL_AUTH_NAMESPACE"},
		RuntimeRow{"API token namespace", rs.TokenNamespace, "DWPK__UI_TOKEN_NAMESPACE"},
		RuntimeRow{"Port", rs.Port, "DWPK__UI_PORT"},
		RuntimeRow{"Kubeconfig", rs.Kubeconfig, "DWPK__UI_KUBECONFIG"},
	)

	// One block per provider rather than one row listing names, because when a
	// login is broken the question is always "which redirect URL did it
	// actually register", and a comma-separated list of names cannot answer it.
	for _, provider := range rs.ProviderDetails {
		prefix := "DWPK__UI_PROVIDER_" + strings.ToUpper(strings.ReplaceAll(provider.Name, "-", "_"))
		rows = append(rows,
			RuntimeRow{provider.Name + " client ID", provider.ClientID, prefix + "_CLIENT_ID"},
			RuntimeRow{provider.Name + " issuer", provider.IssuerURL, prefix + "_ISSUER_URL"},
			RuntimeRow{provider.Name + " redirect", provider.RedirectURL, prefix + "_REDIRECT_URL"},
			// Named so its absence from the table is visibly deliberate rather
			// than an oversight somebody later "fixes" by adding the value.
			RuntimeRow{provider.Name + " client secret", "set, and never shown", prefix + "_CLIENT_SECRET"},
		)
	}
	return rows
}

func yesNo(value bool) string {
	return fmt.Sprintf("%t", value)
}
