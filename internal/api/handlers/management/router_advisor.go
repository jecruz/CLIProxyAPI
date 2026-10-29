package management

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

type routerAdvisorResponse struct {
	GeneratedAt     string                        `json:"generatedAt"`
	Status          string                        `json:"status"`
	Summary         string                        `json:"summary"`
	Recommendations []routerAdvisorRecommendation `json:"recommendations"`
}

type routerAdvisorRecommendation struct {
	ID              string   `json:"id"`
	Category        string   `json:"category"`
	Severity        string   `json:"severity"`
	Title           string   `json:"title"`
	Description     string   `json:"description"`
	Evidence        []string `json:"evidence"`
	SuggestedChange string   `json:"suggestedChange,omitempty"`
	CopyText        string   `json:"copyText,omitempty"`
}

type routerAdvisorModelRoute struct {
	Provider        string
	ProviderLabel   string
	Source          string
	Name            string
	Alias           string
	CredentialCount int
}

// GetRouterAdvisor returns read-only routing recommendations backed by local evidence.
func (h *Handler) GetRouterAdvisor(c *gin.Context) {
	recommendations := h.routerAdvisorRecommendations()
	status := "ok"
	summary := "No routing recommendations found from current local evidence."
	if len(recommendations) > 0 {
		status = "review"
		summary = fmt.Sprintf("%d routing recommendation%s found.", len(recommendations), pluralSuffix(len(recommendations)))
	}
	c.JSON(http.StatusOK, routerAdvisorResponse{
		GeneratedAt:     time.Now().UTC().Format(time.RFC3339),
		Status:          status,
		Summary:         summary,
		Recommendations: recommendations,
	})
}

func (h *Handler) routerAdvisorRecommendations() []routerAdvisorRecommendation {
	cfg := h.cfg
	if cfg == nil {
		cfg = &config.Config{}
	}
	routes := h.routerAdvisorModelRoutes(cfg)
	recommendations := []routerAdvisorRecommendation{}
	recommendations = append(recommendations, routerAdvisorDuplicateModels(routes)...)
	recommendations = append(recommendations, routerAdvisorProvidersWithoutRoutes(routes)...)
	recommendations = append(recommendations, h.routerAdvisorOAuthAliasRecommendations(cfg)...)
	recommendations = append(recommendations, routerAdvisorFallbackConflicts(cfg, routes)...)
	sort.SliceStable(recommendations, func(i, j int) bool {
		if recommendations[i].Severity != recommendations[j].Severity {
			return routerAdvisorSeverityRank(recommendations[i].Severity) > routerAdvisorSeverityRank(recommendations[j].Severity)
		}
		return recommendations[i].ID < recommendations[j].ID
	})
	return recommendations
}

func (h *Handler) routerAdvisorModelRoutes(cfg *config.Config) []routerAdvisorModelRoute {
	routes := []routerAdvisorModelRoute{}
	for _, key := range cfg.GeminiKey {
		routes = append(routes, routerAdvisorAPIModelRoutes("gemini", "Gemini API", "api", len(cfg.GeminiKey), key.Models)...)
		if len(key.Models) == 0 && strings.TrimSpace(key.APIKey) != "" {
			routes = append(routes, routerAdvisorModelRoute{Provider: "gemini", ProviderLabel: "Gemini API", Source: "api", CredentialCount: len(cfg.GeminiKey)})
		}
	}
	for _, key := range cfg.CodexKey {
		routes = append(routes, routerAdvisorAPIModelRoutes("codex", "Codex API", "api", len(cfg.CodexKey), key.Models)...)
		if len(key.Models) == 0 && strings.TrimSpace(key.APIKey) != "" {
			routes = append(routes, routerAdvisorModelRoute{Provider: "codex", ProviderLabel: "Codex API", Source: "api", CredentialCount: len(cfg.CodexKey)})
		}
	}
	for _, key := range cfg.ClaudeKey {
		routes = append(routes, routerAdvisorAPIModelRoutes("claude", "Claude API", "api", len(cfg.ClaudeKey), key.Models)...)
		if len(key.Models) == 0 && strings.TrimSpace(key.APIKey) != "" {
			routes = append(routes, routerAdvisorModelRoute{Provider: "claude", ProviderLabel: "Claude API", Source: "api", CredentialCount: len(cfg.ClaudeKey)})
		}
	}
	for _, key := range cfg.VertexCompatAPIKey {
		routes = append(routes, routerAdvisorAPIModelRoutes("vertex", "Vertex API", "api", len(cfg.VertexCompatAPIKey), key.Models)...)
		if len(key.Models) == 0 && strings.TrimSpace(key.APIKey) != "" {
			routes = append(routes, routerAdvisorModelRoute{Provider: "vertex", ProviderLabel: "Vertex API", Source: "api", CredentialCount: len(cfg.VertexCompatAPIKey)})
		}
	}
	for _, provider := range cfg.OpenAICompatibility {
		if provider.Disabled {
			continue
		}
		credentialCount := len(provider.APIKeyEntries)
		label := firstString(provider.Name, "OpenAI-Compatible API")
		for _, model := range provider.Models {
			routes = append(routes, routerAdvisorModelRoute{
				Provider:        "openaiCompatibility",
				ProviderLabel:   label,
				Source:          "api",
				Name:            strings.TrimSpace(model.Name),
				Alias:           strings.TrimSpace(model.Alias),
				CredentialCount: credentialCount,
			})
		}
		if len(provider.Models) == 0 && countOpenAICompatibilityCredentials([]config.OpenAICompatibility{provider}) > 0 {
			routes = append(routes, routerAdvisorModelRoute{
				Provider:        "openaiCompatibility",
				ProviderLabel:   label,
				Source:          "api",
				CredentialCount: credentialCount,
			})
		}
	}
	routes = append(routes, h.routerAdvisorOAuthModelRoutes()...)
	return routes
}

