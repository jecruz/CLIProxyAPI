package management

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

type providerDoctorResponse struct {
	APIProviders   []providerDoctorEntry `json:"apiProviders"`
	OAuthProviders []providerDoctorEntry `json:"oauthProviders"`
}

type providerDoctorEntry struct {
	ID                    string `json:"id"`
	Label                 string `json:"label"`
	Kind                  string `json:"kind"`
	Source                string `json:"source"`
	Configured            bool   `json:"configured"`
	CredentialCount       int    `json:"credentialCount"`
	ActiveCredentialCount int    `json:"activeCredentialCount"`
	ModelCount            int    `json:"modelCount"`
	Status                string `json:"status"`
	StatusMessage         string `json:"statusMessage,omitempty"`
	LastRefreshState      string `json:"lastRefreshState,omitempty"`
	LastRefreshedAt       string `json:"lastRefreshedAt,omitempty"`
	NextRetryAfter        string `json:"nextRetryAfter,omitempty"`
	LastFailureCategory   string `json:"lastFailureCategory,omitempty"`
	LastFailureMessage    string `json:"lastFailureMessage,omitempty"`
}

type providerDoctorCheckRequest struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Kind     string `json:"kind"`
}

type providerDoctorCheckResponse struct {
	Provider         string   `json:"provider"`
	Model            string   `json:"model,omitempty"`
	Kind             string   `json:"kind"`
	Status           string   `json:"status"`
	FailureCategory  string   `json:"failureCategory,omitempty"`
	Evidence         []string `json:"evidence"`
	MatchedProviders []string `json:"matchedProviders,omitempty"`
	MatchedModels    []string `json:"matchedModels,omitempty"`
}

type providerDoctorLiveProbeRequest struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Kind     string `json:"kind"`
	Approved bool   `json:"approved"`
}

type providerDoctorLiveProbeResponse struct {
	Provider        string                       `json:"provider"`
	Model           string                       `json:"model,omitempty"`
	Kind            string                       `json:"kind"`
	Status          string                       `json:"status"`
	FailureCategory string                       `json:"failureCategory,omitempty"`
	HTTPStatus      int                          `json:"httpStatus,omitempty"`
	Endpoint        string                       `json:"endpoint,omitempty"`
	Evidence        []string                     `json:"evidence"`
	Deterministic   *providerDoctorCheckResponse `json:"deterministic,omitempty"`
}

// GetProviderDoctor returns redacted provider readiness evidence for Diagnostics.
func (h *Handler) GetProviderDoctor(c *gin.Context) {
	c.JSON(http.StatusOK, providerDoctorResponse{
		APIProviders:   h.apiProviderDoctorEntries(),
		OAuthProviders: h.oauthProviderDoctorEntries(),
	})
}

// CheckProviderRoute returns deterministic provider/model route evidence without calling upstream providers.
func (h *Handler) CheckProviderRoute(c *gin.Context) {
	var req providerDoctorCheckRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request", "message": err.Error()})
		return
	}

	provider := normalizeProviderDoctorProvider(req.Provider)
	if provider == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_provider", "message": "provider is required"})
		return
	}

	kind := strings.ToLower(strings.TrimSpace(req.Kind))
	if kind == "" {
		kind = "auto"
	}
	if kind != "auto" && kind != "api" && kind != "oauth" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_kind", "message": "kind must be auto, api, or oauth"})
		return
	}

	response := h.checkProviderRoute(provider, strings.TrimSpace(req.Model), kind)
	c.JSON(http.StatusOK, response)
}

// LiveProbeProvider runs one explicitly approved, redacted upstream provider probe.
func (h *Handler) LiveProbeProvider(c *gin.Context) {
	var req providerDoctorLiveProbeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request", "message": err.Error()})
		return
	}
	if !req.Approved {
		c.JSON(http.StatusBadRequest, gin.H{"error": "approval_required", "message": "live provider probes require explicit operator approval"})
		return
	}

	provider := normalizeProviderDoctorProvider(req.Provider)
	if provider == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_provider", "message": "provider is required"})
		return
	}

	kind := strings.ToLower(strings.TrimSpace(req.Kind))
	if kind == "" {
		kind = "api"
	}
	if kind != "api" && kind != "oauth" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_kind", "message": "live probe kind must be api or oauth"})
		return
	}

	model := strings.TrimSpace(req.Model)
	deterministic := h.checkProviderRoute(provider, model, kind)
	response := h.liveProbeProvider(c.Request.Context(), provider, model, kind, deterministic)
	c.JSON(http.StatusOK, response)
}

