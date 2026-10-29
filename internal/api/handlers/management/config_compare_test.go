package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestGetConfigCompareRedactsSecrets(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	configYAML := []byte(`
host: 127.0.0.1
port: 12455
auth-dir: /tmp/llmd-auth
api-keys:
  - client-secret-one
remote-management:
  secret-key: management-secret-one
logging-to-file: true
request-log: true
gemini-api-key:
  - api-key: gemini-secret-one
openai-compatibility:
  - name: openrouter
    base-url: https://openrouter.ai/api/v1
    api-key-entries:
      - api-key: openrouter-secret-one
`)
	if err := os.WriteFile(configPath, configYAML, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	h := NewHandler(&config.Config{
		Host:          "127.0.0.1",
		Port:          12455,
		AuthDir:       "/tmp/llmd-auth",
		LoggingToFile: true,
		SDKConfig: config.SDKConfig{
			RequestLog: true,
			APIKeys:    []string{"client-secret-one"},
		},
		RemoteManagement: config.RemoteManagement{SecretKey: "management-secret-one"},
		GeminiKey:        []config.GeminiKey{{APIKey: "gemini-secret-one"}},
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name: "openrouter",
			APIKeyEntries: []config.OpenAICompatibilityAPIKey{
				{APIKey: "openrouter-secret-one"},
			},
		}},
	}, configPath, nil)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/v0/management/config/compare", nil)

	h.GetConfigCompare(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, secret := range []string{"client-secret-one", "management-secret-one", "gemini-secret-one", "openrouter-secret-one"} {
		if strings.Contains(body, secret) {
			t.Fatalf("response leaked secret %q: %s", secret, body)
		}
	}
	if !strings.Contains(body, `"managementKeyConfigured":true`) {
		t.Fatalf("response did not include management key presence: %s", body)
	}
	if !strings.Contains(body, `"clientApiKeyCount":1`) {
		t.Fatalf("response did not include client API key count: %s", body)
	}
}

func TestGetConfigCompareReportsDifferences(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	configDir := filepath.Join(dir, ".llmdynamix-dev")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	configPath := filepath.Join(configDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(`
port: 12454
auth-dir: /tmp/dev-auth
logging-to-file: false
api-keys:
  - client-one
`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	h := NewHandler(&config.Config{
		Port:          12455,
		AuthDir:       "/tmp/effective-auth",
		LoggingToFile: true,
		SDKConfig: config.SDKConfig{
			APIKeys: []string{"client-one", "client-two"},
		},
	}, configPath, nil)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/v0/management/config/compare", nil)

	h.GetConfigCompare(c)

	var response configCompareResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v\n%s", err, w.Body.String())
	}

	fields := map[string]bool{}
	for _, diff := range response.Differences {
		if diff.Source == "dev" || diff.Source == "production" || diff.Source == "bundled-default" {
			fields[diff.Field] = true
		}
	}
	for _, field := range []string{"port", "authDir", "clientApiKeyCount", "fileLogging"} {
		if !fields[field] {
			t.Fatalf("expected difference field %q in %+v", field, response.Differences)
		}
	}
}
