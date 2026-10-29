package management

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

type diagnosticsRepairActionsResponse struct {
	Runtime string                    `json:"runtime"`
	Actions []diagnosticsRepairAction `json:"actions"`
}

type diagnosticsRepairAction struct {
	ID                   string   `json:"id"`
	Label                string   `json:"label"`
	Category             string   `json:"category"`
	Risk                 string   `json:"risk"`
	Description          string   `json:"description"`
	Enabled              bool     `json:"enabled"`
	RequiresConfirmation bool     `json:"requiresConfirmation"`
	Confirmation         string   `json:"confirmation"`
	Effects              []string `json:"effects"`
	Targets              []string `json:"targets"`
	Status               string   `json:"status"`
}

type diagnosticsRepairActionExecuteRequest struct {
	Confirmation string `json:"confirmation"`
}

type diagnosticsRepairActionExecuteResponse struct {
	ID        string         `json:"id"`
	Executed  bool           `json:"executed"`
	Status    string         `json:"status"`
	Message   string         `json:"message"`
	Runtime   string         `json:"runtime"`
	Effects   []string       `json:"effects,omitempty"`
	Targets   []string       `json:"targets,omitempty"`
	Result    map[string]any `json:"result,omitempty"`
	AuditPath string         `json:"auditPath,omitempty"`
}

type diagnosticsRepairActionAuditRecord struct {
	RecordedAt          string         `json:"recordedAt"`
	ID                  string         `json:"id"`
	Label               string         `json:"label"`
	Runtime             string         `json:"runtime"`
	Risk                string         `json:"risk"`
	ConfirmationMatched bool           `json:"confirmationMatched"`
	Status              string         `json:"status"`
	Message             string         `json:"message"`
	Effects             []string       `json:"effects,omitempty"`
	Targets             []string       `json:"targets,omitempty"`
	Result              map[string]any `json:"result,omitempty"`
}

// GetDiagnosticsRepairActions returns a read-only inventory of safe operator actions.
func (h *Handler) GetDiagnosticsRepairActions(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler unavailable"})
		return
	}
	if h.cfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "configuration unavailable"})
		return
	}
	c.JSON(http.StatusOK, h.diagnosticsRepairActions())
}

// PostDiagnosticsRepairAction executes one explicitly confirmed safe diagnostics action.
func (h *Handler) PostDiagnosticsRepairAction(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler unavailable"})
		return
	}
	if h.cfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "configuration unavailable"})
		return
	}

	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "action_required", "message": "repair action id is required"})
		return
	}
	var req diagnosticsRepairActionExecuteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request", "message": err.Error()})
		return
	}

	registry := h.diagnosticsRepairActions()
	action := findDiagnosticsRepairAction(registry.Actions, id)
	if action == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "action_not_found", "message": "repair action not found"})
		return
	}
	if !action.Enabled {
		response := diagnosticsRepairActionExecuteResponse{
			ID:      action.ID,
			Status:  action.Status,
			Message: "Repair action is blocked for this runtime.",
			Runtime: registry.Runtime,
			Effects: action.Effects,
			Targets: action.Targets,
		}
		c.JSON(http.StatusConflict, response)
		return
	}

	confirmationMatched := !action.RequiresConfirmation || strings.TrimSpace(req.Confirmation) == strings.TrimSpace(action.Confirmation)
	if !confirmationMatched {
		response := diagnosticsRepairActionExecuteResponse{
			ID:      action.ID,
			Status:  "confirmation_required",
			Message: "Confirmation text must exactly match the action confirmation.",
			Runtime: registry.Runtime,
			Effects: action.Effects,
			Targets: action.Targets,
		}
		_, _ = h.appendDiagnosticsRepairActionAudit(diagnosticsRepairActionAuditRecord{
			RecordedAt:          time.Now().UTC().Format(time.RFC3339),
			ID:                  action.ID,
			Label:               action.Label,
			Runtime:             registry.Runtime,
			Risk:                action.Risk,
			ConfirmationMatched: false,
			Status:              response.Status,
			Message:             response.Message,
			Effects:             action.Effects,
			Targets:             action.Targets,
		})
		c.JSON(http.StatusBadRequest, response)
		return
	}

	response, statusCode := h.executeDiagnosticsRepairAction(registry.Runtime, *action)
	record := diagnosticsRepairActionAuditRecord{
		RecordedAt:          time.Now().UTC().Format(time.RFC3339),
		ID:                  action.ID,
		Label:               action.Label,
		Runtime:             registry.Runtime,
		Risk:                action.Risk,
		ConfirmationMatched: true,
		Status:              response.Status,
		Message:             response.Message,
		Effects:             response.Effects,
		Targets:             response.Targets,
		Result:              summarizeDiagnosticsRepairActionResult(response.Result),
	}
	if path, err := h.appendDiagnosticsRepairActionAudit(record); err == nil {
		response.AuditPath = path
	} else if statusCode < http.StatusInternalServerError {
		response.Status = "audit_write_failed"
		response.Message = err.Error()
		statusCode = http.StatusInternalServerError
	}
	c.JSON(statusCode, response)
}

