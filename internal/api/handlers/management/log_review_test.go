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

func TestGetRequestErrorLogReviewExplainsStaticManagement404(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tmp := t.TempDir()
	name := "error-static-management.html-2026-07-05T215514-4.log"
	logText := `=== REQUEST INFO ===
Version: llmdynamix-backend
URL: /static/management.html?api_key=secret-value
Method: HEAD
Timestamp: 2026-07-05T21:55:14.075436-04:00

=== RESPONSE ===
Status: 404
`
	if err := os.WriteFile(filepath.Join(tmp, name), []byte(logText), 0o600); err != nil {
		t.Fatal(err)
	}

	handler := NewHandler(&config.Config{LoggingToFile: true}, "", nil)
	handler.SetLogDirectory(tmp)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/request-error-logs/"+name+"/review", nil)
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Params = gin.Params{{Key: "name", Value: name}}
	ctx.Request = req
	handler.GetRequestErrorLogReview(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got logReviewResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Category != "static_management_asset_missing" {
		t.Fatalf("category = %q, want static_management_asset_missing", got.Category)
	}
	if got.Severity != "warning" {
		t.Fatalf("severity = %q, want warning", got.Severity)
	}
	if !strings.Contains(got.LikelyCause, "/management.html") {
		t.Fatalf("likely cause does not mention current management path: %#v", got)
	}
	body := rec.Body.String()
	if strings.Contains(body, "secret-value") {
		t.Fatalf("review leaked secret value: %s", body)
	}
	if !strings.Contains(body, "[REDACTED]") {
		t.Fatalf("review did not show redacted evidence: %s", body)
	}
}

func TestGetRequestErrorLogReviewRejectsInvalidName(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := NewHandler(&config.Config{LoggingToFile: true}, "", nil)
	handler.SetLogDirectory(t.TempDir())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/request-error-logs/../main.log/review", nil)
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Params = gin.Params{{Key: "name", Value: "../main.log"}}
	ctx.Request = req
	handler.GetRequestErrorLogReview(ctx)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}
