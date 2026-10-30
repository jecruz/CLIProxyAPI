// Package localdiscovery maintains live catalogs for explicitly configured local engines.
// It never starts processes, writes configuration, or makes inference requests.
package localdiscovery

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/providerkeys"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

const maxBodyBytes = 1024 * 1024

// Status is a bounded, credential-free snapshot of one local provider's catalog.
type Status struct {
	Provider  string                `json:"provider"`
	Available bool                  `json:"available"`
	Reason    string                `json:"reason"`
	CheckedAt time.Time             `json:"checked_at"`
	Models    []*registry.ModelInfo `json:"models"`
}

type entry struct {
	provider config.OpenAICompatibility
	status   Status
}

type refreshOperation struct {
	done       chan struct{}
	generation uint64
	complete   bool
}

// Controller owns an in-memory catalog independent of config persistence and inference.
type Controller struct {
	mu           sync.RWMutex
	active       *refreshOperation
	generation   uint64
	entries      map[string]entry
	onChange     func()
	client       *http.Client
	blockedPorts []int
}

// New creates a loopback-only, no-proxy, no-redirect discovery controller.
func New(onChange func(), listenerPorts ...int) *Controller {
	return &Controller{entries: make(map[string]entry), onChange: onChange, blockedPorts: append([]int(nil), listenerPorts...), client: &http.Client{
		Timeout:       2 * time.Second,
		Transport:     &http.Transport{Proxy: nil, DisableKeepAlives: true},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

// Configure snapshots enabled opt-in providers and invalidates changed endpoints immediately.
func (c *Controller) Configure(providers []config.OpenAICompatibility) {
	c.mu.Lock()
	defer c.mu.Unlock()
	changed := false
	next := make(map[string]entry)
	for _, p := range providers {
		if !p.DiscoverLocalModels || p.Disabled {
			continue
		}
		key := strings.ToLower(p.Name)
		// Copy credential/header containers: callers may replace their config later.
		p.APIKeyEntries = append([]config.OpenAICompatibilityAPIKey(nil), p.APIKeyEntries...)
		headers := make(map[string]string, len(p.Headers))
		for k, v := range p.Headers {
			headers[k] = v
		}
		p.Headers = headers
		e := entry{provider: p, status: Status{Provider: p.Name, Reason: "not_checked", Models: []*registry.ModelInfo{}}}
		if old, ok := c.entries[key]; ok && sameDiscoveryInputs(old.provider, p) {
			e.status = old.status
		} else {
			changed = true
		}
		next[key] = e
	}
	if changed || len(c.entries) != len(next) {
		c.generation++
	}
	c.entries = next
}

func sameDiscoveryInputs(a, b config.OpenAICompatibility) bool {
	if a.Name != b.Name || a.BaseURL != b.BaseURL || !reflect.DeepEqual(a.Headers, b.Headers) || len(a.APIKeyEntries) != len(b.APIKeyEntries) {
		return false
	}
	// Probes always bypass proxies; per-key routing proxy settings do not affect discovery.
	for i := range a.APIKeyEntries {
		if a.APIKeyEntries[i].APIKey != b.APIKeyEntries[i].APIKey {
			return false
		}
	}
	return true
}

// Models returns independent model records only for the current configured endpoint.
func (c *Controller) Models(name, baseURL string) []*registry.ModelInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.entries[strings.ToLower(name)]
	if !ok || e.provider.BaseURL != baseURL {
		return nil
	}
	return cloneModels(e.status.Models)
}

// Snapshot returns deterministic status records without credentials or upstream response bodies.
func (c *Controller) Snapshot() []Status {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]Status, 0, len(c.entries))
	for _, e := range c.entries {
		s := e.status
		s.Models = cloneModels(s.Models)
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Provider < out[j].Provider })
	return out
}

// Refresh probes catalogs once; concurrent demands coalesce with the active refresh.
func (c *Controller) Refresh(ctx context.Context) {
	var operation *refreshOperation
	for {
		if ctx.Err() != nil {
			return
		}
		c.mu.Lock()
		if active := c.active; active != nil {
			c.mu.Unlock()
			select {
			case <-active.done:
			case <-ctx.Done():
				return
			}
			c.mu.RLock()
			current := active.complete && active.generation == c.generation
			c.mu.RUnlock()
			if current {
				return
			}
			continue
		}
		operation = &refreshOperation{done: make(chan struct{}), generation: c.generation}
		c.active = operation
		break
	}
	defer func() {
		c.mu.Lock()
		operation.complete = ctx.Err() == nil && operation.generation == c.generation
		close(operation.done)
		c.active = nil
		c.mu.Unlock()
	}()
	generation := c.generation
	pending := make(map[string]entry, len(c.entries))
	for k, e := range c.entries {
		pending[k] = e
	}
	c.mu.Unlock()
	changed := false
	for key, e := range pending {
		if ctx.Err() != nil {
			break
		}
		status := c.probe(ctx, e.provider)
		// A management caller disconnect is not evidence that the engine is down.
		if ctx.Err() != nil {
			break
		}
		c.mu.Lock()
		if c.generation == generation {
			current := c.entries[key]
			changed = changed || !reflect.DeepEqual(current.status.Models, status.Models)
			current.status = status
			c.entries[key] = current
		}
		c.mu.Unlock()
	}
	if changed && c.onChange != nil {
		c.onChange()
	}
}

