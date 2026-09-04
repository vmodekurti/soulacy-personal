package slack

import (
	"testing"

	"github.com/soulacy/soulacy/sdk/registry"
)

func TestRegistryFactory(t *testing.T) {
	a, ok, err := registry.NewChannel("slack", map[string]any{
		"bot_token": "xoxb-1", "app_token": "xapp-1", "agent_id": "a",
	})
	if !ok || err != nil {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if a.ID() != "slack" {
		t.Fatalf("ID = %q", a.ID())
	}
	if _, _, err := registry.NewChannel("slack", map[string]any{"bot_token": "xoxb-1"}); err == nil {
		t.Fatal("missing app_token must error")
	}
}
