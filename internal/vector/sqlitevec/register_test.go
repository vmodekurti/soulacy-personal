package sqlitevec

import (
	"strings"
	"testing"

	"github.com/soulacy/soulacy/sdk/registry"
)

func TestRegistryFactoryRequiresStore(t *testing.T) {
	_, ok, err := registry.NewVector("sqlite-vec", map[string]any{})
	if !ok {
		t.Fatal("sqlite-vec factory not registered")
	}
	if err == nil || !strings.Contains(err.Error(), "store") {
		t.Fatalf("missing store must error, got %v", err)
	}
	// wrong type under the key is also an error, not a panic
	if _, _, err := registry.NewVector("sqlite-vec", map[string]any{"store": 42}); err == nil {
		t.Fatal("wrong store type must error")
	}
}
