package auth

import (
	"context"
	"testing"
)

// TestProviderRuntimeMetadataAndPersistence keeps key material out of account identities and stores.
func TestProviderRuntimeMetadataAndPersistence(t *testing.T) {
	reference := "keychain://caiss/providers/FIXTURE"
	store := &countingStore{}
	manager := NewManager(store, nil, nil)
	var index string
	for _, value := range []string{"sentinel-one-fixture-only", "sentinel-two-fixture-only"} {
		a := &Auth{ID: "fixture", Provider: "fixture", Metadata: map[string]any{"disable_cooling": true}, Attributes: map[string]string{
			"api_key": value, "compat_name": "fixture", "runtime_only": "true", "keychain_reference": reference,
		}}
		kind, label := a.AccountInfo()
		if kind != "api_key" || label != reference {
			t.Fatal("account metadata must identify the reference, never the resolved value")
		}
		if next := a.EnsureIndex(); index != "" && index != next {
			t.Fatal("rotation must not expose a value-derived identity")
		} else {
			index = next
		}
		if _, err := manager.Register(WithSkipPersist(context.Background()), a); err != nil {
			t.Fatal("registration failed")
		}
		if _, err := manager.Update(context.Background(), a); err != nil {
			t.Fatal("update failed")
		}
	}
	if store.saveCount.Load() != 0 {
		t.Fatal("runtime-only credentials reached the persistence store")
	}
}
