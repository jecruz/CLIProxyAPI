package localdiscovery

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestLiveCatalogSwitchAndDownWithdrawsModels(t *testing.T) {
	var response atomic.Value
	response.Store(`{"data":[{"id":"model/a","owned_by":"llamacpp"}]}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/v1/models" {
			t.Errorf("unexpected probe %s %s", r.Method, r.URL.Path)
		}
		fmt.Fprint(w, response.Load().(string))
	}))
	defer server.Close()
	changes := 0
	c := New(func() { changes++ })
	c.Configure([]config.OpenAICompatibility{{Name: "Engine Bay", BaseURL: server.URL + "/v1", DiscoverLocalModels: true, Models: []config.OpenAICompatibilityModel{{Name: "stale-static"}}}})
	if got := c.Models("Engine Bay", server.URL+"/v1"); len(got) != 0 {
		t.Fatal("static/unprobed models leaked")
	}
	c.Refresh(context.Background())
	models := c.Models("Engine Bay", server.URL+"/v1")
	if len(models) != 1 || models[0].ID != "model/a" || models[0].OwnedBy != "Engine Bay (unknown engine)" {
		t.Fatalf("unexpected models: %+v", models)
	}
	models[0].ID = "mutated-copy"
	if c.Models("Engine Bay", server.URL+"/v1")[0].ID != "model/a" {
		t.Fatal("caller mutated cache")
	}
	response.Store(`{"data":[{"id":"model/b","owned_by":"splash"},{"id":"model/b","owned_by":"splash"}]}`)
	c.Refresh(context.Background())
	models = c.Models("Engine Bay", server.URL+"/v1")
	if len(models) != 1 || models[0].ID != "model/b" || models[0].OwnedBy != "Engine Bay (unknown engine)" {
		t.Fatalf("switch retained old model: %+v", models)
	}
	server.Close()
	c.Refresh(context.Background())
	if len(c.Models("Engine Bay", server.URL+"/v1")) != 0 || c.Snapshot()[0].Available {
		t.Fatal("stopped engine still routable")
	}
	if changes != 3 {
		t.Fatalf("registry not notified on all transitions: %d", changes)
	}
}

func TestProbeFailuresAreBoundedUnavailableAndNeverRedirect(t *testing.T) {
	for _, test := range []struct {
		name, body string
		code       int
	}{
		{"empty", `{"data":[]}`, 200}, {"malformed", `{"data":`, 200}, {"missing-data", `{}`, 200},
		{"bad-id", `{"data":[{"id":"bad\nname"}]}`, 200}, {"oversize", strings.Repeat("x", maxBodyBytes+1), 200},
		{"partial-bad", `{"data":[{"id":"valid"},{"id":"bad\nname"}]}`, 200},
		{"auth", `secret response must not escape`, 401}, {"redirect", "", 302},
	} {
		t.Run(test.name, func(t *testing.T) {
			var redirected atomic.Int32
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1) }))
			defer target.Close()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", target.URL)
				w.WriteHeader(test.code)
				fmt.Fprint(w, test.body)
			}))
			defer server.Close()
			c := New(nil)
			c.Configure([]config.OpenAICompatibility{{Name: "bay", BaseURL: server.URL + "/v1", DiscoverLocalModels: true}})
			c.Refresh(context.Background())
			if c.Snapshot()[0].Available || len(c.Models("bay", server.URL+"/v1")) != 0 || redirected.Load() != 0 {
				t.Fatal("unsafe probe became available or followed redirect")
			}
		})
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	c := New(nil)
	c.Configure([]config.OpenAICompatibility{{Name: "slow", BaseURL: server.URL + "/v1", DiscoverLocalModels: true}})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	c.Refresh(ctx)
	if time.Since(start) > time.Second || c.Snapshot()[0].Available {
		t.Fatal("probe ignored cancellation")
	}
}

func TestProbeHasOwnDeadlineAndDoesNotInferIdentityFromOwnership(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	c := New(nil)
	c.Configure([]config.OpenAICompatibility{{Name: "Engine Bay", BaseURL: server.URL + "/v1", DiscoverLocalModels: true}})
	start := time.Now()
	c.Refresh(context.Background())
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("probe exceeded its two-second deadline: %s", elapsed)
	}
	if c.Snapshot()[0].Available {
		t.Fatal("timed out engine became available")
	}
	for _, owner := range []string{"panthro", "vmlx-engine", "splash", "llamacpp", "unexpected"} {
		t.Run(owner, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprintf(w, `{"data":[{"id":"upstream/exact-id","owned_by":%q}]}`, owner)
			}))
			defer s.Close()
			c.Configure([]config.OpenAICompatibility{{Name: "Engine Bay", BaseURL: s.URL + "/v1", DiscoverLocalModels: true}})
			c.Refresh(context.Background())
			models := c.Models("Engine Bay", s.URL+"/v1")
			if len(models) != 1 || models[0].ID != "upstream/exact-id" || models[0].OwnedBy != "Engine Bay (unknown engine)" {
				t.Fatalf("invented ID or wrong engine identity: %+v", models)
			}
		})
	}
}

func TestExplicitLocalOnlyConfigurationAndRemoval(t *testing.T) {
	for _, endpoint := range []string{"https://example.com/v1", "http://localhost/v1", "http://127.0.0.1/v1?secret=x", "http://u:p@127.0.0.1/v1", "http://127.0.0.1/other", "http://127.0.0.1:12445/v1"} {
		c := New(nil)
		c.Configure([]config.OpenAICompatibility{{Name: "invalid", BaseURL: endpoint, DiscoverLocalModels: true}})
		c.Refresh(context.Background())
		if status := c.Snapshot(); len(status) != 1 || status[0].Reason != "invalid_endpoint" {
			t.Fatalf("accepted endpoint %s: %+v", endpoint, status)
		}
	}
	c := New(nil)
	c.Configure([]config.OpenAICompatibility{{Name: "disabled", BaseURL: "http://127.0.0.1:8318/v1", DiscoverLocalModels: true, Disabled: true}, {Name: "static", BaseURL: "https://example.com/v1"}})
	if len(c.Snapshot()) != 0 {
		t.Fatal("opt-out providers were probed")
	}
}

func TestReconfigureRejectsInflightOldEndpointAndTimerStops(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		fmt.Fprint(w, `{"data":[{"id":"old-model","owned_by":"splash"}]}`)
	}))
	defer server.Close()
	c := New(nil)
	c.Configure([]config.OpenAICompatibility{{Name: "bay", BaseURL: server.URL + "/v1", DiscoverLocalModels: true}})
	done := make(chan struct{})
	go func() { c.Refresh(context.Background()); close(done) }()
	<-started
	c.Configure(nil)
	close(release)
	<-done
	if len(c.Snapshot()) != 0 || len(c.Models("bay", server.URL+"/v1")) != 0 {
		t.Fatal("stale response restored removed provider")
	}
	var calls atomic.Int32
	fast := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); fmt.Fprint(w, `{"data":[{"id":"live"}]}`) }))
	defer fast.Close()
	c.Configure([]config.OpenAICompatibility{{Name: "bay", BaseURL: fast.URL + "/v1", DiscoverLocalModels: true}})
	ctx, cancel := context.WithCancel(context.Background())
	ticks := make(chan time.Time)
	finished := make(chan struct{})
	go func() { c.run(ctx, ticks); close(finished) }()
	deadline := time.After(time.Second)
	for calls.Load() < 1 {
		select {
		case <-deadline:
			t.Fatal("no initial refresh")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	ticks <- time.Now()
	deadline = time.After(time.Second)
	for calls.Load() < 2 {
		select {
		case <-deadline:
			t.Fatal("timer did not refresh")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	cancel()
	<-finished
	if calls.Load() < 2 {
		t.Fatal("timer did not refresh")
	}
}

func TestConcurrentRefreshWaitsAndCanceledWaiterDoesNotCancelLeader(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		close(started)
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		fmt.Fprint(w, `{"data":[{"id":"fresh"}]}`)
	}))
	defer server.Close()
	c := New(nil)
	c.Configure([]config.OpenAICompatibility{{Name: "bay", BaseURL: server.URL + "/v1", DiscoverLocalModels: true}})
	leader := make(chan struct{})
	go func() { c.Refresh(context.Background()); close(leader) }()
	<-started
	waiter := make(chan struct{})
	go func() { c.Refresh(context.Background()); close(waiter) }()
	select {
	case <-waiter:
		t.Fatal("concurrent refresh returned before active probe completed")
	case <-time.After(30 * time.Millisecond):
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c.Refresh(ctx)
	close(release)
	<-leader
	<-waiter
	if calls.Load() != 1 || !c.Snapshot()[0].Available {
		t.Fatal("waiters duplicated or canceled the active probe")
	}
}

func TestCanceledRefreshPreservesHealthyCatalog(t *testing.T) {
	var block atomic.Bool
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if block.Load() {
			close(started)
			<-r.Context().Done()
			return
		}
		fmt.Fprint(w, `{"data":[{"id":"healthy"}]}`)
	}))
	defer server.Close()
	changes := 0
	c := New(func() { changes++ })
	c.Configure([]config.OpenAICompatibility{{Name: "bay", BaseURL: server.URL + "/v1", DiscoverLocalModels: true}})
	c.Refresh(context.Background())
	before := c.Snapshot()[0]
	block.Store(true)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { c.Refresh(ctx); close(done) }()
	<-started
	cancel()
	<-done
	after := c.Snapshot()[0]
	if !after.Available || len(after.Models) != 1 || after.Models[0].ID != "healthy" || !after.CheckedAt.Equal(before.CheckedAt) || changes != 1 {
		t.Fatal("caller cancellation withdrew or replaced the healthy catalog")
	}
}

func TestProbeFallsBackAcrossCredentialsWithinOneDeadline(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer fixture-valid" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, `{"data":[{"id":"authenticated"}]}`)
	}))
	defer server.Close()
	c := New(nil)
	p := config.OpenAICompatibility{Name: "bay", BaseURL: server.URL + "/v1", DiscoverLocalModels: true, APIKeyEntries: []config.OpenAICompatibilityAPIKey{{APIKey: "fixture-expired"}, {APIKey: "fixture-valid"}}}
	c.Configure([]config.OpenAICompatibility{p})
	c.Refresh(context.Background())
	if calls.Load() != 2 || !c.Snapshot()[0].Available {
		t.Fatal("rejected first credential hid a usable provider")
	}
	var slowCalls atomic.Int32
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slowCalls.Add(1)
		select {
		case <-time.After(1100 * time.Millisecond):
			w.WriteHeader(http.StatusUnauthorized)
		case <-r.Context().Done():
		}
	}))
	defer slow.Close()
	p.BaseURL = slow.URL + "/v1"
	p.APIKeyEntries = append(p.APIKeyEntries, config.OpenAICompatibilityAPIKey{APIKey: "fixture-third"})
	c.Configure([]config.OpenAICompatibility{p})
	start := time.Now()
	c.Refresh(context.Background())
	if time.Since(start) > 2700*time.Millisecond || slowCalls.Load() != 2 || c.Snapshot()[0].Available {
		t.Fatal("credential fallback exceeded a single two-second deadline")
	}
}

func TestWaitingRefreshReprobesAfterReconfigureOrLeaderCancellation(t *testing.T) {
	for _, cancelLeader := range []bool{false, true} {
		t.Run(fmt.Sprintf("cancel=%t", cancelLeader), func(t *testing.T) {
			started, release := make(chan struct{}), make(chan struct{})
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if calls.Add(1) == 1 {
					close(started)
					select {
					case <-release:
					case <-r.Context().Done():
						return
					}
				}
				fmt.Fprint(w, `{"data":[{"id":"current"}]}`)
			}))
			defer server.Close()
			c := New(nil)
			p := config.OpenAICompatibility{Name: "bay", BaseURL: server.URL + "/v1", DiscoverLocalModels: true}
			c.Configure([]config.OpenAICompatibility{p})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			leader := make(chan struct{})
			go func() { c.Refresh(ctx); close(leader) }()
			<-started
			waiter := make(chan struct{})
			go func() { c.Refresh(context.Background()); close(waiter) }()
			select {
			case <-waiter:
				t.Fatal("waiter did not wait")
			case <-time.After(30 * time.Millisecond):
			}
			if cancelLeader {
				cancel()
			} else {
				p.Headers = map[string]string{"X-Fixture-Generation": "new"}
				c.Configure([]config.OpenAICompatibility{p})
				close(release)
			}
			<-leader
			<-waiter
			if calls.Load() != 2 || !c.Snapshot()[0].Available || c.Snapshot()[0].Models[0].ID != "current" {
				t.Fatal("waiter returned without probing current generation after leader failed")
			}
		})
	}
}

func TestSelfReferencePortsAreComparedNumerically(t *testing.T) {
	for _, endpoint := range []string{"http://127.0.0.1:012444/v1", "http://127.0.0.1:012445/v1", "http://127.0.0.1:0/v1", "http://127.0.0.1:65536/v1"} {
		if validEndpoint(endpoint) {
			t.Errorf("accepted self-reference or invalid numeric port: %s", endpoint)
		}
	}
}
