package management

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestGetProviderDoctorReportsAPIAndOAuthEvidence(t *testing.T) {
	gin.SetMode(gin.TestMode)
	manager := coreauth.NewManager(nil, nil, nil)
	authID := "provider-doctor-gemini-auth"
	lastRefresh := time.Date(2026, 7, 6, 1, 2, 3, 0, time.UTC)
	nextRetry := lastRefresh.Add(10 * time.Minute)
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:               authID,
		Provider:         "gemini-cli",
		Status:           coreauth.StatusError,
		StatusMessage:    "Authorization: Bearer secret-token-value failed",
		LastRefreshedAt:  lastRefresh,
		NextRetryAfter:   nextRetry,
		LastError:        &coreauth.Error{Code: "quota", Message: "api_key=secret-key-value quota exceeded", HTTPStatus: http.StatusTooManyRequests, Retryable: true},
		Attributes:       map[string]string{"path": "/tmp/gemini.json"},
		Metadata:         map[string]any{"type": "gemini-cli"},
		ModelStates:      map[string]*coreauth.ModelState{},
		NextRefreshAfter: nextRetry,
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(authID, "gemini", []*registry.ModelInfo{{ID: "gemini-test-model"}})

	h := NewHandler(&config.Config{
		GeminiKey: []config.GeminiKey{{
			APIKey: "gemini-api-secret",
			Models: []config.GeminiModel{{
				Name:  "gemini-upstream",
				Alias: "gemini-alias",
			}},
		}},
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name: "OpenRouter",
			APIKeyEntries: []config.OpenAICompatibilityAPIKey{
				{APIKey: "openrouter-secret"},
				{APIKey: "openrouter-secret-two"},
			},
			Models: []config.OpenAICompatibilityModel{{Name: "upstream", Alias: "alias"}},
		}},
	}, "", manager)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/v0/management/provider-doctor", nil)

	h.GetProviderDoctor(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, secret := range []string{"secret-token-value", "secret-key-value", "gemini-api-secret", "openrouter-secret"} {
		if strings.Contains(body, secret) {
			t.Fatalf("response leaked secret %q: %s", secret, body)
		}
	}

	var payload providerDoctorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode provider doctor response: %v", err)
	}

	geminiAPI := findProviderDoctorEntry(payload.APIProviders, "gemini")
	if geminiAPI.CredentialCount != 1 || geminiAPI.ModelCount != 1 || geminiAPI.Status != "ready" {
		t.Fatalf("unexpected gemini API evidence: %+v", geminiAPI)
	}
	openaiAPI := findProviderDoctorEntry(payload.APIProviders, "openaiCompatibility")
	if openaiAPI.CredentialCount != 2 || openaiAPI.ModelCount != 1 || openaiAPI.Status != "ready" {
		t.Fatalf("unexpected openai-compatible evidence: %+v", openaiAPI)
	}
	geminiOAuth := findProviderDoctorEntry(payload.OAuthProviders, "gemini")
	if geminiOAuth.CredentialCount != 1 || geminiOAuth.ModelCount < 1 || geminiOAuth.Status != "review" {
		t.Fatalf("unexpected gemini OAuth evidence: %+v", geminiOAuth)
	}
	if geminiOAuth.LastFailureCategory != "quota" || geminiOAuth.LastRefreshState != "failed" {
		t.Fatalf("unexpected failure evidence: %+v", geminiOAuth)
	}
	if !strings.Contains(geminiOAuth.LastFailureMessage, "[redacted]") || !strings.Contains(geminiOAuth.StatusMessage, "[redacted]") {
		t.Fatalf("expected redacted messages, got %+v", geminiOAuth)
	}
}

func findProviderDoctorEntry(entries []providerDoctorEntry, id string) providerDoctorEntry {
	for _, entry := range entries {
		if entry.ID == id {
			return entry
		}
	}
	return providerDoctorEntry{}
}

func TestCheckProviderRouteAPIConfiguredModelReady(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewHandler(&config.Config{
		GeminiKey: []config.GeminiKey{{
			APIKey: "gemini-api-secret",
			Models: []config.GeminiModel{{
				Name:  "gemini-3-pro",
				Alias: "gemini-current",
			}},
		}},
	}, "", nil)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v0/management/provider-doctor/check", bytes.NewBufferString(`{"provider":"gemini","kind":"api","model":"gemini-current"}`))
	c.Request.Header.Set("Content-Type", "application/json")

	h.CheckProviderRoute(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "gemini-api-secret") {
		t.Fatalf("response leaked secret: %s", body)
	}
	var payload providerDoctorCheckResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode route check response: %v", err)
	}
	if payload.Status != "ready" || payload.FailureCategory != "" {
		t.Fatalf("unexpected route check response: %+v", payload)
	}
	if len(payload.MatchedModels) != 1 || payload.MatchedModels[0] != "gemini-current" {
		t.Fatalf("unexpected matched models: %+v", payload.MatchedModels)
	}
}

