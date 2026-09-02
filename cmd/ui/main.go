package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	_ "k8s.io/client-go/plugin/pkg/client/auth"

	dwpkv1alpha1 "github.com/devops-ia/dwpk/api/v1alpha1"
	"github.com/devops-ia/dwpk/internal/auth"
	"github.com/devops-ia/dwpk/internal/ui"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	crconfig "sigs.k8s.io/controller-runtime/pkg/client/config"
)

// version is stamped at build time with -ldflags "-X main.version=...".
var version = "dev"

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(dwpkv1alpha1.AddToScheme(scheme))
}

func main() {
	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "load UI config: %v\n", err)
		os.Exit(1)
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.LogLevel})))
	slog.Info("Starting UI", "version", version)

	kubeConfig, err := buildConfig(cfg.Kubeconfig)
	if err != nil {
		slog.Error("Failed to build Kubernetes config", "error", err)
		os.Exit(1)
	}

	workspaceClient, err := ctrlclient.New(kubeConfig, ctrlclient.Options{Scheme: scheme})
	if err != nil {
		slog.Error("Failed to create controller-runtime client", "error", err)
		os.Exit(1)
	}
	kubeClient, err := kubernetes.NewForConfig(kubeConfig)
	if err != nil {
		slog.Error("Failed to create Kubernetes clientset", "error", err)
		os.Exit(1)
	}

	// The one other thing the UI reads as itself rather than as the person
	// using it (see ui.PlatformReader for the first, and why neither weakens
	// SPEC §8.1): encrypting a git-ssh key on upload needs the same AES key
	// the controller later decrypts it with, and that key cannot be
	// something any caller's own forwarded token could read - it lives in
	// the operator's own namespace, never a user's. Absence is not fatal:
	// the platform simply cannot offer git-ssh key storage until the Helm
	// chart has generated the Secret, and PutGitSSHKey refuses cleanly
	// rather than writing something nothing can ever decrypt.
	gitSSHEncryptionKey, err := readGitSSHEncryptionKey(context.Background(), kubeClient, cfg.GitSSHEncryptionKeyNamespace)
	if err != nil {
		slog.Warn("git-ssh key storage is unavailable", "error", err)
	}

	sessionStore := auth.NewSessionStore(cfg.SessionTTL)
	registry, err := auth.NewRegistry(auth.RegistryConfig{Providers: cfg.ProviderConfigs})
	if err != nil {
		slog.Error("Failed to build provider registry", "error", err)
		os.Exit(1)
	}

	tokenMinter := auth.NewTokenMinter(kubeClient)
	loginFlowDeps := ui.LoginFlowDeps{
		Registry:      registry,
		Sessions:      sessionStore,
		TokenMinter:   tokenMinter,
		OwnerResolver: auth.NewOwnerResolver(workspaceClient),
		GroupRoles:    cfg.GroupRoles,
	}
	var localUsers *auth.LocalUserStore
	if cfg.LocalAuthEnabled {
		localUsers = auth.NewLocalUserStore(workspaceClient, cfg.LocalAuthNamespace)
		loginFlowDeps.LocalUsers = localUsers
		slog.Info("Local username/password login enabled", "namespace", cfg.LocalAuthNamespace)
	}

	loginFlow, err := ui.NewLoginFlow(loginFlowDeps)
	if err != nil {
		slog.Error("Failed to build login flow", "error", err)
		os.Exit(1)
	}

	serverConfig := ui.ServerConfig{
		LoginFlow:      loginFlow,
		ClientFactory:  ui.NewClientFactory(kubeConfig, scheme, gitSSHEncryptionKey),
		GatewayHost:    cfg.GatewayHost,
		ListenAddress:  cfg.ListenAddress,
		BasePath:       cfg.BasePath,
		SessionTTL:     cfg.SessionTTL,
		CookieSecure:   cfg.CookieSecure,
		IconHTTPClient: &http.Client{Timeout: 10 * time.Second},
		APITokens:      auth.NewTokenStore(workspaceClient, cfg.TokenNamespace),
		TokenMinter:    tokenMinter,
		// The one object the UI reads as itself: the login page renders the
		// platform's name and favicon before there is a session to read them
		// with. See ui.PlatformReader for why this does not weaken §8.1.
		PlatformReader: workspaceClient,
		// Names and namespaces only. Every confidential provider value stays in
		// cfg and never reaches the UI package. See ui.RuntimeSettings.
		Runtime: ui.RuntimeSettings{
			GatewayHost:        cfg.GatewayHost,
			BaseURL:            cfg.BaseURL,
			BasePath:           cfg.BasePath,
			ListenAddress:      cfg.ListenAddress,
			SessionTTL:         cfg.SessionTTL,
			CookieSecure:       cfg.CookieSecure,
			LogLevel:           cfg.LogLevel.String(),
			LocalAuthEnabled:   cfg.LocalAuthEnabled,
			LocalAuthNamespace: cfg.LocalAuthNamespace,
			TokenNamespace:     cfg.TokenNamespace,
			Providers:          providerNames(cfg.ProviderConfigs),
			ProviderDetails:    providerDetails(cfg.ProviderConfigs),
			Kubeconfig:         cfg.Kubeconfig,
			Port:               os.Getenv("DWPK__UI_PORT"),
		},
	}
	// A nil-valued interface is not a nil interface, so the store is assigned
	// only when local auth is on; otherwise the API would see a non-nil
	// LocalUserAdmin backed by a nil pointer.
	if localUsers != nil {
		serverConfig.LocalUsers = localUsers
		// Reset links only mean anything where there is a local password to
		// reset, so they share local auth's switch. Same nil-interface caveat.
		serverConfig.PasswordResets = auth.NewResetStore(workspaceClient, cfg.LocalAuthNamespace)
	}

	server, err := ui.NewServer(serverConfig)
	if err != nil {
		slog.Error("Failed to build UI server", "error", err)
		os.Exit(1)
	}

	slog.Info("dwpk UI listening", "address", cfg.ListenAddress, "base_path", cfg.BasePath)
	if err := server.ListenAndServe(); err != nil {
		slog.Error("UI server exited", "error", err)
		os.Exit(1)
	}
}

