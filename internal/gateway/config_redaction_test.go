// config_redaction_test.go — Story 1 (auth/secret hardening) focused tests.
//
// Verifies that GET /api/v1/config never returns channel secrets (bot tokens,
// webhook/verify tokens, generic *_secret keys) unredacted, and that the
// redaction helpers behave correctly for known specs, bots lists, and
// unknown channel types.
package gateway

import (
	"net/http"
	"strings"
	"testing"
)

// ── safeChannelsView: known channel spec secrets redacted ─────────────────────

func TestSafeChannelsView_TelegramTokenRedacted(t *testing.T) {
	t.Parallel() // TEST-4: pure redaction logic, no shared state.
	out := safeChannelsView(map[string]map[string]any{
		"telegram": {
			"enabled":  true,
			"token":    "123456:ABC-real-telegram-token",
			"agent_id": "helper",
		},
	})
	tg := out["telegram"]
	if tg["token"] != "***" {
		t.Errorf("telegram token = %v, want ***", tg["token"])
	}
	if tg["agent_id"] != "helper" {
		t.Errorf("agent_id should pass through, got %v", tg["agent_id"])
	}
	if tg["enabled"] != true {
		t.Errorf("enabled should pass through, got %v", tg["enabled"])
	}
}

func TestSafeChannelsView_SlackAndWhatsAppSecretsRedacted(t *testing.T) {
	t.Parallel() // TEST-4: pure redaction logic, no shared state.
	out := safeChannelsView(map[string]map[string]any{
		"slack": {
			"bot_token": "xoxb-real",
			"app_token": "xapp-real",
			"agent_id":  "a1",
		},
		"whatsapp": {
			"access_token":    "EAAG-real",
			"verify_token":    "verify-real",
			"phone_number_id": "1234567890",
		},
	})
	for ch, keys := range map[string][]string{
		"slack":    {"bot_token", "app_token"},
		"whatsapp": {"access_token", "verify_token"},
	} {
		for _, k := range keys {
			if out[ch][k] != "***" {
				t.Errorf("%s.%s = %v, want ***", ch, k, out[ch][k])
			}
		}
	}
	if out["whatsapp"]["phone_number_id"] != "1234567890" {
		t.Errorf("phone_number_id should pass through, got %v", out["whatsapp"]["phone_number_id"])
	}
}

// ── safeChannelsView: empty secrets stay empty (not masked as set) ────────────

func TestSafeChannelsView_EmptySecretNotMasked(t *testing.T) {
	t.Parallel() // TEST-4: pure redaction logic, no shared state.
	out := safeChannelsView(map[string]map[string]any{
		"telegram": {"token": "", "agent_id": "a"},
	})
	if out["telegram"]["token"] == "***" {
		t.Error("empty token must not be masked as *** (GUI would think a key is set)")
	}
}

// ── safeChannelsView: bots list secrets redacted ──────────────────────────────

func TestSafeChannelsView_BotListSecretsRedacted(t *testing.T) {
	t.Parallel() // TEST-4: pure redaction logic, no shared state.
	out := safeChannelsView(map[string]map[string]any{
		"telegram": {
			"bots": []any{
				map[string]any{"bot_name": "primary", "token": "111:real-a", "agent_id": "a"},
				map[string]any{"bot_name": "backup", "token": "222:real-b", "agent_id": "b"},
			},
		},
	})
	bots, ok := out["telegram"]["bots"].([]map[string]any)
	if !ok {
		t.Fatalf("bots is %T, want []map[string]any", out["telegram"]["bots"])
	}
	if len(bots) != 2 {
		t.Fatalf("got %d bots, want 2", len(bots))
	}
	for i, bot := range bots {
		if bot["token"] != "***" {
			t.Errorf("bot[%d].token = %v, want ***", i, bot["token"])
		}
		if bot["bot_name"] == "" || bot["bot_name"] == "***" {
			t.Errorf("bot[%d].bot_name should pass through, got %v", i, bot["bot_name"])
		}
	}
}

// ── safeChannelsView: unknown channel types use generic heuristic ─────────────

func TestSafeChannelsView_UnknownChannelGenericRedaction(t *testing.T) {
	t.Parallel() // TEST-4: pure redaction logic, no shared state.
	out := safeChannelsView(map[string]map[string]any{
		"customhook": {
			"webhook_secret": "shhh-real",
			"api_key":        "key-real",
			"password":       "pw-real",
			"endpoint":       "https://example.com/hook",
		},
	})
	ch := out["customhook"]
	for _, k := range []string{"webhook_secret", "api_key", "password"} {
		if ch[k] != "***" {
			t.Errorf("customhook.%s = %v, want *** (generic heuristic)", k, ch[k])
		}
	}
	if ch["endpoint"] != "https://example.com/hook" {
		t.Errorf("endpoint should pass through, got %v", ch["endpoint"])
	}
}

// ── safeChannelsView: does not mutate the live config map ─────────────────────

