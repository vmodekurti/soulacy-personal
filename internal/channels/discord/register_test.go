package discord

import (
	"testing"

	"github.com/soulacy/soulacy/sdk/registry"
)

func TestRegistryFactory(t *testing.T) {
	a, ok, err := registry.NewChannel("discord", map[string]any{
		"id": "discord-x", "token": "Bot abc", "agent_id": "a", "guild_id": "g",
	})
	if !ok || err != nil {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if a.ID() != "discord-x" {
		t.Fatalf("ID = %q", a.ID())
	}
	if _, _, err := registry.NewChannel("discord", map[string]any{}); err == nil {
		t.Fatal("missing token must error")
	}
}

func TestDiscordTokenNormalization(t *testing.T) {
	for _, tc := range []struct {
		name    string
		input   string
		gateway string
		rest    string
	}{
		{name: "raw", input: "abc.def", gateway: "abc.def", rest: "Bot abc.def"},
		{name: "bot prefix", input: "Bot abc.def", gateway: "abc.def", rest: "Bot abc.def"},
		{name: "lowercase prefix", input: "bot abc.def", gateway: "abc.def", rest: "Bot abc.def"},
		{name: "trimmed", input: "  Bot abc.def  ", gateway: "abc.def", rest: "Bot abc.def"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := discordGatewayToken(tc.input); got != tc.gateway {
				t.Fatalf("discordGatewayToken() = %q, want %q", got, tc.gateway)
			}
			if got := discordRESTAuth(tc.input); got != tc.rest {
				t.Fatalf("discordRESTAuth() = %q, want %q", got, tc.rest)
			}
		})
	}
}