type config struct {
	ListenAddress                string
	Kubeconfig                   string
	GatewayHost                  string
	BaseURL                      string
	BasePath                     string
	SessionTTL                   time.Duration
	CookieSecure                 bool
	LogLevel                     slog.Level
	LocalAuthEnabled             bool
	LocalAuthNamespace           string
	TokenNamespace               string
	GitSSHEncryptionKeyNamespace string
	ProviderConfigs              map[auth.Name]auth.ProviderConfig
	GroupRoles                   map[auth.Name]ui.GroupRoleMapping
}

func loadConfig() (config, error) {
	ttl := 15 * time.Minute
	if raw := strings.TrimSpace(os.Getenv("DWPK__UI_SESSION_TTL")); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil {
			return config{}, fmt.Errorf("parse DWPK__UI_SESSION_TTL: %w", err)
		}
		ttl = parsed
	}

	logLevel, err := parseLogLevel(firstEnv("DWPK__UI_LOG_LEVEL", "info"))
	if err != nil {
		return config{}, err
	}

	cfg := config{
		ListenAddress:                listenAddressFromEnv(),
		Kubeconfig:                   strings.TrimSpace(os.Getenv("DWPK__UI_KUBECONFIG")),
		GatewayHost:                  firstEnv("DWPK__UI_GATEWAY_HOST", "dwpk.example.com"),
		BaseURL:                      strings.TrimRight(strings.TrimSpace(os.Getenv("DWPK__UI_BASE_URL")), "/"),
		BasePath:                     strings.TrimSpace(os.Getenv("DWPK__UI_BASE_PATH")),
		SessionTTL:                   ttl,
		CookieSecure:                 boolEnv("DWPK__UI_COOKIE_SECURE", true),
		LogLevel:                     logLevel,
		LocalAuthEnabled:             boolEnv("DWPK__UI_LOCAL_AUTH_ENABLED", false),
		LocalAuthNamespace:           firstEnv("DWPK__UI_LOCAL_AUTH_NAMESPACE", "dwpk-system"),
		TokenNamespace:               firstEnv("DWPK__UI_TOKEN_NAMESPACE", "dwpk-system"),
		GitSSHEncryptionKeyNamespace: firstEnv("DWPK__UI_GIT_SSH_ENCRYPTION_KEY_NAMESPACE", "dwpk-system"),
		ProviderConfigs:              map[auth.Name]auth.ProviderConfig{},
		GroupRoles:                   map[auth.Name]ui.GroupRoleMapping{},
	}

	for _, provider := range ui.ProviderNames() {
		providerCfg, ok, err := providerConfigFromEnv(provider, cfg.BaseURL)
		if err != nil {
			return config{}, err
		}
		if ok {
			cfg.ProviderConfigs[provider] = providerCfg
		}

		if mapping, ok := groupRoleMappingFromEnv(provider); ok {
			cfg.GroupRoles[provider] = mapping
		}
	}

	return cfg, nil
}

// listenAddressFromEnv resolves the bind address. DWPK__UI_LISTEN_ADDRESS (a
// full "host:port" address) takes precedence when set; otherwise
// DWPK__UI_PORT selects just the port on all interfaces; the default is
// ":8080".
func listenAddressFromEnv() string {
	if addr := strings.TrimSpace(os.Getenv("DWPK__UI_LISTEN_ADDRESS")); addr != "" {
		return addr
	}
	if port := strings.TrimSpace(os.Getenv("DWPK__UI_PORT")); port != "" {
		return ":" + port
	}
	return ":8080"
}