func (h *Handler) diagnosticsRepairActions() diagnosticsRepairActionsResponse {
	runtime := h.detectDiagnosticsRuntime()
	isDev := runtime == "dev"
	configPath := strings.TrimSpace(h.configFilePath)
	configDir := ""
	if configPath != "" {
		configDir = filepath.Dir(configPath)
	}
	logDir := h.logDirectory()
	targetPorts := []string{"proxy port 12454", "backend port 12455"}
	if !isDev {
		targetPorts = []string{"production proxy port 12444", "production backend port 12445"}
	}

	actions := []diagnosticsRepairAction{
		{
			ID:                   "refresh-model-inventory",
			Label:                "Refresh Model Inventory",
			Category:             "provider",
			Risk:                 "low",
			Description:          "Reload model inventory using existing provider and OAuth state.",
			Enabled:              true,
			RequiresConfirmation: true,
			Confirmation:         "Refresh model inventory for the active runtime. This does not edit config or restart services.",
			Effects:              []string{"Re-queries local model inventory endpoints", "Updates in-memory model availability"},
			Targets:              targetPorts,
			Status:               "planned",
		},
		{
			ID:                   "clear-console-login-state",
			Label:                "Clear Console Login State",
			Category:             "auth",
			Risk:                 "low",
			Description:          "Clear browser-side management console login state so the app can inject the current key again.",
			Enabled:              true,
			RequiresConfirmation: true,
			Confirmation:         "Clear management console login state in this browser session. This does not change the backend management key.",
			Effects:              []string{"Clears browser-local console authentication cache", "Forces re-authentication through the app-managed key handoff"},
			Targets:              []string{"management.html local storage", "current browser session"},
			Status:               "planned",
		},
		{
			ID:                   "open-config-folder",
			Label:                "Open Config Folder",
			Category:             "support",
			Risk:                 "low",
			Description:          "Open the active runtime config folder for inspection.",
			Enabled:              configDir != "",
			RequiresConfirmation: true,
			Confirmation:         "Open the active runtime config folder in Finder.",
			Effects:              []string{"Opens Finder only", "Does not modify files"},
			Targets:              nonEmptyStrings(configDir),
			Status:               "planned",
		},
		{
			ID:                   "open-logs-folder",
			Label:                "Open Logs Folder",
			Category:             "support",
			Risk:                 "low",
			Description:          "Open the active runtime logs folder for inspection.",
			Enabled:              logDir != "",
			RequiresConfirmation: true,
			Confirmation:         "Open the active runtime logs folder in Finder.",
			Effects:              []string{"Opens Finder only", "Does not modify files"},
			Targets:              nonEmptyStrings(logDir),
			Status:               "planned",
		},
		{
			ID:                   "copy-support-bundle",
			Label:                "Copy Support Bundle",
			Category:             "support",
			Risk:                 "low",
			Description:          "Prepare a redacted diagnostics bundle for support or Redmine notes.",
			Enabled:              true,
			RequiresConfirmation: true,
			Confirmation:         "Create a local redacted support bundle from diagnostics, config summary, provider state, and logs.",
			Effects:              []string{"Copies redacted diagnostics only", "Does not include raw API keys, OAuth tokens, or management keys"},
			Targets:              append(nonEmptyStrings(configPath, logDir), "clipboard or downloadable JSON"),
			Status:               "planned",
		},
		{
			ID:                   "rebuild-effective-config",
			Label:                "Rebuild Effective Config",
			Category:             "config",
			Risk:                 "medium",
			Description:          "Recompose the active runtime config from app-managed sources.",
			Enabled:              isDev,
			RequiresConfirmation: true,
			Confirmation:         "Rebuild the dev effective config only. This must not touch production storage or ports 12444/12445.",
			Effects:              []string{"Writes the dev merged config", "Does not edit production config"},
			Targets:              nonEmptyStrings(configPath, "dev backend port 12455"),
			Status:               actionStatus(isDev),
		},
		{
			ID:                   "restart-dev-backend",
			Label:                "Restart Dev Backend",
			Category:             "runtime",
			Risk:                 "medium",
			Description:          "Restart only the dev backend process after diagnostics confirms the active runtime is dev.",
			Enabled:              isDev,
			RequiresConfirmation: true,
			Confirmation:         "Restart only LLM Dynamix Dev backend on port 12455. Do not stop production ports 12444 or 12445.",
			Effects:              []string{"Stops and starts dev backend only", "Briefly interrupts dev API traffic"},
			Targets:              []string{"LLM Dynamix Dev.app", "backend port 12455", "proxy port 12454"},
			Status:               actionStatus(isDev),
		},
		{
			ID:                   "revalidate-management-key",
			Label:                "Revalidate Management Key",
			Category:             "auth",
			Risk:                 "low",
			Description:          "Verify the active management key against the active runtime without changing it.",
			Enabled:              true,
			RequiresConfirmation: true,
			Confirmation:         "Revalidate the active management key against the current runtime. This does not rotate or persist a new key.",
			Effects:              []string{"Performs an authenticated management API check", "Does not write config"},
			Targets:              targetPorts,
			Status:               "planned",
		},
	}

	return diagnosticsRepairActionsResponse{
		Runtime: runtime,
		Actions: actions,
	}
}

