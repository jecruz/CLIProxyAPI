package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestGetRequestTracesParsesRequestLogEvidence(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tmp := t.TempDir()
	name := "v1-chat-completions-2026-07-07T010203-trace123.log"
	logText := `=== REQUEST INFO ===
URL: /v1/chat/completions
Method: POST
Timestamp: 2026-07-07T01:02:03Z

=== REQUEST BODY ===
{"model":"client-model","messages":[]}

=== API REQUEST 1 ===
Timestamp: 2026-07-07T01:02:04Z
Upstream URL: https://api.example.test/v1/chat/completions?key=secret
HTTP Method: POST
Auth: provider=gemini, auth_id=oauth-1, label=primary, type=oauth value=secret

Body:
{"model":"gemini-upstream","contents":[]}

=== RESPONSE ===
Status: 429 Too Many Requests
`
	if err := os.WriteFile(filepath.Join(tmp, name), []byte(logText), 0o600); err != nil {
		t.Fatal(err)
	}

	handler := NewHandler(&config.Config{LoggingToFile: true, SDKConfig: config.SDKConfig{RequestLog: true}}, "", nil)
	handler.SetLogDirectory(tmp)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/request-traces?limit=10", nil)
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = req
	handler.GetRequestTraces(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got requestTraceResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Source != "request_logs" {
		t.Fatalf("source = %q, want request_logs", got.Source)
	}
	if len(got.Traces) != 1 {
		t.Fatalf("trace count = %d, want 1", len(got.Traces))
	}
	trace := got.Traces[0]
	if trace.RequestedModel != "client-model" {
		t.Fatalf("requested model = %q", trace.RequestedModel)
	}
	if trace.ResolvedUpstreamModel != "gemini-upstream" {
		t.Fatalf("upstream model = %q", trace.ResolvedUpstreamModel)
	}
	if trace.ResolvedProvider != "gemini" || trace.CredentialType != "oauth" {
		t.Fatalf("provider/type = %q/%q", trace.ResolvedProvider, trace.CredentialType)
	}
	if trace.Status != 429 || trace.FailureCategory != "quota" || trace.FinalResult != "failed" {
		t.Fatalf("status/category/result = %d/%q/%q", trace.Status, trace.FailureCategory, trace.FinalResult)
	}
	if !trace.RawLogAvailable || trace.RawLogID != "trace123" {
		t.Fatalf("raw evidence = %v/%q", trace.RawLogAvailable, trace.RawLogID)
	}
	if len(trace.RetryFallbackPath) != 1 || strings.Contains(trace.RetryFallbackPath[0], "secret") {
		t.Fatalf("fallback path not sanitized: %#v", trace.RetryFallbackPath)
	}
	body := rec.Body.String()
	if strings.Contains(body, "secret") || strings.Contains(body, "oauth-1") {
		t.Fatalf("response leaked credential detail: %s", body)
	}
}

func TestGetRequestTracesFallsBackToMainLog(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tmp := t.TempDir()
	line := `[2026-07-06 22:28:28] [--------] [warn ] [gin_logger.go:102] 401 | 5ms | 127.0.0.1 | POST "/v0/management/provider-doctor/check"`
	if err := os.WriteFile(filepath.Join(tmp, defaultLogFileName), []byte(line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	handler := NewHandler(&config.Config{LoggingToFile: true}, "", nil)
	handler.SetLogDirectory(tmp)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/request-traces", nil)
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = req
	handler.GetRequestTraces(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got requestTraceResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Source != "main_log" {
		t.Fatalf("source = %q, want main_log", got.Source)
	}
	if len(got.Traces) != 1 {
		t.Fatalf("trace count = %d, want 1", len(got.Traces))
	}
	trace := got.Traces[0]
	if trace.Status != 401 || trace.FailureCategory != "auth" || trace.Method != "POST" {
		t.Fatalf("trace = %#v", trace)
	}
	if trace.RawLogAvailable {
		t.Fatalf("main log fallback should not advertise a request-log download")
	}
}

func TestGetRequestTracesLoggingDisabledReturnsGuidance(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := NewHandler(&config.Config{LoggingToFile: false}, "", nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/request-traces", nil)
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = req
	handler.GetRequestTraces(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got requestTraceResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.FileLogging || got.Message == "" || len(got.Traces) != 0 {
		t.Fatalf("unexpected disabled response: %#v", got)
	}
}

func TestCollectRequestTraceLogFilesSortsByModifiedTime(t *testing.T) {
	tmp := t.TempDir()
	older := filepath.Join(tmp, "v1-chat-2026-07-07T010203-old.log")
	newer := filepath.Join(tmp, "error-2026-07-07T010204-new.log")
	if err := os.WriteFile(older, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newer, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-time.Hour)
	newTime := time.Now()
	if err := os.Chtimes(older, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(newer, newTime, newTime); err != nil {
		t.Fatal(err)
	}

	files, err := collectRequestTraceLogFiles(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || files[0].name != filepath.Base(newer) {
		t.Fatalf("files = %#v", files)
	}
}
