package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveConfigPreserveComments_MigratesLegacyOpenAICompatAPIKeys(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	legacy := []byte(`
openai-compatibility:
  - name: Openrouter
    base-url: https://openrouter.ai/api/v1
    api-keys:
      - sk-or-v1-test-key
    models:
      - name: openrouter/auto
`)
	if errWrite := os.WriteFile(configPath, legacy, 0o600); errWrite != nil {
		t.Fatalf("os.WriteFile() error = %v", errWrite)
	}

	cfg, errParse := ParseConfigBytes(legacy)
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}
	if len(cfg.OpenAICompatibility) != 1 {
		t.Fatalf("len(OpenAICompatibility) = %d, want 1", len(cfg.OpenAICompatibility))
	}
	if len(cfg.OpenAICompatibility[0].APIKeyEntries) != 1 {
		t.Fatalf("len(APIKeyEntries) = %d, want 1", len(cfg.OpenAICompatibility[0].APIKeyEntries))
	}
	if cfg.OpenAICompatibility[0].APIKeyEntries[0].APIKey != "sk-or-v1-test-key" {
		t.Fatalf("APIKeyEntries[0].APIKey = %q, want sk-or-v1-test-key", cfg.OpenAICompatibility[0].APIKeyEntries[0].APIKey)
	}
	if errSave := SaveConfigPreserveComments(configPath, cfg); errSave != nil {
		t.Fatalf("SaveConfigPreserveComments() error = %v", errSave)
	}

	data, errRead := os.ReadFile(configPath)
	if errRead != nil {
		t.Fatalf("os.ReadFile() error = %v", errRead)
	}
	text := string(data)
	if strings.Contains(text, "api-keys:") {
		t.Fatalf("saved config still contains legacy api-keys field:\n%s", text)
	}
	if !strings.Contains(text, "api-key-entries:") {
		t.Fatalf("saved config missing api-key-entries field:\n%s", text)
	}
	if !strings.Contains(text, "sk-or-v1-test-key") {
		t.Fatalf("saved config lost OpenRouter API key:\n%s", text)
	}
}