func (h *Handler) checkProviderRoute(provider, model, kind string) providerDoctorCheckResponse {
	checks := make([]providerDoctorCheckResponse, 0, 2)
	if kind == "auto" || kind == "api" {
		checks = append(checks, h.checkAPIProviderRoute(provider, model))
	}
	if kind == "auto" || kind == "oauth" {
		checks = append(checks, h.checkOAuthProviderRoute(provider, model))
	}
	if len(checks) == 0 {
		return providerDoctorCheckResponse{
			Provider:        provider,
			Model:           model,
			Kind:            kind,
			Status:          "missing",
			FailureCategory: "missing_provider",
			Evidence:        []string{"No provider source selected for route check."},
		}
	}
	if kind != "auto" {
		return checks[0]
	}

	merged := providerDoctorCheckResponse{
		Provider: provider,
		Model:    model,
		Kind:     "auto",
		Status:   "missing",
		Evidence: []string{},
	}
	statusRank := map[string]int{"missing": 0, "review": 1, "ready": 2}
	for _, check := range checks {
		merged.Evidence = append(merged.Evidence, check.Evidence...)
		merged.MatchedProviders = appendUniqueStrings(merged.MatchedProviders, check.MatchedProviders...)
		merged.MatchedModels = appendUniqueStrings(merged.MatchedModels, check.MatchedModels...)
		if statusRank[check.Status] > statusRank[merged.Status] {
			merged.Status = check.Status
			merged.FailureCategory = check.FailureCategory
		} else if merged.FailureCategory == "" {
			merged.FailureCategory = check.FailureCategory
		}
	}
	if merged.Status == "ready" {
		merged.FailureCategory = ""
	}
	return merged
}

func (h *Handler) liveProbeProvider(ctx context.Context, provider, model, kind string, deterministic providerDoctorCheckResponse) providerDoctorLiveProbeResponse {
	response := providerDoctorLiveProbeResponse{
		Provider:      provider,
		Model:         model,
		Kind:          kind,
		Status:        "review",
		Evidence:      []string{"Deterministic route check ran before the live probe."},
		Deterministic: &deterministic,
	}
	response.Evidence = append(response.Evidence, deterministic.Evidence...)
	if deterministic.Status == "missing" {
		response.Status = "missing"
		response.FailureCategory = deterministic.FailureCategory
		response.Evidence = append(response.Evidence, "Live probe skipped because deterministic readiness is missing.")
		return response
	}
	if kind == "oauth" {
		response.FailureCategory = "oauth_live_probe_not_supported"
		response.Evidence = append(response.Evidence, "OAuth live probing is not performed from Diagnostics because provider OAuth flows require provider-specific account refresh paths.")
		return response
	}

	probeReq, failureCategory, evidence := h.providerDoctorAPIProbeRequest(ctx, provider, model)
	if failureCategory != "" {
		response.FailureCategory = failureCategory
		response.Evidence = append(response.Evidence, evidence...)
		if failureCategory == "missing_credential" || failureCategory == "missing_endpoint" {
			response.Status = "missing"
		}
		return response
	}

	response.Endpoint = sanitizeProviderDoctorEndpoint(probeReq.URL.String())
	client := &http.Client{Timeout: 4 * time.Second}
	resp, err := client.Do(probeReq)
	if err != nil {
		response.FailureCategory = "network_error"
		if ctx.Err() != nil {
			response.FailureCategory = "timeout"
		}
		response.Evidence = append(response.Evidence, "Live probe failed before a usable HTTP response: "+redactProviderDoctorMessage(err.Error()))
		return response
	}
	defer resp.Body.Close()

	response.HTTPStatus = resp.StatusCode
	response.Status, response.FailureCategory = providerDoctorHTTPProbeStatus(resp.StatusCode)
	response.Evidence = append(response.Evidence, providerDoctorHTTPProbeEvidence(resp.StatusCode))
	return response
}

