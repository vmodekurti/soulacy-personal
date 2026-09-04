package telegram

import (
	"context"
	"testing"

	"github.com/soulacy/soulacy/pkg/message"
	"github.com/soulacy/soulacy/sdk/registry"
)

func TestRegistryFactoryBuildsAdapter(t *testing.T) {
	a, ok, err := registry.NewChannel("telegram", map[string]any{
		"id":       "telegram-second",
		"token":    "123:abc",
		"agent_id": "assistant",
	})
	if !ok {
		t.Fatal("telegram factory not registered")
	}
	if err != nil {
		t.Fatalf("factory error: %v", err)
	}
	if got := a.ID(); got != "telegram-second" {
		t.Fatalf("adapter ID = %q", got)
	}
}

func TestRegistryFactoryDefaultID(t *testing.T) {
	a, ok, err := registry.NewChannel("telegram", map[string]any{"token": "123:abc"})
	if !ok || err != nil {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if got := a.ID(); got != "telegram" {
		t.Fatalf("default adapter ID = %q", got)
	}
}

func TestRegistryFactoryTokenOnlyIsOutboundOnly(t *testing.T) {
	a, ok, err := registry.NewChannel("telegram", map[string]any{"token": "123:abc"})
	if !ok || err != nil {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	msgs := make(chan message.Message, 1)
	if err := a.Start(context.Background(), msgs); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = a.Stop() })
	st := a.Status()
	if !st.Connected {
		t.Fatal("token-only adapter should report connected for outbound sends")
	}
	if st.Detail != "outbound-only" {
		t.Fatalf("status detail = %q, want outbound-only", st.Detail)
	}
}

func TestRegistryFactoryRequiresToken(t *testing.T) {
	_, ok, err := registry.NewChannel("telegram", map[string]any{"agent_id": "x"})
	if !ok {
		t.Fatal("factory should be registered")
	}
	if err == nil {
		t.Fatal("missing token must error")
	}
}
