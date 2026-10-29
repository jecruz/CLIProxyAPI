package openai

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
)

func TestOpenAIModelsPublishesVerifiedCapabilitiesOnly(t *testing.T) {
	r := registry.GetGlobalRegistry()
	r.RegisterClient("capability-test", "openai", []*registry.ModelInfo{
		{ID: "gpt-6.1-sol", ContextLength: 32000, MaxCompletionTokens: 4000, Thinking: &registry.ThinkingSupport{Levels: []string{"low", "high"}}, SupportedParameters: []string{"tools"}, SupportedInputModalities: []string{"TEXT", "IMAGE"}},
		{ID: "gpt-6.1-terra"}, {ID: "unknown-capability-model"},
	})
	defer r.UnregisterClient("capability-test")
	var cfg config.SDKConfig
	// Synthetic operator-supplied values test plumbing, not real route specifications.
	if err := json.Unmarshal([]byte(`{"model-capabilities":{"gpt-6.1-terra":{"source":"test operator table","context_length":64000,"max_output_tokens":8000,"supports_reasoning":false,"supports_tool_calls":true,"supports_image_input":false}}}`), &cfg); err != nil {
		t.Fatal(err)
	}
	h := NewOpenAIAPIHandler(handlers.NewBaseAPIHandlers(&cfg, nil))
	rr := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rr)
	c.Request = httptest.NewRequest("GET", "/v1/models", nil)
	h.OpenAIModels(c)
	var response struct {
		Object string           `json:"object"`
		Data   []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	byID := map[string]map[string]any{}
	for _, m := range response.Data {
		byID[m["id"].(string)] = m
	}
	sol := byID["gpt-6.1-sol"]
	for key, want := range map[string]any{"context_length": float64(32000), "max_output_tokens": float64(4000), "supports_reasoning": true, "supports_tool_calls": true, "supports_image_input": true} {
		if sol[key] != want {
			t.Errorf("Sol %s=%v, want %v", key, sol[key], want)
		}
	}
	if levels, ok := sol["reasoning_levels"].([]any); !ok || len(levels) != 2 {
		t.Fatal("reasoning levels missing")
	}
	terra := byID["gpt-6.1-terra"]
	if terra["context_length"] != float64(64000) || terra["supports_reasoning"] != false || terra["supports_image_input"] != false {
		t.Errorf("Terra override missing: %#v", terra)
	}
	if unknown := byID["unknown-capability-model"]; len(unknown) != 3 {
		t.Errorf("unknown gained capabilities: %#v", unknown)
	}
	if response.Object != "list" {
		t.Error("OpenAI envelope changed")
	}
}
