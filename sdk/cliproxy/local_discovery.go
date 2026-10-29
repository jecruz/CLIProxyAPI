package cliproxy

import (
	"context"
	"os"
	"strconv"
	"strings"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

// refreshLocalDiscoveryRegistrations publishes catalogs without changing user config or auth files.
func (s *Service) refreshLocalDiscoveryRegistrations() {
	s.configUpdateMu.Lock()
	defer s.configUpdateMu.Unlock()
	names := make(map[string]bool)
	if s.cfg != nil {
		for _, provider := range s.cfg.OpenAICompatibility {
			if provider.DiscoverLocalModels {
				names[strings.ToLower(provider.Name)] = true
			}
		}
	}
	s.refreshLocalDiscoveryRegistrationsLocked(names)
}

// refreshLocalDiscoveryRegistrationsLocked also reconciles opt-outs and removals during config updates.
func (s *Service) refreshLocalDiscoveryRegistrationsLocked(names map[string]bool) {
	if s.coreManager == nil {
		return
	}
	ctx := coreauth.WithSkipPersist(context.Background())
	for _, a := range s.coreManager.List() {
		_, name, compatible := openAICompatInfoFromAuth(a)
		if !compatible || !names[strings.ToLower(name)] {
			continue
		}
		s.completeModelRegistrationForAuth(ctx, a)
	}
}

// startLocalDiscovery owns the same catalog lifecycle in native and Home modes.
func (s *Service) startLocalDiscovery(ctx context.Context) {
	if s.cfg != nil && s.cfg.Home.Enabled {
		s.registerConfigAPIKeyAuths(coreauth.WithSkipPersist(ctx), s.cfg)
	}
	discoveryCtx, cancel := context.WithCancel(ctx)
	s.localDiscoveryCancel = cancel
	s.localDiscoveryDone = make(chan struct{})
	go func() {
		defer close(s.localDiscoveryDone)
		s.localDiscovery.Run(discoveryCtx)
	}()
}

// localDiscoveryListenerPorts excludes the actual backend and app-provided public listener.
func localDiscoveryListenerPorts(cfg *config.Config) []int {
	ports := []int{}
	if cfg != nil {
		ports = append(ports, cfg.Port)
	}
	if port, err := strconv.Atoi(os.Getenv("LLMDYNAMIX_PROXY_PORT")); err == nil && port > 0 && port <= 65535 {
		ports = append(ports, port)
	}
	return ports
}
