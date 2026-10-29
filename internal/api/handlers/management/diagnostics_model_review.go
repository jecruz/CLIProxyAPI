package management

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

type diagnosticsModelReviewResponse struct {
	GeneratedAt       string                    `json:"generatedAt"`
	RedactionNotice   string                    `json:"redactionNotice"`
	Snapshot          diagnosticsReviewSnapshot `json:"snapshot"`
	ModelPrompt       string                    `json:"modelPrompt"`
	ModelPromptReady  bool                      `json:"modelPromptReady"`
	SuggestedSections []string                  `json:"suggestedSections"`
}

type diagnosticsModelReviewAuditRequest struct {
	Model         string `json:"model"`
	GeneratedAt   string `json:"generatedAt"`
	ModelPrompt   string `json:"modelPrompt"`
	ModelResponse string `json:"modelResponse"`
	Approved      bool   `json:"approved"`
}

type diagnosticsModelReviewAuditResponse struct {
	Recorded        bool     `json:"recorded"`
	AuditPath       string   `json:"auditPath,omitempty"`
	Validated       bool     `json:"validated"`
	MissingSections []string `json:"missingSections,omitempty"`
}

type diagnosticsModelReviewAuditRecord struct {
	RecordedAt      string   `json:"recordedAt"`
	GeneratedAt     string   `json:"generatedAt,omitempty"`
	Model           string   `json:"model"`
	Approved        bool     `json:"approved"`
	Validated       bool     `json:"validated"`
	MissingSections []string `json:"missingSections,omitempty"`
	PromptSHA256    string   `json:"promptSha256"`
	ResponseSHA256  string   `json:"responseSha256"`
	Prompt          string   `json:"prompt"`
	Response        string   `json:"response"`
}

type diagnosticsReviewSnapshot struct {
	Runtime       diagnosticsReviewRuntime       `json:"runtime"`
	ConfigCompare *configCompareResponse         `json:"configCompare,omitempty"`
	Providers     *providerDoctorResponse        `json:"providers,omitempty"`
	RequestTraces *diagnosticsReviewTraceSummary `json:"requestTraces,omitempty"`
}

type diagnosticsReviewRuntime struct {
	Product            string `json:"product"`
	StaticAssetVersion string `json:"staticAssetVersion,omitempty"`
	ConfigPath         string `json:"configPath,omitempty"`
	StartedAt          string `json:"startedAt,omitempty"`
	Uptime             string `json:"uptime,omitempty"`
}

type diagnosticsReviewTraceSummary struct {
	Source         string              `json:"source"`
	RequestLogging bool                `json:"requestLogging"`
	FileLogging    bool                `json:"fileLogging"`
	Message        string              `json:"message,omitempty"`
	Groups         []requestTraceGroup `json:"groups"`
	Recent         []requestTraceEntry `json:"recent"`
}

// GetDiagnosticsModelReview returns a redacted diagnostics prompt for optional model review.
func (h *Handler) GetDiagnosticsModelReview(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler unavailable"})
		return
	}
	if h.cfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "configuration unavailable"})
		return
	}

	response := h.buildDiagnosticsModelReview()
	c.JSON(http.StatusOK, response)
}

// PostDiagnosticsModelReviewAudit records an approved model-assisted diagnostics review.
func (h *Handler) PostDiagnosticsModelReviewAudit(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler unavailable"})
		return
	}
	if h.cfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "configuration unavailable"})
		return
	}

	var req diagnosticsModelReviewAuditRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request", "message": err.Error()})
		return
	}
	if !req.Approved {
		c.JSON(http.StatusBadRequest, gin.H{"error": "approval_required", "message": "model review audit requires explicit operator approval"})
		return
	}
	if strings.TrimSpace(req.Model) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "model_required", "message": "model is required"})
		return
	}
	if strings.TrimSpace(req.ModelPrompt) == "" || strings.TrimSpace(req.ModelResponse) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "payload_required", "message": "model prompt and response are required"})
		return
	}

	record, response := buildDiagnosticsModelReviewAuditRecord(req)
	path, err := h.appendDiagnosticsModelReviewAudit(record)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "audit_write_failed", "message": err.Error()})
		return
	}
	response.Recorded = true
	response.AuditPath = path
	c.JSON(http.StatusOK, response)
}

