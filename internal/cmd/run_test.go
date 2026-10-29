package cmd

import (
	"testing"
	"time"
)

func TestLocalManagementKeepAliveTimeoutAllowsDesktopTimerJitter(t *testing.T) {
	if localManagementKeepAliveTimeout < 24*time.Hour {
		t.Fatalf("local management keep-alive timeout = %s, want at least 24h", localManagementKeepAliveTimeout)
	}
}
