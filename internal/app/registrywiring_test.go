package app

import (
	"testing"

	"go.uber.org/zap"

	// The buildChannel tests resolve through the SDK registry, so the
	// telegram driver's init() self-registration must be linked in (the
	// binary gets this from cmd/soulacy/builtins_gen.go).
	_ "github.com/soulacy/soulacy/internal/channels/telegram"

	"github.com/soulacy/soulacy/internal/channels"
	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/runtime"
	"github.com/soulacy/soulacy/pkg/agent"
)

func TestProviderCfgMapOmitsZeroValues(t *testing.T) {
	pt := false
	m := providerCfgMap(config.ProviderConfig{
		BaseURL:           "https://x",
		APIKey:            "k",
		ThinkingBudget:    100,
		ParallelToolCalls: &pt,
	})
	if m["base_url"] != "https://x" || m["api_key"] != "k" || m["thinking_budget"] != 100 {
		t.Fatalf("map = %v", m)
	}
	if _, present := m["model"]; present {
		t.Fatal("zero-value model must be omitted so factory defaults apply")
	}
	if _, present := m["prompt_caching"]; present {
		t.Fatal("false prompt_caching must be omitted")
	}
	if got, ok := m["parallel_tool_calls"].(*bool); !ok || *got != false {
		t.Fatalf("parallel_tool_calls = %v", m["parallel_tool_calls"])
	}
}

func TestBuildChannelInjectsIDAndLogger(t *testing.T) {
	a, err := buildChannel("telegram", "telegram-x", map[string]any{
		"token": "123:abc", "agent_id": "a",
	}, zap.NewNop())
	if err != nil {
		t.Fatalf("buildChannel: %v", err)
	}
	if a.ID() != "telegram-x" {
		t.Fatalf("ID = %q", a.ID())
	}
	// source map must not be mutated
	src := map[string]any{"token": "123:abc"}
	if _, err := buildChannel("telegram", "tg2", src, zap.NewNop()); err != nil {
		t.Fatalf("buildChannel: %v", err)
	}
	if _, mutated := src["id"]; mutated {
		t.Fatal("buildChannel must not mutate the caller's config map")
	}
}

func TestBuildChannelUnknownName(t *testing.T) {
	if _, err := buildChannel("matrix", "", map[string]any{}, zap.NewNop()); err == nil {
		t.Fatal("unknown channel name must error (host falls back / warns)")
	}
}

func TestRegisterChannelsAllowsTelegramOutboundOnlyWithoutAgentBinding(t *testing.T) {
	app := &App{log: zap.NewNop()}
	reg := channels.NewRegistry(1)
	app.registerChannels(map[string]map[string]any{
		"telegram": {
			"enabled":       true,
			"token":         "123:abc",
			"outbound_only": true,
		},
	}, reg, runtime.NewLoader(nil), config.Paths{})

	st, ok := reg.Statuses()["telegram"]
	if !ok {
		t.Fatal("telegram outbound-only adapter was not registered")
	}
	if st.Detail != "outbound-only" {
		t.Fatalf("status detail = %q, want outbound-only", st.Detail)
	}
}

func TestRegisterChannelsTelegramDefaultSenderAndAgentMapping(t *testing.T) {
	app := &App{log: zap.NewNop()}
	reg := channels.NewRegistry(1)
	app.registerChannels(map[string]map[string]any{
		"telegram": {
			"enabled":       true,
			"token":         "123:default",
			"outbound_only": true,
			"bots": []any{
				map[string]any{
					"token":    "123:weather",
					"agent_id": "weather-agent",
				},
			},
		},
	}, reg, runtime.NewLoader(nil), config.Paths{})

	statuses := reg.Statuses()
	if _, ok := statuses["telegram"]; !ok {
		t.Fatal("default telegram sender was not registered")
	}
	if _, ok := statuses["telegram-weather-agent"]; !ok {
		t.Fatal("agent-specific telegram mapping was not registered with a distinct adapter ID")
	}
}

func TestRegisterChannelsTelegramBotInheritsPrivilegedConsent(t *testing.T) {
	dir := t.TempDir()
	loader := runtime.NewLoader([]string{dir})
	if err := loader.Upsert(dir, &agent.Definition{
		ID:          "librarian",
		Trigger:     agent.TriggerCron,
		Enabled:     true,
		SystemTools: true,
		Schedule:    &agent.Schedule{Cron: "0 7 * * *"},
	}); err != nil {
		t.Fatalf("upsert privileged agent: %v", err)
	}

	app := &App{log: zap.NewNop()}
	reg := channels.NewRegistry(1)
	app.registerChannels(map[string]map[string]any{
		"telegram": {
			"enabled":                    true,
			"token":                      "123:default",
			"outbound_only":              true,
			"accept_privileged_exposure": true,
			"bots": []any{
				map[string]any{
					"token":    "123:librarian",
					"agent_id": "librarian",
				},
			},
		},
	}, reg, loader, config.Paths{})

	if _, ok := reg.Statuses()["telegram-librarian"]; !ok {
		t.Fatal("privileged bot should register when consent is inherited from the telegram channel")
	}
}
