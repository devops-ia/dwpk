package main

import (
	"log/slog"
	"os"
	"slices"
	"testing"

	"github.com/devops-ia/dwpk/internal/auth"
)

func TestParseLogLevel(t *testing.T) {
	t.Parallel()
	cases := []struct {
		raw     string
		want    slog.Level
		wantErr bool
	}{
		{"", slog.LevelInfo, false},
		{"info", slog.LevelInfo, false},
		{"INFO", slog.LevelInfo, false},
		{"debug", slog.LevelDebug, false},
		{"warn", slog.LevelWarn, false},
		{"warning", slog.LevelWarn, false},
		{"error", slog.LevelError, false},
		{"trace", 0, true},
	}
	for _, tc := range cases {
		got, err := parseLogLevel(tc.raw)
		if tc.wantErr {
			if err == nil {
				t.Errorf("parseLogLevel(%q) expected error, got nil", tc.raw)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseLogLevel(%q) unexpected error: %v", tc.raw, err)
		}
		if got != tc.want {
			t.Errorf("parseLogLevel(%q) = %v, want %v", tc.raw, got, tc.want)
		}
	}
}

func TestListenAddressFromEnvPrecedence(t *testing.T) {
	clearListenEnv(t)

	if got := listenAddressFromEnv(); got != ":8080" {
		t.Errorf("default = %q, want :8080", got)
	}

	t.Setenv("DWPK__UI_PORT", "9090")
	if got := listenAddressFromEnv(); got != ":9090" {
		t.Errorf("with DWPK__UI_PORT = %q, want :9090", got)
	}

	t.Setenv("DWPK__UI_LISTEN_ADDRESS", "127.0.0.1:7070")
	if got := listenAddressFromEnv(); got != "127.0.0.1:7070" {
		t.Errorf("DWPK__UI_LISTEN_ADDRESS should take precedence over DWPK__UI_PORT, got %q", got)
	}
}

func TestLoadConfigDefaultsLocalAuthDisabled(t *testing.T) {
	clearListenEnv(t)
	t.Setenv("DWPK__UI_LOCAL_AUTH_ENABLED", "")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if cfg.LocalAuthEnabled {
		t.Error("LocalAuthEnabled should default to false")
	}
	if cfg.LocalAuthNamespace != "dwpk-system" {
		t.Errorf("LocalAuthNamespace = %q, want dwpk-system", cfg.LocalAuthNamespace)
	}
	if cfg.BasePath != "" {
		t.Errorf("BasePath = %q, want empty by default", cfg.BasePath)
	}
	if cfg.LogLevel != slog.LevelInfo {
		t.Errorf("LogLevel = %v, want info", cfg.LogLevel)
	}
}

func TestLoadConfigReadsLocalAuthAndBasePath(t *testing.T) {
	clearListenEnv(t)
	t.Setenv("DWPK__UI_LOCAL_AUTH_ENABLED", "true")
	t.Setenv("DWPK__UI_LOCAL_AUTH_NAMESPACE", "custom-ns")
	t.Setenv("DWPK__UI_BASE_PATH", "/dwpk")
	t.Setenv("DWPK__UI_LOG_LEVEL", "debug")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if !cfg.LocalAuthEnabled {
		t.Error("LocalAuthEnabled should be true")
	}
	if cfg.LocalAuthNamespace != "custom-ns" {
		t.Errorf("LocalAuthNamespace = %q, want custom-ns", cfg.LocalAuthNamespace)
	}
	if cfg.BasePath != "/dwpk" {
		t.Errorf("BasePath = %q, want /dwpk", cfg.BasePath)
	}
	if cfg.LogLevel != slog.LevelDebug {
		t.Errorf("LogLevel = %v, want debug", cfg.LogLevel)
	}
}

func clearListenEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"DWPK__UI_LISTEN_ADDRESS", "DWPK__UI_PORT", "DWPK__UI_BASE_PATH",
		"DWPK__UI_LOG_LEVEL", "DWPK__UI_LOCAL_AUTH_ENABLED", "DWPK__UI_LOCAL_AUTH_NAMESPACE",
		"DWPK__UI_BASE_URL",
	} {
		orig, had := os.LookupEnv(key)
		if err := os.Unsetenv(key); err != nil {
			t.Fatalf("Unsetenv(%s) error = %v", key, err)
		}
		if had {
			t.Cleanup(func() {
				if err := os.Setenv(key, orig); err != nil {
					t.Fatalf("Setenv(%s) error = %v", key, err)
				}
			})
		}
	}
}

func TestGroupRoleMappingFromEnv(t *testing.T) {
	t.Run("neither var set means no mapping", func(t *testing.T) {
		t.Setenv("DWPK__UI_PROVIDER_ENTRA_ID_ADMIN_GROUPS", "")
		t.Setenv("DWPK__UI_PROVIDER_ENTRA_ID_USER_GROUPS", "")

		if _, ok := groupRoleMappingFromEnv(auth.ProviderEntraID); ok {
			t.Fatal("groupRoleMappingFromEnv() ok = true, want false")
		}
	})

	t.Run("parses comma-separated group lists and trims whitespace", func(t *testing.T) {
		t.Setenv("DWPK__UI_PROVIDER_ENTRA_ID_ADMIN_GROUPS", "paas-admins, platform-team")
		t.Setenv("DWPK__UI_PROVIDER_ENTRA_ID_USER_GROUPS", "engineering")

		mapping, ok := groupRoleMappingFromEnv(auth.ProviderEntraID)
		if !ok {
			t.Fatal("groupRoleMappingFromEnv() ok = false, want true")
		}
		wantAdmin := []string{"paas-admins", "platform-team"}
		if !slices.Equal(mapping.AdminGroups, wantAdmin) {
			t.Fatalf("AdminGroups = %v, want %v", mapping.AdminGroups, wantAdmin)
		}
		if len(mapping.UserGroups) != 1 || mapping.UserGroups[0] != "engineering" {
			t.Fatalf("UserGroups = %v, want [engineering]", mapping.UserGroups)
		}
	})
}
