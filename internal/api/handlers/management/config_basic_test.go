package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestLatestReleaseMetadataUsesLLMDynamixRepository(t *testing.T) {
	if latestReleaseURL != "https://api.github.com/repos/jecruz/llmdynamix/releases/latest" {
		t.Fatalf("latestReleaseURL = %q", latestReleaseURL)
	}
	if latestReleaseUserAgent != "LLM Dynamix Engine" {
		t.Fatalf("latestReleaseUserAgent = %q", latestReleaseUserAgent)
	}
}

func TestRuntimeSummaryFormatsUptime(t *testing.T) {
	if got := formatRuntimeUptime(25 * time.Hour); got != "1d 1h 0m" {
		t.Fatalf("formatRuntimeUptime(25h) = %q", got)
	}
	if got := formatRuntimeUptime(2*time.Hour + 3*time.Minute + 4*time.Second); got != "2h 3m 4s" {
		t.Fatalf("formatRuntimeUptime(2h3m4s) = %q", got)
	}
	if got := formatRuntimeUptime(90 * time.Second); got != "1m 30s" {
		t.Fatalf("formatRuntimeUptime(90s) = %q", got)
	}
}

func TestGetConfigIncludesRuntimeUptime(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := NewHandler(&config.Config{}, "", nil)
	handler.startedAt = time.Now().UTC().Add(-90 * time.Second)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/config", nil)

	handler.GetConfig(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}

	var payload struct {
		Runtime runtimeSummary `json:"runtime"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if payload.Runtime.StartedAt == "" {
		t.Fatal("runtime.startedAt is empty")
	}
	if payload.Runtime.UptimeSeconds < 89 {
		t.Fatalf("runtime.uptimeSeconds = %d, want at least 89", payload.Runtime.UptimeSeconds)
	}
	if payload.Runtime.Uptime == "" {
		t.Fatal("runtime.uptime is empty")
	}
}

func TestGetConfigReportsManagementKeyPresenceWithoutReturningSecret(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("MANAGEMENT_PASSWORD", "runtime-management-secret")
	handler := NewHandler(&config.Config{}, "", nil)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/config", nil)

	handler.GetConfig(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	var payload struct {
		ManagementKeyConfigured bool `json:"managementKeyConfigured"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if !payload.ManagementKeyConfigured {
		t.Fatal("managementKeyConfigured = false, want true")
	}
	if strings.Contains(recorder.Body.String(), "runtime-management-secret") {
		t.Fatal("config response exposed the management secret")
	}
}
