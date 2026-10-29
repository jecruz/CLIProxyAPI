package management

import (
	"bytes"
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

func TestGetDiagnosticsModelReviewBuildsRedactedPrompt(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tmp := t.TempDir()
	logText := `=== REQUEST INFO ===
URL: /v1/chat/completions?api_key=secret-value
Method: POST
Timestamp: 2026-07-07T01:00:00-04:00
Auth: Authorization: Bearer secret-token-value

=== REQUEST BODY ===
{"model":"gemini-2.5-pro"}

=== RESPONSE ===
Status: 401
`
	if err := os.WriteFile(filepath.Join(tmp, "error-chat-2026-07-07T010000-1.log"), []byte(logText), 0o600); err != nil {
		t.Fatal(err)
	}

	handler := NewHandler(&config.Config{
		SDKConfig: config.SDKConfig{
			RequestLog: true,
		},
		Host:          "127.0.0.1",
		Port:          12455,
		AuthDir:       filepath.Join(tmp, "auth"),
		LoggingToFile: true,
		RemoteManagement: config.RemoteManagement{
			SecretKey: "management-secret-one",
		},
		GeminiKey: []config.GeminiKey{{
			APIKey: "gemini-secret-one",
		}},
	}, filepath.Join(tmp, "config.yaml"), nil)
	handler.SetLogDirectory(tmp)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/diagnostics/model-review", nil)
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = req
	handler.GetDiagnosticsModelReview(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got diagnosticsModelReviewResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.ModelPromptReady || !strings.Contains(got.ModelPrompt, "Evidence") {
		t.Fatalf("model prompt not ready: %#v", got)
	}
	body := rec.Body.String()
	for _, secret := range []string{"secret-value", "secret-token-value", "gemini-secret-one", "management-secret-one"} {
		if strings.Contains(body, secret) {
			t.Fatalf("diagnostics model review leaked %q in %s", secret, body)
		}
	}
	if !strings.Contains(body, "[REDACTED]") {
		t.Fatalf("expected redacted evidence in response: %s", body)
	}
	if got.Snapshot.RequestTraces == nil || len(got.Snapshot.RequestTraces.Recent) == 0 {
		t.Fatalf("expected request trace summary: %#v", got.Snapshot.RequestTraces)
	}
}

func TestPostDiagnosticsModelReviewAuditWritesRedactedRecord(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tmp := t.TempDir()
	handler := NewHandler(&config.Config{}, filepath.Join(tmp, "config.yaml"), nil)
	handler.SetLogDirectory(tmp)

	body := `{
		"model":"diagnostic-model",
		"generatedAt":"2026-07-07T01:00:00Z",
		"approved":true,
		"modelPrompt":"Authorization: Bearer prompt-secret\napi_key=prompt-secret",
		"modelResponse":"Evidence: redacted evidence\nLikely Cause: stale key\nSuggested Fix: open console\nConfidence: medium\napi_key=response-secret"
	}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/diagnostics/model-review/audit", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = req
	handler.PostDiagnosticsModelReviewAudit(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got diagnosticsModelReviewAuditResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Recorded || !got.Validated || len(got.MissingSections) != 0 {
		t.Fatalf("unexpected audit response: %#v", got)
	}
	data, err := os.ReadFile(filepath.Join(tmp, "diagnostics-model-review-audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, secret := range []string{"prompt-secret", "response-secret"} {
		if strings.Contains(text, secret) {
			t.Fatalf("audit leaked %q in %s", secret, text)
		}
	}
	if !strings.Contains(text, "[REDACTED]") {
		t.Fatalf("audit did not contain redacted evidence: %s", text)
	}
}

func TestPostDiagnosticsModelReviewAuditRequiresApproval(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := NewHandler(&config.Config{}, "", nil)
	handler.SetLogDirectory(t.TempDir())

	body := `{"model":"diagnostic-model","approved":false,"modelPrompt":"prompt","modelResponse":"response"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/diagnostics/model-review/audit", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = req
	handler.PostDiagnosticsModelReviewAudit(ctx)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestMissingDiagnosticsModelReviewSections(t *testing.T) {
	missing := missingDiagnosticsModelReviewSections("Evidence: one\nConfidence: low")
	if strings.Join(missing, ",") != "Likely Cause,Suggested Fix" {
		t.Fatalf("missing sections = %#v", missing)
	}
}

func TestBuildDiagnosticsModelPromptRequiresStructuredSections(t *testing.T) {
	prompt := buildDiagnosticsModelPrompt(diagnosticsReviewSnapshot{
		Runtime: diagnosticsReviewRuntime{Product: "LLM Dynamix"},
	})
	for _, section := range []string{"Evidence", "Likely Cause", "Suggested Fix", "Confidence"} {
		if !strings.Contains(prompt, section) {
			t.Fatalf("prompt missing section %q: %s", section, prompt)
		}
	}
}
