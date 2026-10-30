package providerkeys

import (
	"os"
	"strings"
	"testing"
)

// TestProviderTransportConsumesAndClearsInvalidGenerations never retains or logs rejected values.
func TestProviderTransportConsumesAndClearsInvalidGenerations(t *testing.T) {
	reference := "keychain://caiss/providers/FIXTURE"
	for _, raw := range []string{`{"keychain://caiss/providers/FIXTURE":"fixture-only"}`,
		`{"keychain://wrong/NAME":"secret-fixture"}`, `{"keychain://caiss/providers/FIXTURE":""}`,
		`{"keychain://caiss/providers/FIXTURE":"bad\nheader"}`, `{"keychain://caiss/providers/FIXTURE":123}`, `{`, `null`} {
		t.Setenv(EnvironmentName, raw)
		err := ConsumeEnvironment()
		if _, present := os.LookupEnv(EnvironmentName); present {
			t.Fatal("transport was not consumed")
		}
		value, available := Resolve(reference)
		if strings.Contains(raw, "fixture-only") {
			if err != nil || !available || value != "fixture-only" {
				t.Fatal("valid transport was not resolved")
			}
		} else if err == nil || available || strings.Contains(err.Error(), "secret-fixture") {
			t.Fatal("invalid transport was accepted or disclosed")
		}
	}
	t.Setenv(EnvironmentName, "")
	if ConsumeEnvironment() != nil {
		t.Fatal("absent transport should permit literal providers")
	}
}