func TestCheckProviderRouteOAuthMissingModelReportsFailureCategory(t *testing.T) {
	gin.SetMode(gin.TestMode)
	manager := coreauth.NewManager(nil, nil, nil)
	authID := "provider-doctor-codex-auth"
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:         authID,
		Provider:   "codex",
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{"path": "/tmp/codex.json"},
		Metadata:   map[string]any{"type": "codex"},
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: "gpt-5"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(authID)
	})

	h := NewHandler(&config.Config{}, "", manager)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v0/management/provider-doctor/check", bytes.NewBufferString(`{"provider":"codex","kind":"oauth","model":"missing-model"}`))
	c.Request.Header.Set("Content-Type", "application/json")

	h.CheckProviderRoute(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var payload providerDoctorCheckResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode route check response: %v", err)
	}
	if payload.Status != "review" || payload.FailureCategory != "missing_model" {
		t.Fatalf("unexpected route check response: %+v", payload)
	}
	if len(payload.MatchedModels) != 1 || payload.MatchedModels[0] != "gpt-5" {
		t.Fatalf("unexpected matched models: %+v", payload.MatchedModels)
	}
}

func TestCheckProviderRouteMissingProvider(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewHandler(&config.Config{}, "", nil)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v0/management/provider-doctor/check", bytes.NewBufferString(`{"provider":"gemini","kind":"auto"}`))
	c.Request.Header.Set("Content-Type", "application/json")

	h.CheckProviderRoute(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var payload providerDoctorCheckResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode route check response: %v", err)
	}
	if payload.Status != "missing" || payload.FailureCategory != "missing_credential" {
		t.Fatalf("unexpected route check response: %+v", payload)
	}
}

func TestLiveProbeProviderRequiresApproval(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewHandler(&config.Config{}, "", nil)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v0/management/provider-doctor/live-probe", bytes.NewBufferString(`{"provider":"gemini","kind":"api","approved":false}`))
	c.Request.Header.Set("Content-Type", "application/json")

	h.LiveProbeProvider(c)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "approval_required") {
		t.Fatalf("expected approval error, got %s", rec.Body.String())
	}
}

func TestLiveProbeProviderOpenAICompatibilityRedactsSecretAndCategorizesAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const secret = "sk-live-probe-secret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+secret {
			t.Fatalf("Authorization header = %q", got)
		}
		http.Error(w, `{"error":"bad key `+secret+`"}`, http.StatusUnauthorized)
	}))
	t.Cleanup(server.Close)

	h := NewHandler(&config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:    "Test Compat",
			BaseURL: server.URL + "/v1?api_key=" + secret,
			APIKeyEntries: []config.OpenAICompatibilityAPIKey{
				{APIKey: ""},
				{APIKey: secret},
			},
			Models: []config.OpenAICompatibilityModel{{Name: "upstream", Alias: "alias"}},
		}},
	}, "", nil)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v0/management/provider-doctor/live-probe", bytes.NewBufferString(`{"provider":"openaiCompatibility","kind":"api","model":"alias","approved":true}`))
	c.Request.Header.Set("Content-Type", "application/json")

	h.LiveProbeProvider(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, secret) || strings.Contains(body, "api_key=") || strings.Contains(body, "bad key") {
		t.Fatalf("live probe response leaked secret material: %s", body)
	}
	var payload providerDoctorLiveProbeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode live probe response: %v", err)
	}
	if payload.Status != "review" || payload.FailureCategory != "auth_failed" || payload.HTTPStatus != http.StatusUnauthorized {
		t.Fatalf("unexpected live probe response: %+v", payload)
	}
	if payload.Endpoint != server.URL+"/v1/models" {
		t.Fatalf("endpoint = %q", payload.Endpoint)
	}
}

func TestLiveProbeProviderOAuthReportsUnsupportedWithoutUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	manager := coreauth.NewManager(nil, nil, nil)
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "provider-doctor-oauth-live-auth",
		Provider: "gemini-cli",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{"type": "gemini-cli"},
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	h := NewHandler(&config.Config{}, "", manager)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v0/management/provider-doctor/live-probe", bytes.NewBufferString(`{"provider":"gemini","kind":"oauth","approved":true}`))
	c.Request.Header.Set("Content-Type", "application/json")

	h.LiveProbeProvider(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var payload providerDoctorLiveProbeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode live probe response: %v", err)
	}
	if payload.Status != "review" || payload.FailureCategory != "oauth_live_probe_not_supported" {
		t.Fatalf("unexpected OAuth live probe response: %+v", payload)
	}
}
