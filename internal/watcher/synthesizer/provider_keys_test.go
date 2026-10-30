package synthesizer

import (
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

// TestProviderReferencesFailClosed rejects an entire mixed provider without its key.
func TestProviderReferencesFailClosed(t *testing.T) {
	for _, reference := range []string{
		"keychain://caiss/providers/MISSING", "keychain://other/providers/NAME",
		"keychain://caiss/providers/NAME?query", "KEYCHAIN://caiss/providers/NAME",
	} {
		t.Run(reference, func(t *testing.T) {
			cfg := &config.Config{OpenAICompatibility: []config.OpenAICompatibility{
				{Name: "protected", BaseURL: "http://127.0.0.1", APIKeyEntries: []config.OpenAICompatibilityAPIKey{
					{APIKey: "literal-fixture"}, {APIKey: reference},
				}},
				{Name: "unrelated", BaseURL: "http://127.0.0.1"},
			}}
			auths, err := NewConfigSynthesizer().Synthesize(&SynthesisContext{
				Config: cfg, Now: time.Now(), IDGenerator: NewStableIDGenerator(),
			})
			if err != nil || len(auths) != 1 || auths[0].Provider != "unrelated" {
				t.Fatal("unresolved reference must disable the whole provider without affecting unrelated providers")
			}
			if cfg.OpenAICompatibility[0].APIKeyEntries[1].APIKey != reference {
				t.Fatal("config references must remain unchanged")
			}
		})
	}
}
