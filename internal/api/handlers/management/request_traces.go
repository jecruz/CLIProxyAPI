package management

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	defaultRequestTraceLimit = 25
	maxRequestTraceLimit     = 100
	maxRequestTraceLogBytes  = 2 * 1024 * 1024
)

type requestTraceResponse struct {
	Traces         []requestTraceEntry `json:"traces"`
	Groups         []requestTraceGroup `json:"groups"`
	Source         string              `json:"source"`
	RequestLogging bool                `json:"requestLogging"`
	FileLogging    bool                `json:"fileLogging"`
	Message        string              `json:"message,omitempty"`
}

type requestTraceEntry struct {
	ID                    string   `json:"id"`
	Timestamp             string   `json:"timestamp,omitempty"`
	Method                string   `json:"method,omitempty"`
	Path                  string   `json:"path,omitempty"`
	Status                int      `json:"status,omitempty"`
	Latency               string   `json:"latency,omitempty"`
	IP                    string   `json:"ip,omitempty"`
	RequestedModel        string   `json:"requestedModel,omitempty"`
	ResolvedProvider      string   `json:"resolvedProvider,omitempty"`
	ResolvedUpstreamModel string   `json:"resolvedUpstreamModel,omitempty"`
	CredentialType        string   `json:"credentialType,omitempty"`
	FailureCategory       string   `json:"failureCategory,omitempty"`
	FinalResult           string   `json:"finalResult,omitempty"`
	RetryFallbackPath     []string `json:"retryFallbackPath,omitempty"`
	RawLogAvailable       bool     `json:"rawLogAvailable"`
	RawLogName            string   `json:"rawLogName,omitempty"`
	RawLogID              string   `json:"rawLogId,omitempty"`
	Evidence              []string `json:"evidence,omitempty"`
}

type requestTraceGroup struct {
	Category string `json:"category"`
	Count    int    `json:"count"`
}

type requestTraceLogFile struct {
	path     string
	name     string
	modified time.Time
}

var (
	requestTraceModelPattern = regexp.MustCompile(`(?i)"model"\s*:\s*"([^"]+)"`)
	requestTraceAuthPattern  = regexp.MustCompile(`(?m)^Auth:\s*(.+)$`)
	requestTraceGINPattern   = regexp.MustCompile(`^\[([^\]]+)\]\s+\[[^\]]+\]\s+\[[^\]]+\]\s+\[[^\]]+\]\s+(\d{3})\s+\|\s*([^|]+)\|\s*([^|]+)\|\s*([A-Z]+)\s+"([^"]+)"`)
)

// GetRequestTraces returns recent request routing evidence derived from request logs.
func (h *Handler) GetRequestTraces(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler unavailable"})
		return
	}
	if h.cfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "configuration unavailable"})
		return
	}

	limit, errLimit := parseRequestTraceLimit(c.Query("limit"))
	if errLimit != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("invalid limit: %v", errLimit)})
		return
	}

	response := requestTraceResponse{
		Traces:         []requestTraceEntry{},
		Groups:         []requestTraceGroup{},
		Source:         "unavailable",
		RequestLogging: h.cfg.RequestLog,
		FileLogging:    h.cfg.LoggingToFile,
	}
	if !h.cfg.LoggingToFile {
		response.Message = "File logging is disabled. Enable logging-to-file to inspect request traces."
		c.JSON(http.StatusOK, response)
		return
	}

	logDir := h.logDirectory()
	if strings.TrimSpace(logDir) == "" {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "log directory not configured"})
		return
	}

	requestLogs, err := collectRequestTraceLogFiles(logDir)
	if err != nil {
		if os.IsNotExist(err) {
			response.Message = "Log directory does not exist yet."
			c.JSON(http.StatusOK, response)
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to list request logs: %v", err)})
		return
	}

	if len(requestLogs) > 0 {
		response.Source = "request_logs"
		for _, file := range requestLogs {
			if len(response.Traces) >= limit {
				break
			}
			text, errRead := readRequestTraceLog(file.path)
			if errRead != nil {
				continue
			}
			response.Traces = append(response.Traces, parseRequestTraceFile(file, text))
		}
		response.Groups = groupRequestTraceFailures(response.Traces)
		c.JSON(http.StatusOK, response)
		return
	}

	traces, err := h.requestTraceMainLogFallback(logDir, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to read main log fallback: %v", err)})
		return
	}
	response.Source = "main_log"
	response.Traces = traces
	response.Groups = groupRequestTraceFailures(traces)
	if len(traces) == 0 {
		response.Message = "No request log entries were found yet."
	}
	c.JSON(http.StatusOK, response)
}

