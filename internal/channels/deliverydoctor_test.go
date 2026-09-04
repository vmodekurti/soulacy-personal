package channels

import (
	"errors"
	"strings"
	"testing"
)

func TestClassifyDeliveryError(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want DeliveryCategory
	}{
		// Telegram (from "telegram: send: API returned status %d: %s")
		{"telegram-chat-not-found", "telegram: send: API returned status 400: Bad Request: chat not found", DeliveryInvalidDest},
		{"telegram-unauthorized", "telegram: send: API returned status 401: Unauthorized", DeliveryBadToken},
		{"telegram-bot-blocked", "telegram: send: API returned status 403: Forbidden: bot was blocked by the user", DeliveryForbidden},
		// Slack (raw error codes)
		{"slack-not-in-channel", "slack: send: not_in_channel", DeliveryBotNotInvited},
		{"slack-missing-scope", "slack: send: missing_scope", DeliveryMissingScope},
		{"slack-channel-not-found", "slack: send: channel_not_found", DeliveryInvalidDest},
		{"slack-invalid-auth", "slack: send: invalid_auth", DeliveryBadToken},
		// Discord (from discordErrorDetail: "<message> (code <n>)")
		{"discord-unknown-channel", "discord: send: API returned status 404: Unknown Channel (code 10003)", DeliveryInvalidDest},
		{"discord-unauthorized", "discord: send: API returned status 401: 401: Unauthorized", DeliveryBadToken},
		{"discord-missing-access", "discord: send: API returned status 403: Missing Access (code 50001)", DeliveryForbidden},
		// Webhook-style destinations.
		{"teams-webhook-not-found", "teams: send: API returned status 404: Not Found", DeliveryInvalidDest},
		{"google-chat-webhook-forbidden", "google_chat: send: API returned status 403: Forbidden", DeliveryForbidden},
		{"provider-rate-limit", "slack: send: rate_limited", DeliveryRateLimited},
		// Transport
		{"network-timeout", "telegram: send: context deadline exceeded", DeliveryNetwork},
		{"network-refused", "slack: send: dial tcp: connection refused", DeliveryNetwork},
		// Unknown
		{"weird", "slack: send: something_totally_new", DeliveryUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyDeliveryError(tc.raw)
			if got.Category != tc.want {
				t.Errorf("ClassifyDeliveryError(%q) category = %q, want %q", tc.raw, got.Category, tc.want)
			}
			if got.Reason == "" {
				t.Errorf("empty Reason for %q", tc.raw)
			}
			if got.OK {
				t.Errorf("classified error should never be OK for %q", tc.raw)
			}
		})
	}
}

func TestDiagnoseDeliveryPreconditions(t *testing.T) {
	// No destination.
	if d := DiagnoseDelivery("telegram", "", true, true, nil); d.Category != DeliveryMissingTo {
		t.Errorf("missing to: got %q", d.Category)
	}
	// Adapter not registered.
	if d := DiagnoseDelivery("telegram", "123", false, false, nil); d.Category != DeliveryAdapterDown {
		t.Errorf("adapter down: got %q", d.Category)
	}
	// Registered but disconnected.
	if d := DiagnoseDelivery("telegram", "123", true, false, nil); d.Category != DeliveryAdapterDisconnect {
		t.Errorf("disconnected: got %q", d.Category)
	}
	// All good.
	d := DiagnoseDelivery("telegram", "123", true, true, nil)
	if !d.OK || d.Category != DeliveryOK {
		t.Errorf("ok: got OK=%v cat=%q", d.OK, d.Category)
	}
	// Send error is classified and detail preserved.
	d = DiagnoseDelivery("slack", "C123", true, true, errors.New("slack: send: missing_scope"))
	if d.Category != DeliveryMissingScope {
		t.Errorf("send err: got %q", d.Category)
	}
	if d.Detail == "" {
		t.Errorf("expected raw detail to be preserved")
	}
}

func TestDiagnoseDeliveryAdapterSpecificFixes(t *testing.T) {
	cases := []struct {
		name    string
		adapter string
		raw     string
		want    string
	}{
		{
			name:    "telegram-chat-id-guidance",
			adapter: "telegram-research",
			raw:     "telegram: send: API returned status 400: Bad Request: chat not found",
			want:    "-100",
		},
		{
			name:    "slack-channel-id-guidance",
			adapter: "slack-research",
			raw:     "slack: send: channel_not_found",
			want:    "Slack channel ID",
		},
		{
			name:    "webhook-regenerate-guidance",
			adapter: "teams",
			raw:     "teams: send: API returned status 404: Not Found",
			want:    "fresh incoming webhook",
		},
		// Story 4 sweep — email/SMTP-specific classifications so operators
		// stop seeing raw SMTP status codes bubble through as "unknown".
		{
			name:    "email-smtp-auth-failed",
			adapter: "email",
			raw:     "email: send: 535 5.7.8 Authentication credentials invalid",
			want:    "app password",
		},
		{
			name:    "email-recipient-rejected",
			adapter: "email",
			raw:     "email: send: 550 5.1.1 User unknown",
			want:    "deliverable address",
		},
		{
			name:    "email-relay-denied",
			adapter: "email",
			raw:     "email: send: 550 5.7.1 Relay access denied",
			want:    "SPF",
		},
		{
			name:    "email-quota",
			adapter: "email",
			raw:     "email: send: 452 4.5.3 Too many recipients",
			want:    "transactional provider",
		},
		{
			name:    "email-starttls-required",
			adapter: "email",
			raw:     "email: send: 530 5.7.0 Must issue a STARTTLS command first",
			want:    "starttls",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DiagnoseDelivery(tc.adapter, "dest", true, true, errors.New(tc.raw))
			if got.Fix == "" || !strings.Contains(got.Fix, tc.want) {
				t.Fatalf("fix = %q, want substring %q", got.Fix, tc.want)
			}
		})
	}
}
