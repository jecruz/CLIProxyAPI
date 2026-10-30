// Package providerkeys holds per-launch provider credentials without changing serialized config.
package providerkeys

import (
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"strings"
	"sync/atomic"
)

// EnvironmentName is consumed before plugin or service startup and never inherited onward.
const EnvironmentName = "LLMDYNAMIX_PROVIDER_KEYS_JSON"

var referencePattern = regexp.MustCompile(`^keychain://caiss/providers/[A-Za-z_][A-Za-z0-9_.-]{0,127}$`)
var current atomic.Value // Immutable map[string]string for this launch.

// IsReference recognizes even malformed/case-mismatched Keychain URIs for fail-closed handling.
func IsReference(value string) bool {
	return strings.HasPrefix(strings.ToLower(value), "keychain:")
}

// ConsumeEnvironment unsets the transport immediately and rejects the entire map on invalid input.
func ConsumeEnvironment() error {
	raw := os.Getenv(EnvironmentName)
	errUnset := os.Unsetenv(EnvironmentName)
	current.Store(map[string]string{})
	if errUnset != nil {
		return errors.New("provider credential transport unavailable")
	}
	if raw == "" {
		return nil
	}
	var next map[string]string
	if json.Unmarshal([]byte(raw), &next) != nil || next == nil {
		return errors.New("invalid provider credential map")
	}
	for reference, value := range next {
		if !referencePattern.MatchString(reference) || strings.TrimSpace(value) == "" || strings.ContainsAny(value, "\r\n\x00") {
			return errors.New("invalid provider credential entry")
		}
	}
	current.Store(next)
	return nil
}

// Resolve retrieves only a strictly valid reference from the private startup snapshot.
func Resolve(reference string) (string, bool) {
	if !referencePattern.MatchString(reference) {
		return "", false
	}
	values, _ := current.Load().(map[string]string)
	value, ok := values[reference]
	return value, ok
}
