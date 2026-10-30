package management

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/providerkeys"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// TestProviderDoctorResolvesEveryCredentialBeforeProbing forbids literal references and partial fallback.
func TestProviderDoctorResolvesEveryCredentialBeforeProbing(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv(providerkeys.EnvironmentName, `{"keychain://caiss/providers/FIXTURE":"sentinel-doctor-only"}`)
	if err := providerkeys.ConsumeEnvironment(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = providerkeys.ConsumeEnvironment() })
	for _, tc := range []struct {
		name    string
		entries []config.OpenAICompatibilityAPIKey
		allowed bool
	}{
		{"resolved", []config.OpenAICompatibilityAPIKey{{APIKey: "keychain://caiss/providers/FIXTURE"}}, true},
		{"mixed-missing", []config.OpenAICompatibilityAPIKey{{APIKey: "literal-fixture"}, {APIKey: "keychain://caiss/providers/MISSING"}}, false},
		{"mixed-malformed", []config.OpenAICompatibilityAPIKey{{APIKey: "literal-fixture"}, {APIKey: "KEYCHAIN://caiss/providers/FIXTURE"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			var correctHeader atomic.Bool
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				correctHeader.Store(r.Header.Get("Authorization") == "Bearer sentinel-doctor-only")
				w.WriteHeader(http.StatusOK)
			}))
			defer upstream.Close()
			h := NewHandler(&config.Config{OpenAICompatibility: []config.OpenAICompatibility{{Name: "fixture", BaseURL: upstream.URL, APIKeyEntries: tc.entries}}}, "", nil)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/provider-doctor/live-probe", strings.NewReader(`{"provider":"openaiCompatibility","kind":"api","approved":true}`))
			h.LiveProbeProvider(c)
			if tc.allowed {
				if requests.Load() != 1 || !correctHeader.Load() {
					t.Fatal("probe did not use the resolved runtime credential")
				}
			} else if requests.Load() != 0 || !strings.Contains(rec.Body.String(), "credential_unavailable") {
				t.Fatal("unavailable provider must refuse before any upstream request")
			}
			if strings.Contains(rec.Body.String(), "sentinel-doctor-only") {
				t.Fatal("probe response disclosed runtime credential")
			}
		})
	}
}

// TestProviderRuntimeAuthCannotDownload blocks runtime credentials before file lookup.
func TestProviderRuntimeAuthCannotDownload(t *testing.T) {
	gin.SetMode(gin.TestMode)
	manager := coreauth.NewManager(nil, nil, nil)
	_, err := manager.Register(context.Background(), &coreauth.Auth{
		ID: "provider-fixture", FileName: "provider-fixture.json", Provider: "fixture",
		Attributes: map[string]string{"runtime_only": "true", "api_key": "sentinel-fixture-only"},
	})
	if err != nil {
		t.Fatal("fixture registration failed")
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if os.WriteFile(path, []byte("port: 12445\n"), 0o600) != nil {
		t.Fatal("fixture config creation failed")
	}
	h := NewHandler(&config.Config{AuthDir: t.TempDir()}, path, manager)
	for _, method := range []string{http.MethodPut, http.MethodGet} {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(method, "/openai-compatibility", strings.NewReader(`[{"name":"fixture","base-url":"http://127.0.0.1","api-key-entries":[{"api-key":"keychain://caiss/providers/FIXTURE"}]}]`))
		if method == http.MethodPut {
			h.PutOpenAICompat(c)
		} else {
			h.GetOpenAICompat(c)
		}
		if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "sentinel-fixture-only") {
			t.Fatal("management config round-trip failed or disclosed a resolved key")
		}
	}
	data, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(data), "sentinel-fixture-only") || !strings.Contains(string(data), "keychain://caiss/providers/FIXTURE") {
		t.Fatal("management save did not preserve the reference-only config")
	}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/auth-files/download?name=provider-fixture.json", nil)
	h.DownloadAuthFile(c)
	if rec.Code != http.StatusForbidden {
		t.Fatal("runtime-only credentials must refuse export before accessing a file")
	}
}