func parseRequestTraceLimit(raw string) (int, error) {
	limit, err := parseLimit(raw)
	if err != nil {
		return 0, err
	}
	if limit == 0 {
		return defaultRequestTraceLimit, nil
	}
	if limit > maxRequestTraceLimit {
		return maxRequestTraceLimit, nil
	}
	return limit, nil
}

func collectRequestTraceLogFiles(dir string) ([]requestTraceLogFile, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	files := make([]requestTraceLogFile, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".log") || name == defaultLogFileName || isRotatedLogFile(name) {
			continue
		}
		info, errInfo := entry.Info()
		if errInfo != nil {
			return nil, errInfo
		}
		files = append(files, requestTraceLogFile{
			path:     filepath.Join(dir, name),
			name:     name,
			modified: info.ModTime(),
		})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].modified.After(files[j].modified) })
	return files, nil
}

func readRequestTraceLog(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()

	data, err := io.ReadAll(io.LimitReader(file, maxRequestTraceLogBytes))
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func parseRequestTraceFile(file requestTraceLogFile, text string) requestTraceEntry {
	entry := requestTraceEntry{
		ID:              requestTraceIDFromFile(file.name),
		Timestamp:       firstRequestTraceHeader(text, "Timestamp"),
		Method:          firstRequestTraceHeader(text, "Method"),
		Path:            firstRequestTraceHeader(text, "URL"),
		RawLogAvailable: true,
		RawLogName:      file.name,
		RawLogID:        requestTraceIDFromFile(file.name),
	}
	if entry.ID == "" {
		entry.ID = file.name
	}
	if entry.Timestamp == "" && !file.modified.IsZero() {
		entry.Timestamp = file.modified.Format(time.RFC3339)
	}
	entry.RequestedModel = firstModelInSection(text, "=== REQUEST BODY ===")
	entry.ResolvedUpstreamModel = firstModelInSection(text, "=== API REQUEST")
	if entry.ResolvedUpstreamModel == "" {
		entry.ResolvedUpstreamModel = firstModelInSection(text, "=== API ERROR REQUEST")
	}
	if entry.RequestedModel == "" {
		entry.RequestedModel = firstModelInText(text)
	}

	entry.Status = firstRequestTraceStatus(text)
	entry.FailureCategory = requestTraceFailureCategory(entry.Status)
	entry.FinalResult = requestTraceFinalResult(entry.Status)

	if auth := firstRequestTraceAuth(text); auth != "" {
		entry.ResolvedProvider, entry.CredentialType = parseRequestTraceAuth(auth)
	}
	entry.RetryFallbackPath = requestTraceUpstreamPath(text)
	entry.Evidence = requestTraceEvidence(entry)
	return entry
}

func (h *Handler) requestTraceMainLogFallback(logDir string, limit int) ([]requestTraceEntry, error) {
	files, err := h.collectLogFiles(logDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []requestTraceEntry{}, nil
		}
		return nil, err
	}
	acc := newLogAccumulator(0, limit*4)
	for i := range files {
		if errProcess := acc.consumeFile(files[i]); errProcess != nil {
			return nil, errProcess
		}
	}
	lines, _, _ := acc.result()
	traces := make([]requestTraceEntry, 0, limit)
	for i := len(lines) - 1; i >= 0 && len(traces) < limit; i-- {
		trace, ok := parseRequestTraceGINLine(lines[i])
		if ok {
			traces = append(traces, trace)
		}
	}
	return traces, nil
}

func parseRequestTraceGINLine(line string) (requestTraceEntry, bool) {
	matches := requestTraceGINPattern.FindStringSubmatch(line)
	if len(matches) != 7 {
		return requestTraceEntry{}, false
	}
	status, _ := strconv.Atoi(matches[2])
	entry := requestTraceEntry{
		ID:              fmt.Sprintf("main-log-%s-%s", strings.ReplaceAll(matches[1], " ", "T"), matches[5]),
		Timestamp:       matches[1],
		Status:          status,
		Latency:         strings.TrimSpace(matches[3]),
		IP:              strings.TrimSpace(matches[4]),
		Method:          matches[5],
		Path:            matches[6],
		RawLogAvailable: false,
	}
	entry.FailureCategory = requestTraceFailureCategory(entry.Status)
	entry.FinalResult = requestTraceFinalResult(entry.Status)
	entry.Evidence = requestTraceEvidence(entry)
	return entry, true
}

func firstRequestTraceHeader(text, key string) string {
	pattern := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(key) + `:\s*(.+)$`)
	if match := pattern.FindStringSubmatch(text); len(match) == 2 {
		return strings.TrimSpace(match[1])
	}
	return ""
}