func (h *Handler) executeDiagnosticsRepairAction(runtime string, action diagnosticsRepairAction) (diagnosticsRepairActionExecuteResponse, int) {
	response := diagnosticsRepairActionExecuteResponse{
		ID:      action.ID,
		Runtime: runtime,
		Effects: action.Effects,
		Targets: action.Targets,
	}
	switch action.ID {
	case "clear-console-login-state":
		response.Executed = true
		response.Status = "browser_action_required"
		response.Message = "Backend audit recorded. The management console should now clear browser-side login storage."
		response.Result = map[string]any{
			"browserStorageKeys":   []string{"cli-proxy-auth", "isLoggedIn", "apiBase", "apiUrl", "managementKey"},
			"requiresBrowserClear": true,
		}
		return response, http.StatusOK
	case "open-config-folder", "open-logs-folder":
		response.Executed = true
		response.Status = "path_ready"
		response.Message = "Path is ready to copy or open from the app shell."
		response.Result = map[string]any{
			"path": firstNonEmpty(action.Targets),
		}
		return response, http.StatusOK
	case "copy-support-bundle":
		bundle, bundlePath, err := h.buildDiagnosticsSupportBundle()
		if err != nil {
			response.Status = "support_bundle_failed"
			response.Message = err.Error()
			return response, http.StatusInternalServerError
		}
		response.Executed = true
		response.Status = "support_bundle_ready"
		response.Message = "Redacted support bundle created locally and returned for copying."
		response.Result = map[string]any{
			"bundle": bundle,
			"path":   bundlePath,
		}
		return response, http.StatusOK
	case "revalidate-management-key":
		response.Executed = true
		response.Status = "management_key_valid"
		response.Message = "The current management request is authenticated against the active runtime."
		response.Result = map[string]any{
			"authenticated":           true,
			"managementKeyConfigured": strings.TrimSpace(h.cfg.RemoteManagement.SecretKey) != "" || strings.TrimSpace(h.envSecret) != "",
		}
		return response, http.StatusOK
	case "refresh-model-inventory":
		response.Executed = true
		response.Status = "browser_refresh_required"
		response.Message = "Backend audit recorded. The management console should refetch model inventory through the active API base."
		response.Result = map[string]any{
			"requiresModelRefresh": true,
		}
		return response, http.StatusOK
	case "rebuild-effective-config":
		response.Executed = false
		response.Status = "app_control_required"
		response.Message = "Backend audit recorded. The LLM Dynamix Dev app must rebuild the effective config through its local control endpoint."
		response.Result = map[string]any{
			"appControlURL":  "http://127.0.0.1:12454/_llmdynamix/dev/rebuild-effective-config",
			"appControlPath": "/_llmdynamix/dev/rebuild-effective-config",
		}
		return response, http.StatusOK
	case "restart-dev-backend":
		response.Executed = false
		response.Status = "app_control_required"
		response.Message = "Backend audit recorded. The LLM Dynamix Dev app must restart the dev backend through its local control endpoint."
		response.Result = map[string]any{
			"appControlURL":  "http://127.0.0.1:12454/_llmdynamix/dev/restart-backend",
			"appControlPath": "/_llmdynamix/dev/restart-backend",
		}
		return response, http.StatusOK
	default:
		response.Status = "not_implemented"
		response.Message = "This repair action requires a separate guarded implementation."
		return response, http.StatusNotImplemented
	}
}

