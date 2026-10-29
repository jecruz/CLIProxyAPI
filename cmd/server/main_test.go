package main

import (
	"os"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/buildinfo"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestLoadManagementPasswordFromFileRemovesSecretFile(t *testing.T) {
	secretFile, err := os.CreateTemp(t.TempDir(), "management-secret-")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := secretFile.WriteString("file-management-secret\n"); err != nil {
		t.Fatal(err)
	}
	if err := secretFile.Close(); err != nil {
		t.Fatal(err)
	}

	t.Setenv("MANAGEMENT_PASSWORD_FILE", secretFile.Name())
	t.Setenv("MANAGEMENT_PASSWORD", "")

	got := loadManagementPasswordFromEnvironment()
	if got != "file-management-secret" {
		t.Fatalf("loadManagementPasswordFromEnvironment() = %q, want file secret", got)
	}
	if _, err := os.Stat(secretFile.Name()); !os.IsNotExist(err) {
		t.Fatalf("secret file still exists, stat error = %v", err)
	}
}

func TestVersionLineUsesLLMDynamixProductName(t *testing.T) {
	oldVersion, oldCommit, oldBuildDate := buildinfo.Version, buildinfo.Commit, buildinfo.BuildDate
	t.Cleanup(func() {
		buildinfo.Version = oldVersion
		buildinfo.Commit = oldCommit
		buildinfo.BuildDate = oldBuildDate
	})

	buildinfo.Version = "1.2.3"
	buildinfo.Commit = "abc123"
	buildinfo.BuildDate = "2026-06-13T00:00:00Z"

	got := versionLine()
	if !strings.Contains(got, "LLM Dynamix Engine Version: 1.2.3") {
		t.Fatalf("versionLine() = %q, want LLM Dynamix product name", got)
	}
	if strings.Contains(got, "CLIProxyAPI Version") {
		t.Fatalf("versionLine() = %q, leaked upstream product name", got)
	}
}

func TestShouldStartExampleAPIKeyWarningServer(t *testing.T) {
	cfgWithExampleKey := &config.Config{
		SDKConfig: config.SDKConfig{
			APIKeys: []string{"real-key", " your-api-key-1 "},
		},
	}
	cfgWithRealKey := &config.Config{
		SDKConfig: config.SDKConfig{
			APIKeys: []string{"real-key"},
		},
	}

	tests := []struct {
		name               string
		cfg                *config.Config
		commandMode        bool
		tuiMode            bool
		standalone         bool
		cloudConfigMissing bool
		homeMode           bool
		want               bool
	}{
		{
			name: "normal server with example key",
			cfg:  cfgWithExampleKey,
			want: true,
		},
		{
			name:       "standalone tui with example key",
			cfg:        cfgWithExampleKey,
			tuiMode:    true,
			standalone: true,
			want:       true,
		},
		{
			name:        "pure tui client is not blocked",
			cfg:         cfgWithExampleKey,
			tuiMode:     true,
			standalone:  false,
			commandMode: false,
			want:        false,
		},
		{
			name:        "one-shot command is not blocked",
			cfg:         cfgWithExampleKey,
			commandMode: true,
			want:        false,
		},
		{
			name:     "home mode is not blocked",
			cfg:      cfgWithExampleKey,
			homeMode: true,
			want:     false,
		},
		{
			name:               "cloud standby without config is not blocked",
			cfg:                cfgWithExampleKey,
			cloudConfigMissing: true,
			want:               false,
		},
		{
			name: "normal server with real key",
			cfg:  cfgWithRealKey,
			want: false,
		},
		{
			name: "nil config",
			cfg:  nil,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldStartExampleAPIKeyWarningServer(tt.cfg, tt.commandMode, tt.tuiMode, tt.standalone, tt.cloudConfigMissing, tt.homeMode)
			if got != tt.want {
				t.Fatalf("shouldStartExampleAPIKeyWarningServer() = %t, want %t", got, tt.want)
			}
		})
	}
}