func routerAdvisorAPIModelRoutes[M interface {
	GetName() string
	GetAlias() string
}](provider, label, source string, credentialCount int, models []M) []routerAdvisorModelRoute {
	routes := make([]routerAdvisorModelRoute, 0, len(models))
	for _, model := range models {
		routes = append(routes, routerAdvisorModelRoute{
			Provider:        provider,
			ProviderLabel:   label,
			Source:          source,
			Name:            strings.TrimSpace(model.GetName()),
			Alias:           strings.TrimSpace(model.GetAlias()),
			CredentialCount: credentialCount,
		})
	}
	return routes
}

func (h *Handler) routerAdvisorOAuthModelRoutes() []routerAdvisorModelRoute {
	if h == nil || h.authManager == nil {
		return nil
	}
	modelRegistry := registry.GetGlobalRegistry()
	routes := []routerAdvisorModelRoute{}
	for _, auth := range h.authManager.List() {
		if auth == nil || auth.Disabled {
			continue
		}
		family := oauthProviderFamilyForAuth(auth)
		auth.EnsureIndex()
		models := modelRegistry.GetModelsForClient(auth.ID)
		for _, model := range models {
			if model == nil {
				continue
			}
			routes = append(routes, routerAdvisorModelRoute{
				Provider:        family,
				ProviderLabel:   strings.ToUpper(family[:1]) + family[1:] + " OAuth",
				Source:          "oauth",
				Name:            strings.TrimSpace(model.ID),
				Alias:           strings.TrimSpace(firstString(model.DisplayName, model.Name)),
				CredentialCount: 1,
			})
		}
		if len(models) == 0 {
			routes = append(routes, routerAdvisorModelRoute{
				Provider:        family,
				ProviderLabel:   strings.ToUpper(family[:1]) + family[1:] + " OAuth",
				Source:          "oauth",
				CredentialCount: 1,
			})
		}
	}
	return routes
}

func routerAdvisorDuplicateModels(routes []routerAdvisorModelRoute) []routerAdvisorRecommendation {
	byModel := map[string][]routerAdvisorModelRoute{}
	for _, route := range routes {
		for _, modelName := range []string{route.Name, route.Alias} {
			key := strings.ToLower(strings.TrimSpace(modelName))
			if key == "" {
				continue
			}
			byModel[key] = append(byModel[key], route)
		}
	}
	recommendations := []routerAdvisorRecommendation{}
	for model, matches := range byModel {
		if len(uniqueRouterAdvisorRouteLabels(matches)) < 2 {
			continue
		}
		labels := uniqueRouterAdvisorRouteLabels(matches)
		sort.Strings(labels)
		recommendations = append(recommendations, routerAdvisorRecommendation{
			ID:          "duplicate-model-" + routerAdvisorSlug(model),
			Category:    "duplicate_models",
			Severity:    "medium",
			Title:       "Duplicate model route: " + model,
			Description: "Multiple providers expose the same client-visible model name or alias. Routing may depend on priority, strategy, or credential cooling state.",
			Evidence: []string{
				"Model or alias " + model + " appears in " + strings.Join(labels, ", ") + ".",
			},
			SuggestedChange: "Give each provider route a distinct alias or intentionally document the priority/fallback behavior.",
			CopyText:        "# Review duplicate route for model " + model + "\n# Providers: " + strings.Join(labels, ", "),
		})
	}
	return recommendations
}