// Run refreshes on startup and every thirty seconds until the service context ends.
func (c *Controller) Run(ctx context.Context) {
	timer := time.NewTicker(30 * time.Second)
	defer timer.Stop()
	c.run(ctx, timer.C)
}

func (c *Controller) run(ctx context.Context, ticks <-chan time.Time) {
	c.Refresh(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			c.Refresh(ctx)
		}
	}
}

func validEndpoint(base string, blockedPorts ...int) bool {
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "/v1" || u.RawPath != "" {
		return false
	}
	ip := net.ParseIP(u.Hostname())
	if ip == nil || !ip.IsLoopback() {
		return false
	}
	port := 80
	if u.Port() != "" {
		port, err = strconv.Atoi(u.Port())
		if err != nil || port < 1 || port > 65535 {
			return false
		}
	}
	for _, blocked := range append([]int{12444, 12445}, blockedPorts...) {
		if port == blocked {
			return false
		}
	}
	return true
}

func (c *Controller) probe(ctx context.Context, p config.OpenAICompatibility) Status {
	// Credential fallback shares one deadline rather than multiplying probe latency.
	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	keys := make([]string, len(p.APIKeyEntries))
	for i, entry := range p.APIKeyEntries {
		keys[i] = strings.TrimSpace(entry.APIKey)
		if providerkeys.IsReference(keys[i]) {
			value, ok := providerkeys.Resolve(keys[i])
			if !ok {
				return Status{Provider: p.Name, Reason: "credential_unavailable", CheckedAt: time.Now().UTC(), Models: []*registry.ModelInfo{}}
			}
			keys[i] = value
		}
	}
	if len(keys) == 0 {
		keys = []string{""}
	}
	var status Status
	for _, key := range keys {
		var rejected bool
		status, rejected = c.probeCredential(probeCtx, p, key)
		if !rejected || probeCtx.Err() != nil {
			break
		}
	}
	return status
}

func (c *Controller) probeCredential(ctx context.Context, p config.OpenAICompatibility, apiKey string) (Status, bool) {
	s := Status{Provider: p.Name, Reason: "invalid_endpoint", CheckedAt: time.Now().UTC(), Models: []*registry.ModelInfo{}}
	if !validEndpoint(p.BaseURL, c.blockedPorts...) {
		return s, false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.BaseURL+"/models", nil)
	if err != nil {
		return s, false
	}
	for k, v := range p.Headers {
		req.Header.Set(k, v)
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	s.Reason = "unreachable"
	resp, err := c.client.Do(req)
	if err != nil {
		return s, false
	}
	defer func() { _ = resp.Body.Close() }()
	s.Reason = "http_status"
	if resp.StatusCode != http.StatusOK {
		return s, resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden
	}
	s.Reason = "invalid_catalog"
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil || len(body) > maxBodyBytes {
		return s, false
	}
	var catalog struct {
		Data *[]map[string]any `json:"data"`
	}
	if json.Unmarshal(body, &catalog) != nil || catalog.Data == nil || len(*catalog.Data) > 4096 {
		return s, false
	}
	seen := make(map[string]bool)
	models := []*registry.ModelInfo{}
	for _, metadata := range *catalog.Data {
		id, _ := metadata["id"].(string)
		capabilities := registry.CapabilitiesFromMetadata(metadata)
		if id == "" || len(id) > 512 || strings.IndexFunc(id, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) >= 0 {
			return s, false
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		// Engine identity requires the jointly defined producer/consumer contract in #4461.
		label := p.Name + " (unknown engine)"
		models = append(models, &registry.ModelInfo{ID: id, Object: "model", OwnedBy: label, Type: "openai-compatibility", DisplayName: id, Description: label, Capabilities: capabilities.Clone()})
	}
	s.Models = models
	if len(s.Models) == 0 {
		s.Reason = "no_models"
		return s, false
	}
	sort.Slice(s.Models, func(i, j int) bool { return s.Models[i].ID < s.Models[j].ID })
	s.Available = true
	s.Reason = "available"
	return s, false
}

func cloneModels(models []*registry.ModelInfo) []*registry.ModelInfo {
	out := make([]*registry.ModelInfo, 0, len(models))
	for _, m := range models {
		copy := *m
		if m.Capabilities != nil {
			copy.Capabilities = m.Capabilities.Clone()
		}
		out = append(out, &copy)
	}
	return out
}