func (h *Handler) providerDoctorAPIProbeRequest(ctx context.Context, provider, model string) (*http.Request, string, []string) {
	cfg := h.cfg
	if cfg == nil {
		cfg = &config.Config{}
	}
	switch provider {
	case "gemini":
		for _, key := range cfg.GeminiKey {
			apiKey := strings.TrimSpace(key.APIKey)
			if apiKey == "" {
				continue
			}
			base := firstString(key.BaseURL, "https://generativelanguage.googleapis.com")
			req, err := providerDoctorNewGETRequest(ctx, base, "/v1beta/models")
			if err != nil {
				return nil, "invalid_endpoint", []string{"Gemini API base URL is invalid."}
			}
			req.Header.Set("x-goog-api-key", apiKey)
			return req, "", []string{"Gemini API live probe will call the models endpoint with the configured key in a header."}
		}
		return nil, "missing_credential", []string{"No Gemini API key is configured for live probing."}
	case "claude":
		for _, key := range cfg.ClaudeKey {
			apiKey := strings.TrimSpace(key.APIKey)
			if apiKey == "" {
				continue
			}
			base := firstString(key.BaseURL, "https://api.anthropic.com")
			req, err := providerDoctorNewGETRequest(ctx, base, "/v1/models")
			if err != nil {
				return nil, "invalid_endpoint", []string{"Claude API base URL is invalid."}
			}
			req.Header.Set("x-api-key", apiKey)
			req.Header.Set("anthropic-version", "2023-06-01")
			return req, "", []string{"Claude API live probe will call the models endpoint with the configured key in a header."}
		}
		return nil, "missing_credential", []string{"No Claude API key is configured for live probing."}
	case "codex":
		for _, key := range cfg.CodexKey {
			apiKey := strings.TrimSpace(key.APIKey)
			if apiKey == "" {
				continue
			}
			base := firstString(key.BaseURL, "https://api.openai.com/v1")
			req, err := providerDoctorNewGETRequest(ctx, base, "/models")
			if err != nil {
				return nil, "invalid_endpoint", []string{"Codex API base URL is invalid."}
			}
			req.Header.Set("Authorization", "Bearer "+apiKey)
			return req, "", []string{"Codex API live probe will call the OpenAI-compatible models endpoint with the configured key in a header."}
		}
		return nil, "missing_credential", []string{"No Codex API key is configured for live probing."}
	case "openaiCompatibility":
		provider := selectOpenAICompatibilityProbeProvider(cfg.OpenAICompatibility, model)
		if provider == nil {
			return nil, "missing_credential", []string{"No enabled OpenAI-compatible API provider with a key and base URL is configured for live probing."}
		}
		req, err := providerDoctorNewGETRequest(ctx, provider.BaseURL, "/models")
		if err != nil {
			return nil, "invalid_endpoint", []string{"OpenAI-compatible API base URL is invalid."}
		}
		req.Header.Set("Authorization", "Bearer "+firstOpenAICompatibilityAPIKey(*provider))
		for key, value := range provider.Headers {
			if strings.TrimSpace(key) == "" || strings.TrimSpace(value) == "" {
				continue
			}
			req.Header.Set(key, value)
		}
		label := firstString(provider.Name, "OpenAI-compatible provider")
		return req, "", []string{"OpenAI-compatible live probe will call the models endpoint for " + label + " with the configured key in a header."}
	case "vertex":
		return nil, "live_probe_not_supported", []string{"Vertex-compatible API live probing is not implemented because compatible providers do not share one safe model-list endpoint."}
	default:
		return nil, "live_probe_not_supported", []string{"Live probing is not available for this provider."}
	}
}

