package memory

import (
	"testing"

	"github.com/soulacy/soulacy/sdk/registry"
)

func TestRegistryFactory(t *testing.T) {
	q, ok, err := registry.NewQueue("memory", nil)
	if !ok || err != nil {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if q == nil {
		t.Fatal("nil backend")
	}
	q.(*Backend).Close()
}
