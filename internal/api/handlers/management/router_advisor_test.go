package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestRouterAdvisorDetectsDuplicateModelsAndProviderWithoutRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewHandler(&config.Config{
		GeminiKey: []config.GeminiKey{{
			APIKey: "gemini-secret",
			Models: []config.GeminiModel{{
				Name:  "gemini-upstream",
				Alias: "shared-model",
			}},
		}},
		ClaudeKey: []config.ClaudeKey{{
			APIKey: "claude-secret",
			Models: []config.ClaudeModel{{
				Name:  "claude-upstream",
				Alias: "shared-model",
			}},
		}},
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name: "OpenRouter",
			APIKeyEntries: []config.OpenAICompatibilityAPIKey{{
				APIKey: "openrouter-secret",
			}},
			BaseURL: "https://openrouter.ai/api/v1",
		}},
	}, "", nil)

	payload := runRouterAdvisor(t, h)
	if !routerAdvisorHasCategory(payload, "duplicate_models") {
		t.Fatalf("expected duplicate model recommendation, got %+v", payload.Recommendations)
	}
	if !routerAdvisorHasCategory(payload, "provider_without_routes") {
		t.Fatalf("expected provider without routes recommendation, got %+v", payload.Recommendations)
	}
	body, _ := json.Marshal(payload)
	for _, secret := range []string{"gemini-secret", "claude-secret", "openrouter-secret"} {
		if strings.Contains(string(body), secret) {
			t.Fatalf("router advisor leaked secret %q: %s", secret, body)
		}
	}
}

func TestRouterAdvisorDetectsOAuthAliasUnavailable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	manager := coreauth.NewManager(nil, nil, nil)
	authID := "router-advisor-gemini-auth"
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       authID,
		Provider: "gemini-cli",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{"type": "gemini-cli"},
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(authID, "gemini", []*registry.ModelInfo{{ID: "gemini-live-model"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(authID)
	})

	h := NewHandler(&config.Config{
		OAuthModelAlias: map[string][]config.OAuthModelAlias{
			"gemini": {{Name: "missing-upstream", Alias: "friendly-missing"}},
		},
	}, "", manager)

	payload := runRouterAdvisor(t, h)
	if !routerAdvisorHasCategory(payload, "alias_unavailable") {
		t.Fatalf("expected alias unavailable recommendation, got %+v", payload.Recommendations)
	}
}

func TestRouterAdvisorDetectsAmpFallbackConflict(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewHandler(&config.Config{
		ClaudeKey: []config.ClaudeKey{{
			APIKey: "claude-secret",
			Models: []config.ClaudeModel{{
				Name:  "claude-opus-4.5",
				Alias: "amp-model",
			}},
		}},
		AmpCode: config.AmpCode{
			ForceModelMappings: false,
			ModelMappings: []config.AmpModelMapping{{
				From: "amp-model",
				To:   "claude-sonnet-4",
			}},
		},
	}, "", nil)

	payload := runRouterAdvisor(t, h)
	if !routerAdvisorHasCategory(payload, "fallback_conflict") {
		t.Fatalf("expected fallback conflict recommendation, got %+v", payload.Recommendations)
	}
}

func TestRouterAdvisorReportsOKWhenNoRecommendations(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewHandler(&config.Config{}, "", nil)

	payload := runRouterAdvisor(t, h)
	if payload.Status != "ok" || len(payload.Recommendations) != 0 {
		t.Fatalf("expected ok empty response, got %+v", payload)
	}
}

func runRouterAdvisor(t *testing.T, h *Handler) routerAdvisorResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/v0/management/router-advisor", nil)

	h.GetRouterAdvisor(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var payload routerAdvisorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode router advisor response: %v", err)
	}
	return payload
}

func routerAdvisorHasCategory(payload routerAdvisorResponse, category string) bool {
	for _, recommendation := range payload.Recommendations {
		if recommendation.Category == category {
			return true
		}
	}
	return false
}