func (h *Handler) checkAPIProviderRoute(provider, model string) providerDoctorCheckResponse {
	cfg := h.cfg
	if cfg == nil {
		cfg = &config.Config{}
	}

	models := h.apiProviderRouteModels(provider, cfg)
	credentialCount := h.apiProviderRouteCredentialCount(provider, cfg)
	response := providerDoctorCheckResponse{
		Provider: provider,
		Model:    model,
		Kind:     "api",
		Status:   "missing",
		Evidence: []string{},
	}
	if !isKnownAPIProvider(provider) {
		response.FailureCategory = "missing_provider"
		response.Evidence = append(response.Evidence, "No API provider definition exists for "+provider+".")
		return response
	}
	if credentialCount == 0 {
		response.FailureCategory = "missing_credential"
		response.Evidence = append(response.Evidence, "No configured API credentials were found for "+provider+".")
		return response
	}
	response.MatchedProviders = []string{provider}
	response.Evidence = append(response.Evidence, "API credentials are configured for "+provider+".")
	if model == "" {
		response.Status = "ready"
		return response
	}
	if len(models) == 0 {
		response.Status = "ready"
		response.Evidence = append(response.Evidence, "No configured API model allowlist is present; route can select by provider credential.")
		return response
	}
	if match, ok := matchProviderDoctorModel(model, models); ok {
		response.Status = "ready"
		response.MatchedModels = []string{match}
		response.Evidence = append(response.Evidence, "Configured API model matched "+match+".")
		return response
	}
	response.Status = "review"
	response.FailureCategory = "missing_model"
	response.Evidence = append(response.Evidence, "No configured API model matched "+model+".")
	response.MatchedModels = models
	return response
}

func (h *Handler) checkOAuthProviderRoute(provider, model string) providerDoctorCheckResponse {
	response := providerDoctorCheckResponse{
		Provider: provider,
		Model:    model,
		Kind:     "oauth",
		Status:   "missing",
		Evidence: []string{},
	}
	if h == nil || h.authManager == nil {
		response.FailureCategory = "missing_credential"
		response.Evidence = append(response.Evidence, "OAuth auth manager is not available.")
		return response
	}

	matchingAuths := []*coreauth.Auth{}
	activeAuths := []*coreauth.Auth{}
	hasDisabled := false
	hasError := false
	for _, auth := range h.authManager.List() {
		if auth == nil || oauthProviderFamilyForAuth(auth) != provider {
			continue
		}
		matchingAuths = append(matchingAuths, auth)
		if auth.Disabled || auth.Status == coreauth.StatusDisabled {
			hasDisabled = true
			continue
		}
		if auth.Status == coreauth.StatusError || auth.LastError != nil {
			hasError = true
		}
		activeAuths = append(activeAuths, auth)
	}
	if len(matchingAuths) == 0 {
		response.FailureCategory = "missing_credential"
		response.Evidence = append(response.Evidence, "No OAuth files were found for "+provider+".")
		return response
	}
	if len(activeAuths) == 0 {
		response.Status = "review"
		response.FailureCategory = "disabled"
		if hasDisabled {
			response.Evidence = append(response.Evidence, "OAuth files exist for "+provider+", but none are active.")
		} else {
			response.Evidence = append(response.Evidence, "OAuth files exist for "+provider+", but none are usable.")
		}
		return response
	}

	response.MatchedProviders = []string{provider}
	response.Evidence = append(response.Evidence, "OAuth files are active for "+provider+".")
	if hasError {
		response.Status = "review"
		response.FailureCategory = "provider_error"
		response.Evidence = append(response.Evidence, "At least one matching OAuth file has refresh or provider error evidence.")
		return response
	}
	if model == "" {
		response.Status = "ready"
		return response
	}

	registryModels := oauthProviderRouteModels(activeAuths)
	if len(registryModels) == 0 {
		response.Status = "review"
		response.FailureCategory = "model_inventory_unavailable"
		response.Evidence = append(response.Evidence, "No model inventory is registered for matching OAuth files.")
		return response
	}
	if match, ok := matchProviderDoctorModel(model, registryModels); ok {
		response.Status = "ready"
		response.MatchedModels = []string{match}
		response.Evidence = append(response.Evidence, "Registered OAuth model matched "+match+".")
		return response
	}
	response.Status = "review"
	response.FailureCategory = "missing_model"
	response.Evidence = append(response.Evidence, "No registered OAuth model matched "+model+".")
	response.MatchedModels = registryModels
	return response
}