// parseLogLevel maps a case-insensitive level name to a slog.Level.
func parseLogLevel(raw string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info", "":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("parse DWPK__UI_LOG_LEVEL: unknown level %q (want debug, info, warn, or error)", raw)
	}
}

func providerConfigFromEnv(provider auth.Name, baseURL string) (auth.ProviderConfig, bool, error) {
	prefix := "DWPK__UI_PROVIDER_" + strings.ToUpper(strings.ReplaceAll(string(provider), "-", "_")) + "_"
	clientID := strings.TrimSpace(os.Getenv(prefix + "CLIENT_ID"))
	clientSecret := strings.TrimSpace(os.Getenv(prefix + "CLIENT_SECRET"))
	issuerURL := strings.TrimSpace(os.Getenv(prefix + "ISSUER_URL"))
	redirectURL := strings.TrimSpace(os.Getenv(prefix + "REDIRECT_URL"))
	if clientID == "" && clientSecret == "" && issuerURL == "" && redirectURL == "" {
		return auth.ProviderConfig{}, false, nil
	}
	if redirectURL == "" {
		if baseURL == "" {
			return auth.ProviderConfig{}, false, fmt.Errorf(
				"provider %q configured without redirect URL or DWPK__UI_BASE_URL", provider)
		}
		redirectURL = baseURL + "/callback/" + string(provider)
	}
	return auth.ProviderConfig{
		IssuerURL:    issuerURL,
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURL:  redirectURL,
	}, true, nil
}

// groupRoleMappingFromEnv reads DWPK__UI_PROVIDER_<NAME>_ADMIN_GROUPS and
// _USER_GROUPS (comma-separated group names/IDs, per §7.9) for one provider.
// Neither var set means no group-based mapping - the UserSpace's own
// spec.role decides, exactly as it did before this feature existed.
func groupRoleMappingFromEnv(provider auth.Name) (ui.GroupRoleMapping, bool) {
	prefix := "DWPK__UI_PROVIDER_" + strings.ToUpper(strings.ReplaceAll(string(provider), "-", "_")) + "_"
	adminGroups := splitCommaList(os.Getenv(prefix + "ADMIN_GROUPS"))
	userGroups := splitCommaList(os.Getenv(prefix + "USER_GROUPS"))
	if len(adminGroups) == 0 && len(userGroups) == 0 {
		return ui.GroupRoleMapping{}, false
	}
	return ui.GroupRoleMapping{AdminGroups: adminGroups, UserGroups: userGroups}, true
}

func splitCommaList(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			values = append(values, trimmed)
		}
	}
	return values
}

// readGitSSHEncryptionKey reads the AES key from
// dwpkv1alpha1.GitSSHEncryptionKeySecretName with the UI's own credential -
// never a caller's forwarded token, since no single user's RBAC should ever
// reach a key that decrypts every other user's git-ssh keys too.
func readGitSSHEncryptionKey(ctx context.Context, kubeClient kubernetes.Interface, namespace string) ([]byte, error) {
	name := dwpkv1alpha1.GitSSHEncryptionKeySecretName
	secret, err := kubeClient.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get %s/%s: %w", namespace, name, err)
	}
	dataKey := dwpkv1alpha1.GitSSHEncryptionKeySecretDataKey
	key := secret.Data[dataKey]
	if len(key) == 0 {
		return nil, fmt.Errorf("%s/%s has no %q entry", namespace, name, dataKey)
	}
	return key, nil
}

func buildConfig(kubeconfig string) (*rest.Config, error) {
	if strings.TrimSpace(kubeconfig) != "" {
		return clientcmd.BuildConfigFromFlags("", kubeconfig)
	}
	return crconfig.GetConfig()
}

func firstEnv(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func boolEnv(key string, fallback bool) bool {
	raw := strings.TrimSpace(strings.ToLower(os.Getenv(key)))
	if raw == "" {
		return fallback
	}
	switch raw {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}

// providerNames lists the configured providers for the settings screen. Names
// only: nothing confidential about a provider has any reason to leave here.
func providerNames(configs map[auth.Name]auth.ProviderConfig) []string {
	names := make([]string, 0, len(configs))
	for name := range configs {
		names = append(names, string(name))
	}
	slices.Sort(names)
	return names
}

// providerDetails is what the settings screen may show about each provider.
//
// The client secret is not read here, and there is no field on
// ui.ProviderSettings that could hold it. That is the point: the guarantee is
// structural rather than a masking step somebody can forget.
func providerDetails(configs map[auth.Name]auth.ProviderConfig) []ui.ProviderSettings {
	names := providerNames(configs)
	details := make([]ui.ProviderSettings, 0, len(names))
	for _, name := range names {
		config := configs[auth.Name(name)]
		details = append(details, ui.ProviderSettings{
			Name:        name,
			ClientID:    config.ClientID,
			IssuerURL:   config.IssuerURL,
			RedirectURL: config.RedirectURL,
		})
	}
	return details
}