func (h *Handler) buildDiagnosticsModelReview() diagnosticsModelReviewResponse {
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

	snapshot := diagnosticsReviewSnapshot{
		Runtime: diagnosticsReviewRuntime{
			Product:            "LLM Dynamix",
			StaticAssetVersion: staticVersion,
			ConfigPath:         h.configFilePath,
			StartedAt:          h.startedAt.Format(time.RFC3339),
			Uptime:             formatRuntimeUptime(time.Since(h.startedAt)),
		},
		ConfigCompare: &configCompare,
		Providers:     &providers,
		RequestTraces: h.diagnosticsReviewTraceSummary(8),
	}

	response := diagnosticsModelReviewResponse{
		GeneratedAt:       time.Now().UTC().Format(time.RFC3339),
		RedactionNotice:   "This payload is generated from redacted diagnostics only. Review it before approving model analysis. Secrets, token-like query values, Authorization headers, and raw credentials are not included.",
		Snapshot:          snapshot,
		SuggestedSections: []string{"Evidence", "Likely Cause", "Suggested Fix", "Confidence"},
	}
	response.ModelPrompt = buildDiagnosticsModelPrompt(snapshot)
	response.ModelPromptReady = strings.TrimSpace(response.ModelPrompt) != ""
	return response
}

func buildDiagnosticsModelReviewAuditRecord(req diagnosticsModelReviewAuditRequest) (diagnosticsModelReviewAuditRecord, diagnosticsModelReviewAuditResponse) {
	prompt := sanitizeReviewValue(req.ModelPrompt)
	responseText := sanitizeReviewValue(req.ModelResponse)
	missing := missingDiagnosticsModelReviewSections(responseText)
	record := diagnosticsModelReviewAuditRecord{
		RecordedAt:      time.Now().UTC().Format(time.RFC3339),
		GeneratedAt:     strings.TrimSpace(req.GeneratedAt),
		Model:           sanitizeReviewValue(strings.TrimSpace(req.Model)),
		Approved:        req.Approved,
		Validated:       len(missing) == 0,
		MissingSections: missing,
		PromptSHA256:    sha256Hex(prompt),
		ResponseSHA256:  sha256Hex(responseText),
		Prompt:          prompt,
		Response:        responseText,
	}
	return record, diagnosticsModelReviewAuditResponse{
		Validated:       record.Validated,
		MissingSections: missing,
	}
}

