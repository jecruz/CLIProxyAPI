package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/localdiscovery"
)

func TestLocalCatalogManagementRequiresAuthAndRefreshIsMetadataOnly(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "discovery-test-management-key")
	var probes atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/v1/models" {
			t.Errorf("unexpected engine operation: %s %s", r.Method, r.URL.Path)
		}
		probes.Add(1)
		fmt.Fprint(w, `{"data":[{"id":"loaded-id","owned_by":"splash"}]}`)
	}))
	defer upstream.Close()
	discovery := localdiscovery.New(nil)
	discovery.Configure([]config.OpenAICompatibility{{Name: "Engine Bay", BaseURL: upstream.URL + "/v1", DiscoverLocalModels: true}})
	server := newTestServerWithOptions(t, WithLocalDiscovery(discovery))
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		path := "/v0/management/local-engines"
		if method == http.MethodPost {
			path += "/refresh"
		}
		rr := httptest.NewRecorder()
		server.engine.ServeHTTP(rr, httptest.NewRequest(method, path, nil))
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("unguarded discovery endpoint: %d", rr.Code)
		}
	}
	if probes.Load() != 0 {
		t.Fatal("unauthenticated call probed engine")
	}
	req := httptest.NewRequest(http.MethodPost, "/v0/management/local-engines/refresh", nil)
	req.Header.Set("Authorization", "Bearer discovery-test-management-key")
	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"available":true`) || !strings.Contains(rr.Body.String(), `"id":"loaded-id"`) || probes.Load() != 1 {
		t.Fatalf("refresh not bound to controller: %d %s", rr.Code, rr.Body.String())
	}
	upstream.Close()
	discovery.Refresh(context.Background())
	req = httptest.NewRequest(http.MethodGet, "/v0/management/local-engines", nil)
	req.Header.Set("Authorization", "Bearer discovery-test-management-key")
	rr = httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"available":false`) || !strings.Contains(rr.Body.String(), `"models":[]`) {
		t.Fatalf("stopped engine failed status listing: %d %s", rr.Code, rr.Body.String())
	}
	// One unavailable provider must not turn the public model listing into an error.
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	server.engine.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("unavailable engine broke client model catalog: %d", rr.Code)
	}
}
