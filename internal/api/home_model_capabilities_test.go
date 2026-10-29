package api

import (
	"encoding/json"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func TestHomeOpenAIModelsPreserveUpstreamCapabilities(t *testing.T) {
	entries, err := decodeHomeModels([]byte(`{"openrouter":[{"id":"openai/gpt-6.1-sol","owned_by":"openrouter","context_length":1100000,"top_provider":{"max_completion_tokens":128000},"supported_parameters":["reasoning","tools"],"architecture":{"input_modalities":["text","image"]}},{"id":"unknown-home"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	models := formatHomeOpenAIModels(entries, nil)
	if len(models) != 2 {
		t.Fatalf("catalog size=%d", len(models))
	}
	sol := models[0]
	if sol["context_length"] != 1100000 || sol["max_output_tokens"] != 128000 || sol["supports_reasoning"] != true || sol["supports_tool_calls"] != true || sol["supports_image_input"] != true {
		t.Fatalf("Home lost OpenRouter metadata: %#v", sol)
	}
	if len(models[1]) != 2 {
		t.Fatalf("unknown Home capabilities fabricated: %#v", models[1])
	}
	var cfg config.SDKConfig
	if err := json.Unmarshal([]byte(`{"model-capabilities":{"unknown-home":{"source":"Home operator test","max_output_tokens":1234}}}`), &cfg); err != nil {
		t.Fatal(err)
	}
	if got := formatHomeOpenAIModels(entries, &cfg)[1]["max_output_tokens"]; got != 1234 {
		t.Fatalf("Home override=%v", got)
	}
}

func TestHomeIncludesDiscoveredLocalCapabilities(t *testing.T) {
	r := registry.GetGlobalRegistry()
	r.RegisterClient("home-local-capabilities", "bay", []*registry.ModelInfo{{ID: "local-home-reported", ContextLength: 16384, MaxCompletionTokens: 2048}})
	defer r.UnregisterClient("home-local-capabilities")
	cfg := &config.Config{OpenAICompatibility: []config.OpenAICompatibility{{Name: "Bay", DiscoverLocalModels: true}}}
	entries := appendHomeLocalModels([]homeModelEntry{{id: "remote-home"}}, cfg)
	got := formatHomeOpenAIModels(entries, &cfg.SDKConfig)
	if len(got) != 2 || got[0]["id"] != "local-home-reported" || got[0]["context_length"] != 16384 {
		t.Fatalf("Home local catalog missing: %#v", got)
	}
	cfg.OpenAICompatibility[0].Disabled = true
	if got := appendHomeLocalModels([]homeModelEntry{{id: "remote-home"}}, cfg); len(got) != 1 {
		t.Fatal("disabled local engine leaked")
	}
}

func TestHomeDuplicateAliasDoesNotBorrowAnotherRoutesCapabilities(t *testing.T) {
	entries, err := decodeHomeModels([]byte(`{"codex":[{"id":"shared-alias"}],"openrouter":[{"id":"shared-alias","context_length":1100000,"supports_reasoning":true}]}`))
	if err != nil {
		t.Fatal(err)
	}
	got := formatHomeOpenAIModels(entries, nil)
	if len(got) != 1 || len(got[0]) != 2 {
		t.Fatalf("ambiguous route inherited claims: %#v", got)
	}
}

func TestHomeRemoteLocalAndLocalLocalDuplicatesOmitCapabilities(t *testing.T) {
	r := registry.GetGlobalRegistry()
	for _, provider := range []string{"bay-a", "bay-b"} {
		r.RegisterClient("duplicate-"+provider, provider, []*registry.ModelInfo{{ID: "shared-local", ContextLength: 32768}})
		defer r.UnregisterClient("duplicate-" + provider)
	}
	cfg := &config.Config{OpenAICompatibility: []config.OpenAICompatibility{{Name: "bay-a", DiscoverLocalModels: true}, {Name: "bay-b", DiscoverLocalModels: true}}}
	for _, entries := range [][]homeModelEntry{nil, {{id: "shared-local", capabilities: registry.ModelCapabilities{ContextLength: 1100000}}}} {
		got := formatHomeOpenAIModels(appendHomeLocalModels(entries, cfg), nil)
		if len(got) != 1 {
			t.Fatalf("duplicate IDs not deduped: %#v", got)
		}
		if _, ok := got[0]["context_length"]; ok {
			t.Fatalf("ambiguous duplicate inherited limits: %#v", got)
		}
	}
}
