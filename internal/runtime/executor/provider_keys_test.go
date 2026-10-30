package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/providerkeys"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher/synthesizer"
)

// TestProviderReferenceReachesPrivateUpstream verifies real HTTP authorization across launch generations.
func TestProviderReferenceReachesPrivateUpstream(t *testing.T) {
	received := make(chan bool, 2)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expected := "Bearer sentinel-" + strings.TrimPrefix(r.URL.Path, "/") + "-fixture-only"
		received <- r.Header.Get("Authorization") == expected
		w.WriteHeader(http.StatusServiceUnavailable) // Error paths must not serialize credentials either.
	}))
	defer upstream.Close()
	for _, generation := range []string{"one", "two"} {
		value := "sentinel-" + generation + "-fixture-only"
		t.Setenv(providerkeys.EnvironmentName, `{"keychain://caiss/providers/FIXTURE":"`+value+`"}`)
		if providerkeys.ConsumeEnvironment() != nil {
			t.Fatal("fixture transport rejected")
		}
		cfg := &config.Config{OpenAICompatibility: []config.OpenAICompatibility{{Name: "fixture", BaseURL: upstream.URL,
			APIKeyEntries: []config.OpenAICompatibilityAPIKey{{APIKey: "keychain://caiss/providers/FIXTURE"}},
		}}}
		auths, err := synthesizer.NewConfigSynthesizer().Synthesize(&synthesizer.SynthesisContext{Config: cfg, Now: time.Now(), IDGenerator: synthesizer.NewStableIDGenerator()})
		if err != nil || len(auths) != 1 || auths[0].Attributes["runtime_only"] != "true" || cfg.OpenAICompatibility[0].APIKeyEntries[0].APIKey != "keychain://caiss/providers/FIXTURE" {
			t.Fatal("fixture auth synthesis failed")
		}
		req := httptest.NewRequest(http.MethodGet, upstream.URL+"/"+generation, nil)
		req.RequestURI = ""
		resp, err := NewOpenAICompatExecutor("fixture", cfg).HttpRequest(context.Background(), auths[0], req)
		if err != nil {
			t.Fatal("private upstream request failed")
		}
		_ = resp.Body.Close()
		if !<-received {
			t.Fatal("private upstream did not receive the resolved credential")
		}
	}
}