func (h *Handler) apiProviderDoctorEntries() []providerDoctorEntry {
	cfg := h.cfg
	if cfg == nil {
		cfg = &config.Config{}
	}
	return []providerDoctorEntry{
		apiProviderEntry("gemini", "Gemini API", len(cfg.GeminiKey), countGeminiModels(cfg.GeminiKey)),
		apiProviderEntry("codex", "Codex API", len(cfg.CodexKey), countCodexModels(cfg.CodexKey)),
		apiProviderEntry("claude", "Claude API", len(cfg.ClaudeKey), countClaudeModels(cfg.ClaudeKey)),
		apiProviderEntry("vertex", "Vertex API", len(cfg.VertexCompatAPIKey), countVertexModels(cfg.VertexCompatAPIKey)),
		apiProviderEntry("openaiCompatibility", "OpenAI-Compatible API", countOpenAICompatibilityCredentials(cfg.OpenAICompatibility), countOpenAICompatibilityModels(cfg.OpenAICompatibility)),
	}
}

func apiProviderEntry(id, label string, credentialCount, modelCount int) providerDoctorEntry {
	status := "missing"
	if credentialCount > 0 {
		status = "ready"
	}
	return providerDoctorEntry{
		ID:                    id,
		Label:                 label,
		Kind:                  "api",
		Source:                "config",
		Configured:            credentialCount > 0,
		CredentialCount:       credentialCount,
		ActiveCredentialCount: credentialCount,
		ModelCount:            modelCount,
		Status:                status,
		LastRefreshState:      "not_tracked",
	}
}

func (h *Handler) oauthProviderDoctorEntries() []providerDoctorEntry {
	families := []struct {
		id    string
		label string
	}{
		{"gemini", "Gemini OAuth"},
		{"codex", "Codex OAuth"},
		{"claude", "Claude OAuth"},
		{"vertex", "Vertex OAuth"},
		{"antigravity", "Antigravity OAuth"},
		{"qwen", "Qwen OAuth"},
		{"xai", "xAI OAuth"},
		{"kimi", "Kimi OAuth"},
	}

	entries := make(map[string]providerDoctorEntry, len(families))
	for _, family := range families {
		entries[family.id] = providerDoctorEntry{
			ID:               family.id,
			Label:            family.label,
			Kind:             "oauth",
			Source:           "auth-files",
			Status:           "missing",
			LastRefreshState: "not_available",
		}
	}

	if h != nil && h.authManager != nil {
		modelRegistry := registry.GetGlobalRegistry()
		for _, auth := range h.authManager.List() {
			if auth == nil {
				continue
			}
			family := oauthProviderFamilyForAuth(auth)
			if _, ok := entries[family]; !ok {
				family = "other"
				if _, exists := entries[family]; !exists {
					entries[family] = providerDoctorEntry{
						ID:               family,
						Label:            "Other OAuth",
						Kind:             "oauth",
						Source:           "auth-files",
						Status:           "missing",
						LastRefreshState: "not_available",
					}
				}
			}
			entry := entries[family]
			entry.Configured = true
			entry.CredentialCount++
			if !auth.Disabled && auth.Status != coreauth.StatusDisabled {
				entry.ActiveCredentialCount++
			}
			auth.EnsureIndex()
			entry.ModelCount += len(modelRegistry.GetModelsForClient(auth.ID))
			entry = mergeAuthEvidence(entry, auth)
			entries[family] = entry
		}
	}

	result := make([]providerDoctorEntry, 0, len(families)+1)
	for _, family := range families {
		entry := entries[family.id]
		entry.Status = providerDoctorStatus(entry)
		result = append(result, entry)
	}
	if other, ok := entries["other"]; ok && other.CredentialCount > 0 {
		other.Status = providerDoctorStatus(other)
		result = append(result, other)
	}
	return result
}

