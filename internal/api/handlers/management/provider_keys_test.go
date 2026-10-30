package management

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

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
