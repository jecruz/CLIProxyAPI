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

func TestGetDiagnosticsRepairActionsTargetsDevRuntime(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tmp := t.TempDir()
	handler := NewHandler(&config.Config{
		AuthDir: filepath.Join(tmp, ".llmdynamix-dev", ".llmdynamix"),
	}, filepath.Join(tmp, ".llmdynamix-dev", ".llmdynamix", "merged-config.yaml"), nil)
	handler.SetLogDirectory(filepath.Join(tmp, ".llmdynamix-dev", ".llmdynamix", "logs"))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/diagnostics/repair-actions", nil)
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = req
	handler.GetDiagnosticsRepairActions(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got diagnosticsRepairActionsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Runtime != "dev" {
		t.Fatalf("runtime = %q, want dev", got.Runtime)
	}
	restart := findRepairAction(got.Actions, "restart-dev-backend")
	if restart == nil || !restart.Enabled {
		t.Fatalf("restart-dev-backend not enabled for dev: %#v", restart)
	}
	if !strings.Contains(strings.Join(restart.Targets, " "), "12455") {
		t.Fatalf("restart action did not name dev backend port: %#v", restart)
	}
}

func TestGetDiagnosticsRepairActionsBlocksDevRestartForProductionRuntime(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tmp := t.TempDir()
	handler := NewHandler(&config.Config{
		AuthDir: filepath.Join(tmp, ".llmdynamix"),
	}, filepath.Join(tmp, ".llmdynamix", "merged-config.yaml"), nil)

	got := handler.diagnosticsRepairActions()
	if got.Runtime != "production" {
		t.Fatalf("runtime = %q, want production", got.Runtime)
	}
	restart := findRepairAction(got.Actions, "restart-dev-backend")
	if restart == nil {
		t.Fatal("restart-dev-backend action missing")
	}
	if restart.Enabled || restart.Status != "blocked_for_runtime" {
		t.Fatalf("restart action should be blocked for production: %#v", restart)
	}
	if !strings.Contains(restart.Confirmation, "12444") || !strings.Contains(restart.Confirmation, "12445") {
		t.Fatalf("confirmation should name production ports to avoid: %q", restart.Confirmation)
	}
}

func TestPostDiagnosticsRepairActionRequiresExactConfirmation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tmp := t.TempDir()
	handler := NewHandler(&config.Config{
		AuthDir: filepath.Join(tmp, ".llmdynamix-dev", ".llmdynamix"),
	}, filepath.Join(tmp, ".llmdynamix-dev", ".llmdynamix", "merged-config.yaml"), nil)
	handler.SetLogDirectory(filepath.Join(tmp, "logs"))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/diagnostics/repair-actions/revalidate-management-key", bytes.NewBufferString(`{"confirmation":"wrong"}`))
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = req
	ctx.Params = gin.Params{{Key: "id", Value: "revalidate-management-key"}}
	handler.PostDiagnosticsRepairAction(ctx)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got diagnosticsRepairActionExecuteResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Executed || got.Status != "confirmation_required" {
		t.Fatalf("unexpected response: %#v", got)
	}
	audit, err := os.ReadFile(filepath.Join(tmp, "logs", "diagnostics-repair-actions-audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(audit), `"confirmationMatched":false`) {
		t.Fatalf("audit did not record failed confirmation: %s", audit)
	}
}

func TestPostDiagnosticsRepairActionRevalidatesManagementKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tmp := t.TempDir()
	handler := NewHandler(&config.Config{
		AuthDir: filepath.Join(tmp, ".llmdynamix-dev", ".llmdynamix"),
		RemoteManagement: config.RemoteManagement{
			SecretKey: "hashed-management-key-present",
		},
	}, filepath.Join(tmp, ".llmdynamix-dev", ".llmdynamix", "merged-config.yaml"), nil)
	handler.SetLogDirectory(filepath.Join(tmp, "logs"))
	action := findRepairAction(handler.diagnosticsRepairActions().Actions, "revalidate-management-key")
	if action == nil {
		t.Fatal("revalidate-management-key action missing")
	}

	rec := httptest.NewRecorder()
	body, _ := json.Marshal(diagnosticsRepairActionExecuteRequest{Confirmation: action.Confirmation})
	req := httptest.NewRequest(http.MethodPost, "/diagnostics/repair-actions/revalidate-management-key", bytes.NewReader(body))
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = req
	ctx.Params = gin.Params{{Key: "id", Value: "revalidate-management-key"}}
	handler.PostDiagnosticsRepairAction(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got diagnosticsRepairActionExecuteResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Executed || got.Status != "management_key_valid" {
		t.Fatalf("unexpected response: %#v", got)
	}
	if got.Result["authenticated"] != true || got.Result["managementKeyConfigured"] != true {
		t.Fatalf("unexpected result: %#v", got.Result)
	}
	if got.AuditPath == "" {
		t.Fatal("expected audit path")
	}
}

func TestPostDiagnosticsRepairActionBlocksProductionDevRestart(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tmp := t.TempDir()
	handler := NewHandler(&config.Config{
		AuthDir: filepath.Join(tmp, ".llmdynamix"),
	}, filepath.Join(tmp, ".llmdynamix", "merged-config.yaml"), nil)
	handler.SetLogDirectory(filepath.Join(tmp, "logs"))
	action := findRepairAction(handler.diagnosticsRepairActions().Actions, "restart-dev-backend")
	if action == nil {
		t.Fatal("restart-dev-backend action missing")
	}

	rec := httptest.NewRecorder()
	body, _ := json.Marshal(diagnosticsRepairActionExecuteRequest{Confirmation: action.Confirmation})
	req := httptest.NewRequest(http.MethodPost, "/diagnostics/repair-actions/restart-dev-backend", bytes.NewReader(body))
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = req
	ctx.Params = gin.Params{{Key: "id", Value: "restart-dev-backend"}}
	handler.PostDiagnosticsRepairAction(ctx)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got diagnosticsRepairActionExecuteResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Executed || got.Status != "blocked_for_runtime" {
		t.Fatalf("restart action should be blocked: %#v", got)
	}
}

func TestPostDiagnosticsRepairActionDevRestartReturnsAppControlHandoff(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tmp := t.TempDir()
	handler := NewHandler(&config.Config{
		AuthDir: filepath.Join(tmp, ".llmdynamix-dev", ".llmdynamix"),
	}, filepath.Join(tmp, ".llmdynamix-dev", ".llmdynamix", "merged-config.yaml"), nil)
	handler.SetLogDirectory(filepath.Join(tmp, "logs"))
	action := findRepairAction(handler.diagnosticsRepairActions().Actions, "restart-dev-backend")
	if action == nil {
		t.Fatal("restart-dev-backend action missing")
	}

	rec := httptest.NewRecorder()
	body, _ := json.Marshal(diagnosticsRepairActionExecuteRequest{Confirmation: action.Confirmation})
	req := httptest.NewRequest(http.MethodPost, "/diagnostics/repair-actions/restart-dev-backend", bytes.NewReader(body))
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = req
	ctx.Params = gin.Params{{Key: "id", Value: "restart-dev-backend"}}
	handler.PostDiagnosticsRepairAction(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got diagnosticsRepairActionExecuteResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Executed || got.Status != "app_control_required" {
		t.Fatalf("restart action should require app control handoff: %#v", got)
	}
	if got.Result["appControlPath"] != "/_llmdynamix/dev/restart-backend" {
		t.Fatalf("unexpected app control result: %#v", got.Result)
	}
}

func TestPostDiagnosticsRepairActionRebuildConfigReturnsAppControlHandoff(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tmp := t.TempDir()
	handler := NewHandler(&config.Config{
		AuthDir: filepath.Join(tmp, ".llmdynamix-dev", ".llmdynamix"),
	}, filepath.Join(tmp, ".llmdynamix-dev", ".llmdynamix", "merged-config.yaml"), nil)
	handler.SetLogDirectory(filepath.Join(tmp, "logs"))
	action := findRepairAction(handler.diagnosticsRepairActions().Actions, "rebuild-effective-config")
	if action == nil {
		t.Fatal("rebuild-effective-config action missing")
	}

	rec := httptest.NewRecorder()
	body, _ := json.Marshal(diagnosticsRepairActionExecuteRequest{Confirmation: action.Confirmation})
	req := httptest.NewRequest(http.MethodPost, "/diagnostics/repair-actions/rebuild-effective-config", bytes.NewReader(body))
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = req
	ctx.Params = gin.Params{{Key: "id", Value: "rebuild-effective-config"}}
	handler.PostDiagnosticsRepairAction(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got diagnosticsRepairActionExecuteResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Executed || got.Status != "app_control_required" {
		t.Fatalf("rebuild action should require app control handoff: %#v", got)
	}
	if got.Result["appControlPath"] != "/_llmdynamix/dev/rebuild-effective-config" {
		t.Fatalf("unexpected app control result: %#v", got.Result)
	}
}

func TestPostDiagnosticsRepairActionSupportBundleIsRedacted(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tmp := t.TempDir()
	handler := NewHandler(&config.Config{
		AuthDir: filepath.Join(tmp, ".llmdynamix-dev", ".llmdynamix"),
		RemoteManagement: config.RemoteManagement{
			SecretKey: "management-secret-one",
		},
		GeminiKey: []config.GeminiKey{
			{
				APIKey: "gemini-secret-one",
				Models: []config.GeminiModel{
					{Name: "gemini-test"},
				},
			},
		},
	}, filepath.Join(tmp, ".llmdynamix-dev", ".llmdynamix", "merged-config.yaml"), nil)
	handler.SetLogDirectory(filepath.Join(tmp, "logs"))
	action := findRepairAction(handler.diagnosticsRepairActions().Actions, "copy-support-bundle")
	if action == nil {
		t.Fatal("copy-support-bundle action missing")
	}

	rec := httptest.NewRecorder()
	body, _ := json.Marshal(diagnosticsRepairActionExecuteRequest{Confirmation: action.Confirmation})
	req := httptest.NewRequest(http.MethodPost, "/diagnostics/repair-actions/copy-support-bundle", bytes.NewReader(body))
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = req
	ctx.Params = gin.Params{{Key: "id", Value: "copy-support-bundle"}}
	handler.PostDiagnosticsRepairAction(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	bodyText := rec.Body.String()
	for _, secret := range []string{"management-secret-one", "gemini-secret-one"} {
		if strings.Contains(bodyText, secret) {
			t.Fatalf("support bundle response leaked %q: %s", secret, bodyText)
		}
	}
	var got diagnosticsRepairActionExecuteResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	path, ok := got.Result["path"].(string)
	if !ok || path == "" {
		t.Fatalf("expected support bundle path: %#v", got.Result)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"management-secret-one", "gemini-secret-one"} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("support bundle file leaked %q: %s", secret, data)
		}
	}
}

func findRepairAction(actions []diagnosticsRepairAction, id string) *diagnosticsRepairAction {
	for i := range actions {
		if actions[i].ID == id {
			return &actions[i]
		}
	}
	return nil
}
