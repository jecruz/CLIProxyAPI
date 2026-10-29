package cliproxy

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/localdiscovery"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestLocalDiscoveryRegistrationIgnoresStaticModelsAndWithdrawsOnStop(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[{"id":"actual-loaded-model","owned_by":"splash"}]}`)
	}))
	defer upstream.Close()
	p := config.OpenAICompatibility{Name: "Engine Bay", BaseURL: upstream.URL + "/v1", DiscoverLocalModels: true, Models: []config.OpenAICompatibilityModel{{Name: "stale-static"}}}
	s := &Service{cfg: &config.Config{OpenAICompatibility: []config.OpenAICompatibility{p}}, coreManager: coreauth.NewManager(nil, nil, nil)}
	s.localDiscovery = localdiscovery.New(s.refreshLocalDiscoveryRegistrations)
	s.localDiscovery.Configure(s.cfg.OpenAICompatibility)
	auths, err := synthesizer.NewConfigSynthesizer().Synthesize(&synthesizer.SynthesisContext{Config: s.cfg, IDGenerator: synthesizer.NewStableIDGenerator()})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range auths {
		s.handleAuthUpdate(coreauth.WithSkipPersist(context.Background()), watcher.AuthUpdate{Action: watcher.AuthUpdateActionAdd, Auth: a})
	}
	var id string
	for _, a := range s.coreManager.List() {
		if a.Label == "Engine Bay" {
			id = a.ID
		}
	}
	if id == "" {
		t.Fatal("keyless local provider was not synthesized")
	}
	defer GlobalModelRegistry().UnregisterClient(id)
	if len(registry.GetGlobalRegistry().GetModelsForClient(id)) != 0 {
		t.Fatal("unprobed provider leaked static alias")
	}
	s.localDiscovery.Refresh(context.Background())
	models := registry.GetGlobalRegistry().GetModelsForClient(id)
	if len(models) != 1 || models[0].ID != "actual-loaded-model" || models[0].OwnedBy != "Engine Bay (unknown engine)" {
		t.Fatalf("live discovery not wired to registry: %+v", models)
	}
	upstream.Close()
	s.localDiscovery.Refresh(context.Background())
	if len(registry.GetGlobalRegistry().GetModelsForClient(id)) != 0 {
		t.Fatal("stopped engine remains routable")
	}
	// Changing discovery off must restore explicit config models even for keyless auths.
	p.DiscoverLocalModels = false
	s.applyConfigUpdate(&config.Config{OpenAICompatibility: []config.OpenAICompatibility{p}})
	models = registry.GetGlobalRegistry().GetModelsForClient(id)
	if len(models) != 1 || models[0].ID != "stale-static" {
		t.Fatalf("opt-out did not rebind static models: %+v", models)
	}
	p.DiscoverLocalModels = true
	s.applyConfigUpdate(&config.Config{OpenAICompatibility: []config.OpenAICompatibility{p}})
	if len(registry.GetGlobalRegistry().GetModelsForClient(id)) != 0 {
		t.Fatal("enabling discovery on a down engine leaked static models")
	}
	p.Disabled = true
	s.applyConfigUpdate(&config.Config{OpenAICompatibility: []config.OpenAICompatibility{p}})
	if len(s.localDiscovery.Snapshot()) != 0 || len(registry.GetGlobalRegistry().GetModelsForClient(id)) != 0 {
		t.Fatal("disabled engine remains available")
	}
}

func TestLocalDiscoveryLifecycleStartsInNativeAndHomeModes(t *testing.T) {
	for _, home := range []bool{false, true} {
		t.Run(fmt.Sprintf("home=%t", home), func(t *testing.T) {
			probed := make(chan struct{}, 1)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/models" {
					fmt.Fprint(w, `{"data":[{"id":"live"}]}`)
					probed <- struct{}{}
				} else if r.URL.Path == "/v1/chat/completions" {
					fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"fixture-routed"},"finish_reason":"stop"}]}`)
				} else {
					t.Errorf("unexpected synthetic upstream request %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer upstream.Close()
			p := config.OpenAICompatibility{Name: "bay", BaseURL: upstream.URL + "/v1", DiscoverLocalModels: true}
			s := &Service{cfg: &config.Config{OpenAICompatibility: []config.OpenAICompatibility{p}}, coreManager: coreauth.NewManager(nil, nil, nil)}
			s.cfg.Home.Enabled = home
			s.localDiscovery = localdiscovery.New(s.refreshLocalDiscoveryRegistrations)
			s.localDiscovery.Configure(s.cfg.OpenAICompatibility)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s.startLocalDiscovery(ctx)
			select {
			case <-probed:
			case <-time.After(time.Second):
				t.Fatal("mode did not start discovery")
			}
			s.localDiscovery.Refresh(context.Background())
			cancel()
			select {
			case <-s.localDiscoveryDone:
			case <-time.After(time.Second):
				t.Fatal("discovery did not stop with service context")
			}
			if home {
				registered := false
				for _, a := range s.coreManager.List() {
					defer GlobalModelRegistry().UnregisterClient(a.ID)
					models := registry.GetGlobalRegistry().GetModelsForClient(a.ID)
					if a.Label == "bay" && len(models) == 1 && models[0].ID == "live" {
						registered = true
					}
				}
				if !registered {
					t.Fatal("Home startup probed but did not register a routable keyless local model")
				}
				executeCtx, stop := context.WithTimeout(context.Background(), 2*time.Second)
				defer stop()
				response, err := s.coreManager.Execute(executeCtx, []string{"bay"}, cliproxyexecutor.Request{Model: "live", Payload: []byte(`{"model":"live","messages":[{"role":"user","content":"fixture"}]}`)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")})
				if err != nil || !strings.Contains(string(response.Payload), "fixture-routed") {
					t.Fatalf("Home discovered model was not routable through synthetic upstream: %v", err)
				}
			}
		})
	}
}

func TestDiscoveryBlocksConfiguredBackendAndPublicListenerPorts(t *testing.T) {
	t.Setenv("LLMDYNAMIX_PROXY_PORT", "12454")
	c := localdiscovery.New(nil, localDiscoveryListenerPorts(&config.Config{Port: 12455})...)
	for _, port := range []string{"12454", "012454", "12455", "012455"} {
		c.Configure([]config.OpenAICompatibility{{Name: "bay", BaseURL: "http://127.0.0.1:" + port + "/v1", DiscoverLocalModels: true}})
		c.Refresh(context.Background())
		if c.Snapshot()[0].Reason != "invalid_endpoint" {
			t.Fatalf("configured self-listener accepted at %s", port)
		}
	}
}