func TestSafeChannelsView_DoesNotMutateSource(t *testing.T) {
	t.Parallel() // TEST-4: pure redaction logic, no shared state.
	src := map[string]map[string]any{
		"telegram": {"token": "111:real"},
	}
	_ = safeChannelsView(src)
	if src["telegram"]["token"] != "111:real" {
		t.Errorf("source config was mutated: token = %v", src["telegram"]["token"])
	}
}

// ── isSecretChannelKey ─────────────────────────────────────────────────────────

func TestIsSecretChannelKey_SpecAuthoritative(t *testing.T) {
	t.Parallel() // TEST-4: pure redaction logic, no shared state.
	spec := channelSpecByID("telegram")
	if spec == nil {
		t.Fatal("telegram spec missing")
	}
	if !isSecretChannelKey(spec, "token") {
		t.Error("telegram token must be secret")
	}
	if isSecretChannelKey(spec, "bot_name") {
		t.Error("bot_name must not be secret")
	}
	if isSecretChannelKey(spec, "agent_id") {
		t.Error("agent_id must not be secret")
	}
}

func TestIsSecretChannelKey_GenericFallback(t *testing.T) {
	t.Parallel() // TEST-4: pure redaction logic, no shared state.
	cases := map[string]bool{
		"webhook_secret": true,
		"access_token":   true,
		"api_key":        true,
		"apikey":         true,
		"password":       true,
		"credentials":    true,
		"endpoint":       false,
		"agent_id":       false,
		"keep_alive":     false,
	}
	for key, want := range cases {
		if got := isSecretChannelKey(nil, key); got != want {
			t.Errorf("isSecretChannelKey(nil, %q) = %v, want %v", key, got, want)
		}
	}
}

// ── GET /api/v1/config end-to-end: channel secrets never reach the wire ───────

func TestGetConfig_ChannelSecretsRedactedEndToEnd(t *testing.T) {
	s := newTestGateway(t, "secret")
	s.cfg.Channels = map[string]map[string]any{
		"telegram": {
			"token":    "123456:REAL-LEAKED-TOKEN",
			"agent_id": "helper",
			"bots": []any{
				map[string]any{"bot_name": "b1", "token": "999:REAL-BOT-TOKEN"},
			},
		},
		"customhook": {
			"webhook_secret": "REAL-WEBHOOK-SECRET",
		},
	}

	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/config", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("get config status = %d body=%v", status, body)
	}

	chans, ok := body["channels"].(map[string]any)
	if !ok {
		t.Fatalf("channels is %T, want map", body["channels"])
	}
	tg, _ := chans["telegram"].(map[string]any)
	if tg == nil {
		t.Fatal("telegram channel missing from config view")
	}
	if tg["token"] != "***" {
		t.Errorf("telegram.token leaked: %v", tg["token"])
	}
	bots, _ := tg["bots"].([]any)
	if len(bots) != 1 {
		t.Fatalf("expected 1 bot, got %v", tg["bots"])
	}
	bot, _ := bots[0].(map[string]any)
	if bot["token"] != "***" {
		t.Errorf("bot token leaked: %v", bot["token"])
	}
	custom, _ := chans["customhook"].(map[string]any)
	if custom == nil || custom["webhook_secret"] != "***" {
		t.Errorf("customhook.webhook_secret leaked: %v", custom)
	}

	// Belt and braces: no raw secret substring anywhere in the response.
	rawStatus, rawBody := gatewayRaw(t, s, http.MethodGet, "/api/v1/config", "secret", "")
	if rawStatus != http.StatusOK {
		t.Fatalf("raw get config status = %d", rawStatus)
	}
	for _, leak := range []string{"REAL-LEAKED-TOKEN", "REAL-BOT-TOKEN", "REAL-WEBHOOK-SECRET"} {
		if strings.Contains(rawBody, leak) {
			t.Errorf("secret %q found in raw /api/v1/config response", leak)
		}
	}
}

// ── PATCH /api/v1/config response also uses the redacted view ─────────────────

func TestPatchConfig_ResponseChannelSecretsRedacted(t *testing.T) {
	cfgPath := t.TempDir() + "/config.yaml"
	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)
	s.cfg.Channels = map[string]map[string]any{
		"discord": {"token": "REAL-DISCORD-TOKEN", "agent_id": "a"},
	}

	status, body := gatewayJSON(t, s, http.MethodPatch, "/api/v1/config", "secret",
		`{"log":{"level":"debug"}}`)
	if status != http.StatusOK {
		t.Fatalf("patch config status = %d body=%v", status, body)
	}
	cfgView, _ := body["config"].(map[string]any)
	if cfgView == nil {
		t.Fatal("patch response missing config view")
	}
	chans, _ := cfgView["channels"].(map[string]any)
	dc, _ := chans["discord"].(map[string]any)
	if dc == nil || dc["token"] != "***" {
		t.Errorf("discord token leaked in PATCH response: %v", dc)
	}
}

// ── Auth failures: API returns 401 with a JSON error (drives the GUI state) ───

func TestGetConfig_Unauthenticated401JSONError(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/config", "wrong-key", "")
	if status != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", status)
	}
	if body == nil || body["error"] == nil {
		t.Fatalf("expected JSON error body for 401, got %v", body)
	}
}