func mergeAuthEvidence(entry providerDoctorEntry, auth *coreauth.Auth) providerDoctorEntry {
	if auth == nil {
		return entry
	}
	if strings.TrimSpace(auth.StatusMessage) != "" && entry.StatusMessage == "" {
		entry.StatusMessage = redactProviderDoctorMessage(auth.StatusMessage)
	}
	if !auth.LastRefreshedAt.IsZero() && entry.LastRefreshedAt == "" {
		entry.LastRefreshedAt = auth.LastRefreshedAt.UTC().Format(time.RFC3339)
	}
	if !auth.NextRetryAfter.IsZero() && entry.NextRetryAfter == "" {
		entry.NextRetryAfter = auth.NextRetryAfter.UTC().Format(time.RFC3339)
	}
	if auth.LastError != nil && entry.LastFailureCategory == "" {
		entry.LastFailureCategory = auth.LastError.Code
		if entry.LastFailureCategory == "" {
			entry.LastFailureCategory = statusCategory(auth.LastError.HTTPStatus)
		}
		entry.LastFailureMessage = redactProviderDoctorMessage(auth.LastError.Message)
	}
	if auth.Status == coreauth.StatusRefreshing {
		entry.LastRefreshState = "refreshing"
	} else if auth.Status == coreauth.StatusError || auth.LastError != nil {
		entry.LastRefreshState = "failed"
	} else if !auth.LastRefreshedAt.IsZero() {
		entry.LastRefreshState = "refreshed"
	} else if entry.LastRefreshState == "" || entry.LastRefreshState == "not_available" {
		entry.LastRefreshState = "not_refreshed"
	}
	return entry
}

func providerDoctorStatus(entry providerDoctorEntry) string {
	if entry.CredentialCount == 0 {
		return "missing"
	}
	if entry.LastFailureCategory != "" || entry.ActiveCredentialCount == 0 {
		return "review"
	}
	return "ready"
}

func oauthProviderFamilyForAuth(auth *coreauth.Auth) string {
	family := providerFamilyForAuth(auth)
	if family != "" && family != "openaiCompatibility" {
		return family
	}
	provider := strings.ToLower(strings.TrimSpace(auth.Provider))
	metadataType := ""
	if auth.Metadata != nil {
		metadataType, _ = auth.Metadata["type"].(string)
	}
	token := strings.ToLower(strings.TrimSpace(firstString(provider, metadataType, authAttribute(auth, "provider"))))
	switch {
	case strings.Contains(token, "antigravity"):
		return "antigravity"
	case strings.Contains(token, "qwen"):
		return "qwen"
	case strings.Contains(token, "xai") || strings.Contains(token, "grok"):
		return "xai"
	case strings.Contains(token, "kimi"):
		return "kimi"
	case strings.Contains(token, "vertex"):
		return "vertex"
	case strings.Contains(token, "claude") || strings.Contains(token, "anthropic"):
		return "claude"
	case strings.Contains(token, "codex"):
		return "codex"
	case strings.Contains(token, "gemini") || strings.Contains(token, "aistudio"):
		return "gemini"
	default:
		return "other"
	}
}

func statusCategory(status int) string {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return "auth"
	case status == http.StatusTooManyRequests:
		return "quota"
	case status >= 500:
		return "upstream"
	case status >= 400:
		return "request"
	default:
		return "unknown"
	}
}

func countGeminiModels(keys []config.GeminiKey) int {
	total := 0
	for _, key := range keys {
		total += len(key.Models)
	}
	return total
}

func countCodexModels(keys []config.CodexKey) int {
	total := 0
	for _, key := range keys {
		total += len(key.Models)
	}
	return total
}

func countClaudeModels(keys []config.ClaudeKey) int {
	total := 0
	for _, key := range keys {
		total += len(key.Models)
	}
	return total
}

func countVertexModels(keys []config.VertexCompatKey) int {
	total := 0
	for _, key := range keys {
		total += len(key.Models)
	}
	return total
}

func countOpenAICompatibilityCredentials(providers []config.OpenAICompatibility) int {
	total := 0
	for _, provider := range providers {
		if provider.Disabled {
			continue
		}
		total += len(provider.APIKeyEntries)
	}
	return total
}

func countOpenAICompatibilityModels(providers []config.OpenAICompatibility) int {
	total := 0
	for _, provider := range providers {
		total += len(provider.Models)
	}
	return total
}

