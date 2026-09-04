package channels_test

// Story E11: every built-in channel adapter runs the official SDK
// conformance kit in CI, so the channel.Adapter contract and the
// implementations cannot drift. Adapters are constructed through the same
// factory registry the host uses (E10), proving the registry path AND the
// contract in one pass.

import (
	"testing"

	"go.uber.org/zap"

	_ "github.com/soulacy/soulacy/internal/channels/discord"
	_ "github.com/soulacy/soulacy/internal/channels/googlechat"
	httpchan "github.com/soulacy/soulacy/internal/channels/http"
	_ "github.com/soulacy/soulacy/internal/channels/slack"
	_ "github.com/soulacy/soulacy/internal/channels/teams"
	_ "github.com/soulacy/soulacy/internal/channels/telegram"
	_ "github.com/soulacy/soulacy/internal/channels/webhook"
	_ "github.com/soulacy/soulacy/internal/channels/whatsapp"
	"github.com/soulacy/soulacy/sdk/channel"
	"github.com/soulacy/soulacy/sdk/channel/channeltest"
	"github.com/soulacy/soulacy/sdk/registry"
)

func registryAdapter(t *testing.T, name string, cfg map[string]any) func() channel.Adapter {
	return func() channel.Adapter {
		m := make(map[string]any, len(cfg)+1)
		for k, v := range cfg {
			m[k] = v
		}
		m["logger"] = zap.NewNop()
		a, ok, err := registry.NewChannel(name, m)
		if !ok || err != nil {
			t.Fatalf("registry.NewChannel(%q): ok=%v err=%v", name, ok, err)
		}
		return a
	}
}

func TestConformance_Telegram(t *testing.T) {
	channeltest.RunAdapterSuite(t, registryAdapter(t, "telegram",
		map[string]any{"token": "0:conformance", "agent_id": "a"}))
}

func TestConformance_Discord(t *testing.T) {
	channeltest.RunAdapterSuite(t, registryAdapter(t, "discord",
		map[string]any{"token": "Bot conformance", "agent_id": "a"}))
}

func TestConformance_Slack(t *testing.T) {
	channeltest.RunAdapterSuite(t, registryAdapter(t, "slack",
		map[string]any{"bot_token": "xoxb-conformance", "app_token": "xapp-conformance", "agent_id": "a"}))
}

func TestConformance_GoogleChat(t *testing.T) {
	channeltest.RunAdapterSuite(t, registryAdapter(t, "google_chat",
		map[string]any{"webhook_url": "https://example.invalid/soulacy"}))
}

func TestConformance_Teams(t *testing.T) {
	channeltest.RunAdapterSuite(t, registryAdapter(t, "teams",
		map[string]any{"webhook_url": "https://example.invalid/soulacy"}))
}

func TestConformance_WhatsApp(t *testing.T) {
	channeltest.RunAdapterSuite(t, registryAdapter(t, "whatsapp",
		map[string]any{"phone_number_id": "1", "access_token": "t", "verify_token": "v", "agent_id": "a"}))
}

func TestConformance_HTTP(t *testing.T) {
	channeltest.RunAdapterSuite(t, func() channel.Adapter { return httpchan.New() })
}

func TestConformance_Webhook(t *testing.T) {
	channeltest.RunAdapterSuite(t, registryAdapter(t, "webhook",
		map[string]any{"url": "https://example.invalid/soulacy"}))
}
