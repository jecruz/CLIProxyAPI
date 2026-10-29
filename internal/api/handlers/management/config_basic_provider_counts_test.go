package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestGetConfigIncludesLiveProviderCounts(t *testing.T) {
	t.Parallel()

	manager := coreauth.NewManager(nil, nil, nil)
	mustRegister := func(auth *coreauth.Auth) {
		t.Helper()
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatalf("register auth %q: %v", auth.ID, err)
		}
	}

	mustRegister(&coreauth.Auth{
		ID:       "gemini-live",
		Provider: "gemini-cli",
		Status:   coreauth.StatusActive,
	})
	mustRegister(&coreauth.Auth{
		ID:       "codex-live",
		Provider: "codex",
		Status:   coreauth.StatusActive,
	})
	mustRegister(&coreauth.Auth{
		ID:       "claude-disabled",
		Provider: "anthropic",
		Status:   coreauth.StatusDisabled,
		Disabled: true,
	})
	mustRegister(&coreauth.Auth{
		ID:       "openai-live",
		Provider: "openrouter",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"compat_name":  "Openrouter",
			"provider_key": "openrouter",
		},
	})

	h := &Handler{
		cfg: &config.Config{
			OpenAICompatibility: []config.OpenAICompatibility{{
				Name:    "Openrouter",
				BaseURL: "https://openrouter.ai/api/v1",
			}},
		},
		authManager: manager,
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/v0/management/config", nil)

	h.GetConfig(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	rawCounts, ok := got["provider-counts"].(map[string]any)
	if !ok {
		t.Fatalf("provider-counts missing or wrong type: %#v", got["provider-counts"])
	}

	expectCount := func(name string, wantTotal, wantActive int) {
		t.Helper()
		raw, ok := rawCounts[name].(map[string]any)
		if !ok {
			t.Fatalf("provider count %q missing or wrong type: %#v", name, rawCounts[name])
		}
		gotTotal := int(raw["total"].(float64))
		gotActive := int(raw["active"].(float64))
		if gotTotal != wantTotal || gotActive != wantActive {
			t.Fatalf("provider count %q = (%d,%d), want (%d,%d)", name, gotTotal, gotActive, wantTotal, wantActive)
		}
	}

	expectCount("gemini", 1, 1)
	expectCount("codex", 1, 1)
	expectCount("claude", 1, 0)
	expectCount("openaiCompatibility", 1, 1)
}

func TestProviderFamilyFromProviderToken(t *testing.T) {
	t.Parallel()

	mgr := coreauth.NewManager(nil, nil, nil)
	if _, err := mgr.Register(context.Background(), &coreauth.Auth{
		ID:       "openrouter-live",
		Provider: "openrouter",
		Status:   coreauth.StatusActive,
	}); err != nil {
		t.Fatalf("register openrouter auth: %v", err)
	}

	h := &Handler{
		cfg: &config.Config{
			OpenAICompatibility: []config.OpenAICompatibility{{
				Name:    "Openrouter",
				BaseURL: "https://openrouter.ai/api/v1",
			}},
		},
		authManager: mgr,
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/v0/management/config", nil)

	h.GetConfig(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	rawCounts, ok := got["provider-counts"].(map[string]any)
	if !ok {
		t.Fatalf("provider-counts missing or wrong type: %#v", got["provider-counts"])
	}
	rawOpenAI, ok := rawCounts["openaiCompatibility"].(map[string]any)
	if !ok {
		t.Fatalf("openaiCompatibility missing: %#v", rawCounts)
	}
	if total := int(rawOpenAI["total"].(float64)); total != 1 {
		t.Fatalf("openaiCompatibility total = %d, want 1", total)
	}
}

func TestProviderFamilyFromMetadataTypeFallback(t *testing.T) {
	t.Parallel()

	mgr := coreauth.NewManager(nil, nil, nil)
	if _, err := mgr.Register(context.Background(), &coreauth.Auth{
		ID:       "metatype-openrouter",
		Provider: "",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{"type": "openrouter"},
	}); err != nil {
		t.Fatalf("register openrouter auth: %v", err)
	}

	h := &Handler{
		cfg: &config.Config{OpenAICompatibility: []config.OpenAICompatibility{{
			Name:    "Openrouter",
			BaseURL: "https://openrouter.ai/api/v1",
		}}},
		authManager: mgr,
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/v0/management/config", nil)

	h.GetConfig(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	rawCounts, ok := got["provider-counts"].(map[string]any)
	if !ok {
		t.Fatalf("provider-counts missing or wrong type: %#v", got["provider-counts"])
	}
	rawOpenAI, ok := rawCounts["openaiCompatibility"].(map[string]any)
	if !ok {
		t.Fatalf("openaiCompatibility missing: %#v", rawCounts)
	}
	if total := int(rawOpenAI["total"].(float64)); total != 1 {
		t.Fatalf("openaiCompatibility total = %d, want 1", total)
	}
}
