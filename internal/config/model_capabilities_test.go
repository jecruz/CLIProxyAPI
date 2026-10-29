package config

import (
	"gopkg.in/yaml.v3"
	"testing"
)

func TestModelCapabilitiesConfigKeepsSourceAndExplicitFalse(t *testing.T) {
	var cfg Config
	if err := yaml.Unmarshal([]byte("model-capabilities:\n  gpt-6.1-sol:\n    source: operator-verified-route\n    context-length: 32000\n    max-output-tokens: 4000\n    supports-reasoning: false\n"), &cfg); err != nil {
		t.Fatal(err)
	}
	c := cfg.ModelCapabilities["gpt-6.1-sol"]
	if c.Source != "operator-verified-route" || c.ContextLength != 32000 || c.MaxOutputTokens != 4000 || c.SupportsReasoning == nil || *c.SupportsReasoning {
		t.Fatalf("invalid config decode: %+v", c)
	}
	raw, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var again Config
	if err = yaml.Unmarshal(raw, &again); err != nil {
		t.Fatal(err)
	}
	if got := again.ModelCapabilities["gpt-6.1-sol"]; got.Source != c.Source || got.SupportsReasoning == nil || *got.SupportsReasoning {
		t.Fatal("capability provenance/false lost during save")
	}
	if len((&Config{}).ModelCapabilities) != 0 {
		t.Fatal("overrides must default empty")
	}
}
