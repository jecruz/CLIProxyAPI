package management

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

const maxLogReviewBytes = 1024 * 1024

var logReviewSecretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(api[_-]?key|access[_-]?token|refresh[_-]?token|token|key|secret)=([^&\s"'<>]+)`),
	regexp.MustCompile(`(?i)(authorization:\s*bearer\s+)[^\s]+`),
}

type logReviewResponse struct {
	Name             string   `json:"name"`
	Severity         string   `json:"severity"`
	Category         string   `json:"category"`
	Summary          string   `json:"summary"`
	LikelyCause      string   `json:"likelyCause"`
	Evidence         []string `json:"evidence"`
	NextActions      []string `json:"nextActions"`
	RedactionNotice  string   `json:"redactionNotice"`
	RedactedLog      string   `json:"redactedLog"`
	ModelPrompt      string   `json:"modelPrompt"`
	ModelPromptReady bool     `json:"modelPromptReady"`
}

// GetRequestErrorLogReview returns a local, redacted review of a request error log.
func (h *Handler) GetRequestErrorLogReview(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler unavailable"})
		return
	}
	if h.cfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "configuration unavailable"})
		return
	}

	name := strings.TrimSpace(c.Param("name"))
	text, err := h.readRequestErrorLog(name)
	if err != nil {
		status := http.StatusInternalServerError
		switch err {
		case errInvalidLogFileName:
			status = http.StatusBadRequest
		case errLogFileNotFound:
			status = http.StatusNotFound
		}
		c.JSON(status, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, reviewRequestErrorLog(name, text))
}

var (
	errInvalidLogFileName = fmt.Errorf("invalid log file name")
	errLogFileNotFound    = fmt.Errorf("log file not found")
)

func (h *Handler) readRequestErrorLog(name string) (string, error) {
	if name == "" || strings.Contains(name, "/") || strings.Contains(name, "\\") {
		return "", errInvalidLogFileName
	}
	if !strings.HasPrefix(name, "error-") || !strings.HasSuffix(name, ".log") {
		return "", errLogFileNotFound
	}

	dir := h.logDirectory()
	if strings.TrimSpace(dir) == "" {
		return "", fmt.Errorf("log directory not configured")
	}
	dirAbs, errAbs := filepath.Abs(dir)
	if errAbs != nil {
		return "", fmt.Errorf("failed to resolve log directory: %v", errAbs)
	}
	fullPath := filepath.Clean(filepath.Join(dirAbs, name))
	prefix := dirAbs + string(os.PathSeparator)
	if !strings.HasPrefix(fullPath, prefix) {
		return "", errInvalidLogFileName
	}

	info, errStat := os.Stat(fullPath)
	if errStat != nil {
		if os.IsNotExist(errStat) {
			return "", errLogFileNotFound
		}
		return "", fmt.Errorf("failed to read log file: %v", errStat)
	}
	if info.IsDir() {
		return "", errInvalidLogFileName
	}
	if info.Size() > maxLogReviewBytes {
		return "", fmt.Errorf("log file too large for inline review")
	}
	data, errRead := os.ReadFile(fullPath)
	if errRead != nil {
		return "", fmt.Errorf("failed to read log file: %v", errRead)
	}
	return string(data), nil
}

