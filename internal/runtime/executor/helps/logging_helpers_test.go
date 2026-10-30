package helps

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
)

// TestProviderKeychainRequestLogging removes all credential header fragments.
func TestProviderKeychainRequestLogging(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	headers := http.Header{"Authorization": {"Bearer sentinel-fixture-only"}}
	ctx := context.WithValue(context.Background(), "gin", c)
	cfg := &config.Config{}
	cfg.RequestLog = true
	RecordAPIRequest(ctx, cfg, UpstreamRequestLog{
		Headers: headers, AuthType: "api_key", AuthValue: "keychain://caiss/providers/FIXTURE",
	})
	attempts := getAttempts(c)
	if len(attempts) != 1 || strings.Contains(attempts[0].request, "sentinel") || strings.Contains(attempts[0].request, "only") {
		t.Fatal("request logging retained credential fragments")
	}
	if headers.Get("Authorization") != "Bearer sentinel-fixture-only" {
		t.Fatal("logging changed the actual outbound credential")
	}
}

func TestRecordAPIResponseMetadataStoresHeadersWhenRequestLogDisabled(t *testing.T) {
	ctx := logging.WithResponseHeadersHolder(context.Background())
	headers := http.Header{}
	headers.Add("X-Upstream-Request-Id", "upstream-req-1")

	RecordAPIResponseMetadata(ctx, &config.Config{}, http.StatusOK, headers)
	headers.Set("X-Upstream-Request-Id", "mutated")

	got := logging.GetResponseHeaders(ctx)
	if got.Get("X-Upstream-Request-Id") != "upstream-req-1" {
		t.Fatalf("response header = %q, want %q", got.Get("X-Upstream-Request-Id"), "upstream-req-1")
	}
}