func normalizeProviderDoctorProvider(raw string) string {
	token := strings.ToLower(strings.TrimSpace(raw))
	switch token {
	case "gemini", "gemini-cli", "aistudio", "ai-studio":
		return "gemini"
	case "codex", "openai-codex":
		return "codex"
	case "claude", "anthropic":
		return "claude"
	case "vertex", "vertex-ai":
		return "vertex"
	case "openai", "openaicompatibility", "openai-compatible", "openai-compatibility", "openrouter":
		return "openaiCompatibility"
	case "antigravity":
		return "antigravity"
	case "qwen":
		return "qwen"
	case "xai", "grok":
		return "xai"
	case "kimi":
		return "kimi"
	default:
		return ""
	}
}

func isKnownAPIProvider(provider string) bool {
	switch provider {
	case "gemini", "codex", "claude", "vertex", "openaiCompatibility":
		return true
	default:
		return false
	}
}

func (h *Handler) apiProviderRouteCredentialCount(provider string, cfg *config.Config) int {
	switch provider {
	case "gemini":
		return len(cfg.GeminiKey)
	case "codex":
		return len(cfg.CodexKey)
	case "claude":
		return len(cfg.ClaudeKey)
	case "vertex":
		return len(cfg.VertexCompatAPIKey)
	case "openaiCompatibility":
		return countOpenAICompatibilityCredentials(cfg.OpenAICompatibility)
	default:
		return 0
	}
}

func (h *Handler) apiProviderRouteModels(provider string, cfg *config.Config) []string {
	models := []string{}
	switch provider {
	case "gemini":
		for _, key := range cfg.GeminiKey {
			for _, model := range key.Models {
				models = appendProviderDoctorModelNames(models, model.Name, model.Alias)
			}
		}
	case "codex":
		for _, key := range cfg.CodexKey {
			for _, model := range key.Models {
				models = appendProviderDoctorModelNames(models, model.Name, model.Alias)
			}
		}
	case "claude":
		for _, key := range cfg.ClaudeKey {
			for _, model := range key.Models {
				models = appendProviderDoctorModelNames(models, model.Name, model.Alias)
			}
		}
	case "vertex":
		for _, key := range cfg.VertexCompatAPIKey {
			for _, model := range key.Models {
				models = appendProviderDoctorModelNames(models, model.Name, model.Alias)
			}
		}
	case "openaiCompatibility":
		for _, provider := range cfg.OpenAICompatibility {
			if provider.Disabled {
				continue
			}
			for _, model := range provider.Models {
				models = appendProviderDoctorModelNames(models, model.Name, model.Alias)
			}
		}
	}
	return models
}

func oauthProviderRouteModels(auths []*coreauth.Auth) []string {
	modelRegistry := registry.GetGlobalRegistry()
	models := []string{}
	for _, auth := range auths {
		if auth == nil {
			continue
		}
		auth.EnsureIndex()
		for _, model := range modelRegistry.GetModelsForClient(auth.ID) {
			if model == nil {
				continue
			}
			models = appendProviderDoctorModelNames(models, model.ID, model.Name, model.DisplayName)
		}
	}
	return models
}

func appendProviderDoctorModelNames(models []string, names ...string) []string {
	for _, name := range names {
		trimmed := strings.TrimSpace(name)
		if trimmed == "" {
			continue
		}
		models = appendUniqueStrings(models, trimmed)
	}
	return models
}

func matchProviderDoctorModel(model string, candidates []string) (string, bool) {
	normalized := strings.ToLower(strings.TrimSpace(model))
	if normalized == "" {
		return "", false
	}
	for _, candidate := range candidates {
		if strings.EqualFold(strings.TrimSpace(candidate), normalized) {
			return candidate, true
		}
	}
	return "", false
}

func appendUniqueStrings(values []string, additions ...string) []string {
	seen := make(map[string]struct{}, len(values)+len(additions))
	for _, value := range values {
		seen[strings.ToLower(strings.TrimSpace(value))] = struct{}{}
	}
	for _, value := range additions {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		key := strings.ToLower(trimmed)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		values = append(values, trimmed)
	}
	return values
}

