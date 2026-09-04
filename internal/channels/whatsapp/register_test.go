package whatsapp

import (
	"testing"

	"github.com/soulacy/soulacy/sdk/registry"
)

func TestRegistryFactory(t *testing.T) {
	a, ok, err := registry.NewChannel("whatsapp", map[string]any{
		"phone_number_id": "1", "access_token": "t", "verify_token": "v", "agent_id": "a",
	})
	if !ok || err != nil {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if a.ID() != "whatsapp" {
		t.Fatalf("ID = %q", a.ID())
	}
	if _, _, err := registry.NewChannel("whatsapp", map[string]any{"access_token": "t"}); err == nil {
		t.Fatal("missing phone_number_id must error")
	}
}
