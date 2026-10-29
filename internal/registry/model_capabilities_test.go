package registry

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestCapabilitiesFromProviderMetadata(t *testing.T) {
	var metadata map[string]any
	if err := json.Unmarshal([]byte(`{"context_length":1100000,"top_provider":{"max_completion_tokens":128000},"supported_parameters":["reasoning","tools"],"architecture":{"input_modalities":["text","image"]}}`), &metadata); err != nil {
		t.Fatal(err)
	}
	got := CapabilitiesFromMetadata(metadata).Fields()
	want := map[string]any{"context_length": 1100000, "max_output_tokens": 128000, "supports_reasoning": true, "supports_tool_calls": true, "supports_image_input": true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("provider metadata = %#v, want %#v", got, want)
	}
	for _, metadata := range []map[string]any{{}, {"context_length": -1, "max_output_tokens": 1.5, "supports_reasoning": "yes", "reasoning_levels": []any{5}}, {"context_length": 1e100}} {
		if got := CapabilitiesFromMetadata(metadata).Fields(); len(got) != 0 {
			t.Fatalf("invalid/unknown metadata published: %#v", got)
		}
	}
	got = CapabilitiesFromMetadata(map[string]any{"supports_reasoning": false, "supports_tool_calls": false, "supports_image_input": false}).Fields()
	if len(got) != 3 || got["supports_reasoning"] != false {
		t.Fatalf("explicit negatives lost: %#v", got)
	}
}

func TestCapabilityOverridesRequireSourceAndKeepExplicitFalse(t *testing.T) {
	var cfg map[string]ModelCapabilities
	if err := json.Unmarshal([]byte(`{"sol":{"source":"operator fixture","context_length":32000,"supports_reasoning":false},"unknown":{"context_length":999}}`), &cfg); err != nil {
		t.Fatal(err)
	}
	model := map[string]any{"id": "sol", "supports_reasoning": true, "reasoning_levels": []string{"high"}, "unrelated": "keep"}
	ApplyModelCapabilities(model, cfg)
	if model["context_length"] != 32000 || model["supports_reasoning"] != false || model["capability_source"] != "operator fixture" {
		t.Fatalf("override not applied: %#v", model)
	}
	if _, ok := model["reasoning_levels"]; ok {
		t.Fatal("false reasoning retained levels")
	}
	unknown := map[string]any{"id": "unknown"}
	ApplyModelCapabilities(unknown, cfg)
	if len(unknown) != 1 {
		t.Fatalf("unsourced override published: %#v", unknown)
	}
}