func routerAdvisorProvidersWithoutRoutes(routes []routerAdvisorModelRoute) []routerAdvisorRecommendation {
	byProvider := map[string]routerAdvisorModelRoute{}
	hasRoute := map[string]bool{}
	for _, route := range routes {
		key := route.Source + ":" + route.ProviderLabel
		if route.CredentialCount > 0 {
			byProvider[key] = route
		}
		if strings.TrimSpace(route.Name) != "" || strings.TrimSpace(route.Alias) != "" {
			hasRoute[key] = true
		}
	}
	recommendations := []routerAdvisorRecommendation{}
	for key, route := range byProvider {
		if hasRoute[key] {
			continue
		}
		recommendations = append(recommendations, routerAdvisorRecommendation{
			ID:              "provider-without-routes-" + routerAdvisorSlug(key),
			Category:        "provider_without_routes",
			Severity:        "medium",
			Title:           route.ProviderLabel + " has credentials but no model routes",
			Description:     "The provider has usable credential evidence but no configured or registered model routes for clients to select.",
			Evidence:        []string{fmt.Sprintf("%s credential count is %d and model route count is 0.", route.ProviderLabel, route.CredentialCount)},
			SuggestedChange: "Add provider model mappings or refresh OAuth model inventory before relying on this provider.",
			CopyText:        "# Add model routes for " + route.ProviderLabel,
		})
	}
	return recommendations
}

func (h *Handler) routerAdvisorOAuthAliasRecommendations(cfg *config.Config) []routerAdvisorRecommendation {
	if cfg == nil || len(cfg.OAuthModelAlias) == 0 {
		return nil
	}
	available := map[string]map[string]struct{}{}
	for _, route := range h.routerAdvisorOAuthModelRoutes() {
		provider := strings.ToLower(route.Provider)
		if _, ok := available[provider]; !ok {
			available[provider] = map[string]struct{}{}
		}
		for _, value := range []string{route.Name, route.Alias} {
			if trimmed := strings.ToLower(strings.TrimSpace(value)); trimmed != "" {
				available[provider][trimmed] = struct{}{}
			}
		}
	}
	recommendations := []routerAdvisorRecommendation{}
	for provider, aliases := range cfg.OAuthModelAlias {
		normalizedProvider := strings.ToLower(strings.TrimSpace(provider))
		for _, alias := range aliases {
			name := strings.TrimSpace(alias.Name)
			if name == "" {
				continue
			}
			if _, ok := available[normalizedProvider][strings.ToLower(name)]; ok {
				continue
			}
			recommendations = append(recommendations, routerAdvisorRecommendation{
				ID:              "oauth-alias-missing-" + routerAdvisorSlug(normalizedProvider+"-"+name),
				Category:        "alias_unavailable",
				Severity:        "high",
				Title:           "OAuth alias points to unavailable model: " + name,
				Description:     "An OAuth model alias references an upstream model that is not present in the registered OAuth model inventory.",
				Evidence:        []string{"oauth-model-alias for " + normalizedProvider + " references " + name + ", but that model is not registered for active OAuth files."},
				SuggestedChange: "Refresh OAuth model inventory or update the alias to a registered upstream model.",
				CopyText:        "# Review oauth-model-alias." + normalizedProvider + " entry for " + name,
			})
		}
	}
	return recommendations
}

func routerAdvisorFallbackConflicts(cfg *config.Config, routes []routerAdvisorModelRoute) []routerAdvisorRecommendation {
	if cfg == nil || len(cfg.AmpCode.ModelMappings) == 0 || cfg.AmpCode.ForceModelMappings {
		return nil
	}
	visibleModels := map[string]struct{}{}
	for _, route := range routes {
		for _, value := range []string{route.Name, route.Alias} {
			if trimmed := strings.ToLower(strings.TrimSpace(value)); trimmed != "" {
				visibleModels[trimmed] = struct{}{}
			}
		}
	}
	recommendations := []routerAdvisorRecommendation{}
	for _, mapping := range cfg.AmpCode.ModelMappings {
		from := strings.TrimSpace(mapping.From)
		if from == "" || mapping.Regex {
			continue
		}
		if _, ok := visibleModels[strings.ToLower(from)]; !ok {
			continue
		}
		recommendations = append(recommendations, routerAdvisorRecommendation{
			ID:              "amp-fallback-conflict-" + routerAdvisorSlug(from),
			Category:        "fallback_conflict",
			Severity:        "medium",
			Title:           "Amp fallback mapping may be shadowed: " + from,
			Description:     "Amp model mappings run after local API keys unless force-model-mappings is enabled, so this fallback may not apply while a local route exists.",
			Evidence:        []string{"ampcode model mapping from " + from + " exists and a local route with the same model or alias is configured."},
			SuggestedChange: "Enable ampcode.force-model-mappings only if the fallback should override local routes, or rename the local alias.",
			CopyText:        "ampcode:\n  force-model-mappings: true",
		})
	}
	return recommendations
}

func uniqueRouterAdvisorRouteLabels(routes []routerAdvisorModelRoute) []string {
	labels := []string{}
	for _, route := range routes {
		label := route.ProviderLabel + " (" + route.Source + ")"
		labels = appendUniqueStrings(labels, label)
	}
	return labels
}

func routerAdvisorSeverityRank(severity string) int {
	switch severity {
	case "high":
		return 3
	case "medium":
		return 2
	case "low":
		return 1
	default:
		return 0
	}
}

func routerAdvisorSlug(value string) string {
	trimmed := strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	lastDash := false
	for _, r := range trimmed {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func pluralSuffix(count int) string {
	if count == 1 {
		return ""
	}
	return "s"
}