func firstRequestTraceStatus(text string) int {
	for _, key := range []string{"Status", "HTTP Status"} {
		raw := firstRequestTraceHeader(text, key)
		if raw == "" {
			continue
		}
		fields := strings.Fields(raw)
		if len(fields) == 0 {
			continue
		}
		status, err := strconv.Atoi(fields[0])
		if err == nil {
			return status
		}
	}
	return 0
}

func firstRequestTraceAuth(text string) string {
	if match := requestTraceAuthPattern.FindStringSubmatch(text); len(match) == 2 {
		return strings.TrimSpace(match[1])
	}
	return ""
}

func parseRequestTraceAuth(raw string) (string, string) {
	var provider string
	var credentialType string
	for _, part := range strings.Split(raw, ",") {
		key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "provider":
			provider = strings.TrimSpace(value)
		case "type":
			credentialType = firstRequestTraceToken(value)
		}
	}
	return provider, credentialType
}

func firstRequestTraceToken(value string) string {
	fields := strings.Fields(strings.TrimSpace(value))
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func firstModelInSection(text, marker string) string {
	idx := strings.Index(text, marker)
	if idx < 0 {
		return ""
	}
	section := text[idx:]
	if next := strings.Index(section[len(marker):], "\n==="); next >= 0 {
		section = section[:len(marker)+next]
	}
	return firstModelInText(section)
}

func firstModelInText(text string) string {
	if match := requestTraceModelPattern.FindStringSubmatch(text); len(match) == 2 {
		return strings.TrimSpace(match[1])
	}
	return ""
}

func requestTraceUpstreamPath(text string) []string {
	pattern := regexp.MustCompile(`(?m)^Upstream URL:\s*(.+)$`)
	matches := pattern.FindAllStringSubmatch(text, -1)
	seen := map[string]bool{}
	paths := make([]string, 0, len(matches))
	for _, match := range matches {
		if len(match) != 2 {
			continue
		}
		value := sanitizeRequestTraceURL(match[1])
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		paths = append(paths, value)
	}
	return paths
}

func sanitizeRequestTraceURL(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	parsed, err := url.Parse(value)
	if err == nil && parsed.Scheme != "" && parsed.Host != "" {
		parsed.RawQuery = ""
		parsed.Fragment = ""
		return parsed.String()
	}
	if idx := strings.Index(value, "?"); idx >= 0 {
		value = value[:idx]
	}
	return strings.TrimSpace(value)
}

func requestTraceIDFromFile(name string) string {
	base := strings.TrimSuffix(name, filepath.Ext(name))
	if base == "" {
		return ""
	}
	parts := strings.Split(base, "-")
	return strings.TrimSpace(parts[len(parts)-1])
}

func requestTraceFailureCategory(status int) string {
	switch {
	case status == 0:
		return "unknown"
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return "auth"
	case status == http.StatusNotFound:
		return "not_found"
	case status == http.StatusTooManyRequests:
		return "quota"
	case status >= 500:
		return "upstream"
	case status >= 400:
		return "client_error"
	default:
		return "success"
	}
}

func requestTraceFinalResult(status int) string {
	switch {
	case status == 0:
		return "unknown"
	case status >= 200 && status < 400:
		return "completed"
	default:
		return "failed"
	}
}

func requestTraceEvidence(entry requestTraceEntry) []string {
	evidence := make([]string, 0, 4)
	if entry.Method != "" || entry.Path != "" {
		evidence = append(evidence, strings.TrimSpace(entry.Method+" "+entry.Path))
	}
	if entry.Status > 0 {
		evidence = append(evidence, fmt.Sprintf("status %d categorized as %s", entry.Status, entry.FailureCategory))
	}
	if entry.ResolvedProvider != "" {
		evidence = append(evidence, fmt.Sprintf("provider %s via %s", entry.ResolvedProvider, valueOrFallback(entry.CredentialType, "unknown credential")))
	}
	if len(entry.RetryFallbackPath) > 0 {
		evidence = append(evidence, "upstream evidence "+strings.Join(entry.RetryFallbackPath, " -> "))
	}
	if len(evidence) == 0 {
		evidence = append(evidence, "raw log evidence available")
	}
	return evidence
}

func groupRequestTraceFailures(traces []requestTraceEntry) []requestTraceGroup {
	counts := map[string]int{}
	for _, trace := range traces {
		counts[requestTraceFailureCategory(trace.Status)]++
	}
	order := []string{"success", "auth", "quota", "upstream", "client_error", "not_found", "unknown"}
	groups := make([]requestTraceGroup, 0, len(counts))
	for _, category := range order {
		if count := counts[category]; count > 0 {
			groups = append(groups, requestTraceGroup{Category: category, Count: count})
			delete(counts, category)
		}
	}
	for category, count := range counts {
		groups = append(groups, requestTraceGroup{Category: category, Count: count})
	}
	return groups
}

func valueOrFallback(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
