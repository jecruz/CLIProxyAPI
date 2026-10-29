package cliproxy

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func TestCompatibilityReasoningFallbackIsNotPublishedAsCapability(t *testing.T) {
	models := buildOpenAICompatibilityConfigModels(&config.OpenAICompatibility{Name: "custom", Models: []config.OpenAICompatibilityModel{{Name: "gpt-6.1-sol"}, {Name: "gpt-6.1-terra", Thinking: &registry.ThinkingSupport{Levels: []string{"high"}}}}})
	if models[0].Thinking == nil {
		t.Fatal("internal routing default changed")
	}
	if got := registry.ModelCapabilityFields(models[0]); len(got) != 0 {
		t.Fatalf("internal default advertised: %#v", got)
	}
	if got := registry.ModelCapabilityFields(models[1]); got["supports_reasoning"] != true {
		t.Fatalf("explicit config reasoning lost: %#v", got)
	}
}

func TestConfigAliasesPreserveVerifiedRegistryMetadata(t *testing.T) {
	upstream := registry.GetClaudeModels()[0]
	models := buildClaudeConfigModels(&config.ClaudeKey{Models: []config.ClaudeModel{{Name: upstream.ID, Alias: "verified-claude-alias"}}})
	if len(models) != 1 || models[0].ID != "verified-claude-alias" || models[0].ContextLength != upstream.ContextLength || models[0].MaxCompletionTokens != upstream.MaxCompletionTokens {
		t.Fatalf("alias discarded registry metadata: %+v", models)
	}
}