func providerDoctorNewGETRequest(ctx context.Context, baseURL, path string) (*http.Request, error) {
	endpoint, err := providerDoctorJoinEndpoint(baseURL, path)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "LLM-Dynamix-Diagnostics/1")
	return req, nil
}

func providerDoctorJoinEndpoint(baseURL, path string) (string, error) {
	trimmedBase := strings.TrimSpace(baseURL)
	if trimmedBase == "" {
		return "", errors.New("missing base URL")
	}
	parsed, err := url.Parse(trimmedBase)
	if err != nil {
		return "", err
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return "", errors.New("invalid base URL")
	}
	basePath := strings.TrimRight(parsed.Path, "/")
	parsed.Path = basePath + "/" + strings.TrimLeft(path, "/")
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}

func selectOpenAICompatibilityProbeProvider(providers []config.OpenAICompatibility, model string) *config.OpenAICompatibility {
	for i := range providers {
		provider := providers[i]
		if !openAICompatibilityProviderProbeReady(provider) {
			continue
		}
		if strings.TrimSpace(model) == "" {
			return &providers[i]
		}
		for _, configuredModel := range provider.Models {
			if match, ok := matchProviderDoctorModel(model, appendProviderDoctorModelNames(nil, configuredModel.Name, configuredModel.Alias)); ok && match != "" {
				return &providers[i]
			}
		}
	}
	for i := range providers {
		if openAICompatibilityProviderProbeReady(providers[i]) {
			return &providers[i]
		}
	}
	return nil
}

func openAICompatibilityProviderProbeReady(provider config.OpenAICompatibility) bool {
	if provider.Disabled || strings.TrimSpace(provider.BaseURL) == "" {
		return false
	}
	for _, entry := range provider.APIKeyEntries {
		if strings.TrimSpace(entry.APIKey) != "" {
			return true
		}
	}
	return false
}

func firstOpenAICompatibilityAPIKey(provider config.OpenAICompatibility) string {
	for _, entry := range provider.APIKeyEntries {
		if key := strings.TrimSpace(entry.APIKey); key != "" {
			return key
		}
	}
	return ""
}

func providerDoctorHTTPProbeStatus(status int) (string, string) {
	switch {
	case status >= 200 && status < 300:
		return "ready", ""
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return "review", "auth_failed"
	case status == http.StatusTooManyRequests:
		return "review", "rate_limited"
	case status == http.StatusNotFound || status == http.StatusMethodNotAllowed:
		return "review", "endpoint_not_found"
	case status >= 500:
		return "review", "upstream_error"
	case status >= 400:
		return "review", "request_rejected"
	default:
		return "review", "unexpected_status"
	}
}

func providerDoctorHTTPProbeEvidence(status int) string {
	switch {
	case status >= 200 && status < 300:
		return "Live probe reached the provider models endpoint successfully."
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return "Live probe reached the provider, but credentials were rejected."
	case status == http.StatusTooManyRequests:
		return "Live probe reached the provider, but the provider rate-limited the request."
	case status == http.StatusNotFound || status == http.StatusMethodNotAllowed:
		return "Live probe reached the provider, but the configured endpoint does not expose a compatible models route."
	case status >= 500:
		return "Live probe reached the provider, but the upstream returned a server error."
	case status >= 400:
		return "Live probe reached the provider, but the provider rejected the request."
	default:
		return "Live probe returned an unexpected HTTP status."
	}
}

func sanitizeProviderDoctorEndpoint(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

func firstString(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

var providerDoctorSecretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(bearer\s+)[^\s,;]+`),
	regexp.MustCompile(`(?i)((api[_ -]?key|token|secret)\s*[:=]\s*)[^\s,;]+`),
	regexp.MustCompile(`(?i)(authorization\s*[:=]\s*)[^\s,;]+`),
}

func redactProviderDoctorMessage(message string) string {
	trimmed := strings.TrimSpace(message)
	if trimmed == "" {
		return ""
	}
	for _, pattern := range providerDoctorSecretPatterns {
		trimmed = pattern.ReplaceAllString(trimmed, `${1}[redacted]`)
	}
	if len(trimmed) > 180 {
		trimmed = trimmed[:180] + "..."
	}
	return trimmed
}