func (h *Handler) buildDiagnosticsSupportBundle() (map[string]any, string, error) {
	if h == nil || h.cfg == nil {
		return nil, "", fmt.Errorf("configuration unavailable")
	}
	staticVersion := managementStaticAssetVersion()
	configCompare := configCompareResponse{
		StaticAssetVersion: staticVersion,
		Sources:            h.configCompareSources(staticVersion),
	}
	configCompare.Differences = compareConfigSources(configCompare.Sources)
	providers := providerDoctorResponse{
		APIProviders:   h.apiProviderDoctorEntries(),
		OAuthProviders: h.oauthProviderDoctorEntries(),
	}
	bundle := map[string]any{
		"generatedAt":     time.Now().UTC().Format(time.RFC3339),
		"redactionNotice": "Generated from redacted diagnostics only. Raw API keys, OAuth tokens, and management keys are not included.",
		"runtime": map[string]any{
			"product":            "LLM Dynamix",
			"detectedRuntime":    h.detectDiagnosticsRuntime(),
			"staticAssetVersion": staticVersion,
			"configPath":         h.configFilePath,
			"startedAt":          h.startedAt.Format(time.RFC3339),
			"uptime":             formatRuntimeUptime(time.Since(h.startedAt)),
		},
		"configCompare": configCompare,
		"providers":     providers,
		"requestTraces": h.diagnosticsReviewTraceSummary(8),
		"repairActions": h.diagnosticsRepairActions(),
	}

	dir := h.logDirectory()
	if strings.TrimSpace(dir) == "" {
		return bundle, "", fmt.Errorf("log directory not configured")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return bundle, "", fmt.Errorf("create log directory: %w", err)
	}
	path := filepath.Join(dir, "diagnostics-support-bundle.json")
	data, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		return bundle, "", fmt.Errorf("encode support bundle: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return bundle, "", fmt.Errorf("write support bundle: %w", err)
	}
	return bundle, path, nil
}

func (h *Handler) appendDiagnosticsRepairActionAudit(record diagnosticsRepairActionAuditRecord) (string, error) {
	dir := h.logDirectory()
	if strings.TrimSpace(dir) == "" {
		return "", fmt.Errorf("log directory not configured")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create log directory: %w", err)
	}
	path := filepath.Join(dir, "diagnostics-repair-actions-audit.jsonl")
	data, err := json.Marshal(record)
	if err != nil {
		return "", fmt.Errorf("encode audit record: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return "", fmt.Errorf("open audit log: %w", err)
	}
	defer func() { _ = file.Close() }()
	if _, err := file.Write(append(data, '\n')); err != nil {
		return "", fmt.Errorf("write audit log: %w", err)
	}
	return path, nil
}

func findDiagnosticsRepairAction(actions []diagnosticsRepairAction, id string) *diagnosticsRepairAction {
	for i := range actions {
		if actions[i].ID == id {
			return &actions[i]
		}
	}
	return nil
}

func summarizeDiagnosticsRepairActionResult(result map[string]any) map[string]any {
	if len(result) == 0 {
		return nil
	}
	summary := make(map[string]any, len(result))
	for key, value := range result {
		switch key {
		case "bundle":
			if bundle, ok := value.(map[string]any); ok {
				summary[key] = map[string]any{
					"generatedAt": bundle["generatedAt"],
					"sections":    []string{"runtime", "configCompare", "providers", "requestTraces", "repairActions"},
				}
			} else {
				summary[key] = "included"
			}
		default:
			summary[key] = value
		}
	}
	return summary
}

func (h *Handler) detectDiagnosticsRuntime() string {
	path := strings.ToLower(strings.TrimSpace(h.configFilePath))
	authDir := ""
	if h != nil && h.cfg != nil {
		authDir = strings.ToLower(strings.TrimSpace(h.cfg.AuthDir))
	}
	switch {
	case strings.Contains(path, ".llmdynamix-dev") || strings.Contains(authDir, ".llmdynamix-dev"):
		return "dev"
	case strings.Contains(path, ".llmdynamix") || strings.Contains(authDir, ".llmdynamix"):
		return "production"
	default:
		return "unknown"
	}
}

func firstNonEmpty(values []string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func actionStatus(enabled bool) string {
	if enabled {
		return "planned"
	}
	return "blocked_for_runtime"
}

func nonEmptyStrings(values ...string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}