func reviewRequestErrorLog(name string, text string) logReviewResponse {
	method := requestLogField(text, "Method")
	url := requestLogField(text, "URL")
	statusRaw := requestLogField(text, "Status")
	status := parseReviewStatus(statusRaw)
	evidence := []string{}
	if method != "" {
		evidence = append(evidence, "Method: "+sanitizeReviewValue(method))
	}
	if url != "" {
		evidence = append(evidence, "URL: "+sanitizeReviewValue(url))
	}
	if statusRaw != "" {
		evidence = append(evidence, "Status: "+sanitizeReviewValue(statusRaw))
	}

	review := logReviewResponse{
		Name:     name,
		Severity: "info",
		Category: "request_error",
		Summary:  "The log captured a failed request.",
		Evidence: evidence,
		NextActions: []string{
			"Open the raw log to confirm the request path, method, and response status.",
			"Check nearby request traces for repeated failures before changing configuration.",
		},
	}

	lowerURL := strings.ToLower(url)
	switch {
	case status == 404 && strings.Contains(lowerURL, "/static/management.html"):
		review.Severity = "warning"
		review.Category = "static_management_asset_missing"
		review.Summary = "A legacy static management-console path returned 404."
		review.LikelyCause = "A probe or older client requested /static/management.html, but the bundled console is served from /management.html in the current app."
		review.NextActions = []string{
			"Update any health check or bookmark that probes /static/management.html to use /management.html.",
			"If this came from the app keep-alive path, verify the installed bundle and backend static path are current.",
			"Ignore isolated curl HEAD probes if /management.html loads successfully.",
		}
	case status == 404:
		review.Severity = "warning"
		review.Category = "route_not_found"
		review.Summary = "The backend did not have a route or static asset for this request path."
		review.LikelyCause = "The client likely requested an outdated path, a mistyped endpoint, or a static file that is not bundled."
		review.NextActions = []string{
			"Compare the logged URL with the current management routes and static asset paths.",
			"Check whether the caller is using a stale bookmark, cached console, or old health probe.",
		}
	case status == 401 || status == 403:
		review.Severity = "warning"
		review.Category = "management_auth"
		review.Summary = "The request failed management authentication or authorization."
		review.LikelyCause = "The caller did not provide the active management key or used a stale key."
		review.NextActions = []string{
			"Open the console through the app so the current management key is injected.",
			"Verify the dev or production instance is using the expected config home.",
		}
	case status == 429:
		review.Severity = "warning"
		review.Category = "quota"
		review.Summary = "The request failed because an upstream or local quota limit was reached."
		review.LikelyCause = "The selected provider, credential, or model is cooling down or quota-limited."
		review.NextActions = []string{
			"Use Provider Doctor to check the selected provider and credential state.",
			"Review quota fallback settings before retrying high-volume requests.",
		}
	case status >= 500:
		review.Severity = "error"
		review.Category = "server_or_upstream_error"
		review.Summary = "The request failed with a server-side or upstream error."
		review.LikelyCause = "The backend or selected upstream provider returned an internal error."
		review.NextActions = []string{
			"Check request traces for upstream status, provider, model, and retry evidence.",
			"Compare failures across providers to separate backend issues from provider-specific incidents.",
		}
	}

	if len(review.Evidence) == 0 {
		review.Evidence = append(review.Evidence, "No structured request fields were found in the log.")
	}
	review.RedactedLog = sanitizeReviewValue(text)
	review.RedactionNotice = "Secrets and token-like values are redacted before this log can be sent to a model. Review the payload before approving model analysis."
	review.ModelPrompt = buildLogReviewModelPrompt(review)
	review.ModelPromptReady = strings.TrimSpace(review.ModelPrompt) != ""
	return review
}

func requestLogField(text string, label string) string {
	prefix := label + ":"
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(trimmed, prefix))
		}
	}
	return ""
}

func parseReviewStatus(raw string) int {
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return 0
	}
	status, err := strconv.Atoi(fields[0])
	if err != nil {
		return 0
	}
	return status
}

func sanitizeReviewValue(value string) string {
	for _, pattern := range logReviewSecretPatterns {
		value = pattern.ReplaceAllString(value, `${1}=[REDACTED]`)
	}
	if len(value) > 12000 {
		return value[:12000] + "\n[TRUNCATED]"
	}
	return value
}

func buildLogReviewModelPrompt(review logReviewResponse) string {
	var b strings.Builder
	b.WriteString("You are reviewing an LLM Dynamix request error log. ")
	b.WriteString("Use only the redacted evidence below. Do not assume secrets, keys, or infrastructure details that are not present.\n\n")
	b.WriteString("Return concise sections:\n")
	b.WriteString("1. Diagnosis\n2. Likely root cause\n3. Operator impact\n4. Recommended next actions\n5. Confidence and missing evidence\n\n")
	b.WriteString("Local deterministic review:\n")
	b.WriteString("Category: " + review.Category + "\n")
	b.WriteString("Severity: " + review.Severity + "\n")
	b.WriteString("Summary: " + review.Summary + "\n")
	if review.LikelyCause != "" {
		b.WriteString("Likely cause: " + review.LikelyCause + "\n")
	}
	if len(review.Evidence) > 0 {
		b.WriteString("Evidence:\n")
		for _, item := range review.Evidence {
			b.WriteString("- " + item + "\n")
		}
	}
	if len(review.NextActions) > 0 {
		b.WriteString("Suggested next actions:\n")
		for _, item := range review.NextActions {
			b.WriteString("- " + item + "\n")
		}
	}
	b.WriteString("\nRedacted raw log:\n")
	b.WriteString(review.RedactedLog)
	return b.String()
}