func (h *Handler) appendDiagnosticsModelReviewAudit(record diagnosticsModelReviewAuditRecord) (string, error) {
	dir := h.logDirectory()
	if strings.TrimSpace(dir) == "" {
		return "", fmt.Errorf("log directory not configured")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create log directory: %w", err)
	}
	path := filepath.Join(dir, "diagnostics-model-review-audit.jsonl")
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

func missingDiagnosticsModelReviewSections(text string) []string {
	lower := strings.ToLower(text)
	required := []string{"evidence", "likely cause", "suggested fix", "confidence"}
	missing := make([]string, 0, len(required))
	for _, section := range required {
		if !strings.Contains(lower, section) {
			missing = append(missing, sectionTitle(section))
		}
	}
	return missing
}

func sectionTitle(section string) string {
	switch section {
	case "evidence":
		return "Evidence"
	case "likely cause":
		return "Likely Cause"
	case "suggested fix":
		return "Suggested Fix"
	case "confidence":
		return "Confidence"
	default:
		return section
	}
}

func sha256Hex(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func (h *Handler) diagnosticsReviewTraceSummary(limit int) *diagnosticsReviewTraceSummary {
	if h == nil || h.cfg == nil {
		return nil
	}
	summary := &diagnosticsReviewTraceSummary{
		Source:         "unavailable",
		RequestLogging: h.cfg.RequestLog,
		FileLogging:    h.cfg.LoggingToFile,
		Groups:         []requestTraceGroup{},
		Recent:         []requestTraceEntry{},
	}
	if !h.cfg.LoggingToFile {
		summary.Message = "File logging is disabled."
		return summary
	}
	logDir := h.logDirectory()
	if strings.TrimSpace(logDir) == "" {
		summary.Message = "Log directory is not configured."
		return summary
	}
	requestLogs, err := collectRequestTraceLogFiles(logDir)
	if err == nil && len(requestLogs) > 0 {
		summary.Source = "request_logs"
		for _, file := range requestLogs {
			if len(summary.Recent) >= limit {
				break
			}
			text, errRead := readRequestTraceLog(file.path)
			if errRead != nil {
				continue
			}
			summary.Recent = append(summary.Recent, sanitizeDiagnosticsTraceEntry(parseRequestTraceFile(file, text)))
		}
		summary.Groups = groupRequestTraceFailures(summary.Recent)
		return summary
	}

	traces, errFallback := h.requestTraceMainLogFallback(logDir, limit)
	if errFallback != nil {
		summary.Message = "Request trace fallback was unavailable."
		return summary
	}
	summary.Source = "main_log"
	for i := range traces {
		traces[i] = sanitizeDiagnosticsTraceEntry(traces[i])
	}
	summary.Recent = traces
	summary.Groups = groupRequestTraceFailures(traces)
	if len(traces) == 0 {
		summary.Message = "No recent request trace entries are available."
	}
	return summary
}

func sanitizeDiagnosticsTraceEntry(entry requestTraceEntry) requestTraceEntry {
	entry.ID = sanitizeReviewValue(entry.ID)
	entry.Timestamp = sanitizeReviewValue(entry.Timestamp)
	entry.Method = sanitizeReviewValue(entry.Method)
	entry.Path = sanitizeReviewValue(entry.Path)
	entry.Latency = sanitizeReviewValue(entry.Latency)
	entry.IP = sanitizeReviewValue(entry.IP)
	entry.RequestedModel = sanitizeReviewValue(entry.RequestedModel)
	entry.ResolvedProvider = sanitizeReviewValue(entry.ResolvedProvider)
	entry.ResolvedUpstreamModel = sanitizeReviewValue(entry.ResolvedUpstreamModel)
	entry.CredentialType = sanitizeReviewValue(entry.CredentialType)
	entry.FailureCategory = sanitizeReviewValue(entry.FailureCategory)
	entry.FinalResult = sanitizeReviewValue(entry.FinalResult)
	entry.RawLogName = sanitizeReviewValue(entry.RawLogName)
	entry.RawLogID = sanitizeReviewValue(entry.RawLogID)
	for i := range entry.RetryFallbackPath {
		entry.RetryFallbackPath[i] = sanitizeReviewValue(entry.RetryFallbackPath[i])
	}
	for i := range entry.Evidence {
		entry.Evidence[i] = sanitizeReviewValue(entry.Evidence[i])
	}
	return entry
}

func buildDiagnosticsModelPrompt(snapshot diagnosticsReviewSnapshot) string {
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("You are reviewing a redacted LLM Dynamix diagnostics snapshot. ")
	b.WriteString("Use only the evidence below. Do not assume secrets, hidden config, or provider state that is not present.\n\n")
	b.WriteString("Return exactly these sections:\n")
	b.WriteString("1. Evidence\n2. Likely Cause\n3. Suggested Fix\n4. Confidence\n\n")
	b.WriteString("Rules:\n")
	b.WriteString("- Do not suggest writing config unless the evidence supports it.\n")
	b.WriteString("- Separate process/runtime problems from provider auth, route/model mismatch, stale config, and upstream failures.\n")
	b.WriteString("- If evidence is insufficient, say what specific diagnostic data is missing.\n\n")
	b.WriteString("Redacted diagnostics snapshot:\n")
	b.Write(data)
	return sanitizeReviewValue(b.String())
}
