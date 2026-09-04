package gateway

import "testing"

// The GUI builds its Channels page entirely from channelSpecs. A channel that
// compiles and registers but has no spec is invisible to the user, so these
// tests pin the contract.

func TestChannelSpecs_EmailIsOfferedInTheGUI(t *testing.T) {
	spec := channelSpecByID("email")
	if spec == nil {
		t.Fatal("no channelSpec for email — the channel would be invisible in the GUI")
	}
	if spec.Name == "" {
		t.Error("email spec needs a display name")
	}

	keys := map[string]channelField{}
	for _, f := range spec.Fields {
		keys[f.Key] = f
	}
	for _, want := range []string{"host", "port", "username", "password", "from", "default_output_to", "subject", "tls"} {
		if _, ok := keys[want]; !ok {
			t.Errorf("email spec is missing the %q field", want)
		}
	}
	if !keys["host"].Required {
		t.Error("host must be marked required — SMTP cannot work without it")
	}
	// A password rendered as plain text, or returned unmasked by the API, would
	// leak the user's app password.
	if !keys["password"].Secret || keys["password"].Type != "password" {
		t.Error("the SMTP password must be Secret and of type password so it is masked")
	}
}

// The signing secret is the whole point of signed webhooks; if it isn't in the
// spec, there is no way for a user to turn signing on.
func TestChannelSpecs_WebhookExposesTheSigningSecret(t *testing.T) {
	spec := channelSpecByID("webhook")
	if spec == nil {
		t.Fatal("no channelSpec for webhook")
	}
	if spec.Name != "Outgoing Webhook" {
		t.Fatalf("webhook channel should be labeled as outbound delivery, got %q", spec.Name)
	}
	for _, f := range spec.Fields {
		if f.Key == "secret" {
			if !f.Secret || f.Type != "password" {
				t.Error("the webhook signing secret must be Secret and of type password")
			}
			return
		}
	}
	t.Fatal(`webhook spec has no "secret" field — request signing cannot be enabled from the GUI`)
}

func TestChannelSpecs_TeamsIsOfferedAsOutboundWebhook(t *testing.T) {
	spec := channelSpecByID("teams")
	if spec == nil {
		t.Fatal("no channelSpec for teams — Microsoft Teams would be invisible in the GUI")
	}
	if spec.Name != "Microsoft Teams" {
		t.Fatalf("teams display name = %q", spec.Name)
	}
	keys := map[string]channelField{}
	for _, f := range spec.Fields {
		keys[f.Key] = f
	}
	webhook := keys["webhook_url"]
	if !webhook.Required || !webhook.Secret || webhook.Type != "password" {
		t.Fatalf("teams webhook_url must be required, secret, and password typed: %#v", webhook)
	}
	if _, ok := keys["default_output_to"]; !ok {
		t.Fatal("teams should expose default_output_to for schedule output overrides")
	}
}

func TestChannelSpecs_GoogleChatIsOfferedAsOutboundWebhook(t *testing.T) {
	spec := channelSpecByID("google_chat")
	if spec == nil {
		t.Fatal("no channelSpec for google_chat — Google Chat would be invisible in the GUI")
	}
	if spec.Name != "Google Chat" {
		t.Fatalf("google_chat display name = %q", spec.Name)
	}
	keys := map[string]channelField{}
	for _, f := range spec.Fields {
		keys[f.Key] = f
	}
	webhook := keys["webhook_url"]
	if !webhook.Required || !webhook.Secret || webhook.Type != "password" {
		t.Fatalf("google_chat webhook_url must be required, secret, and password typed: %#v", webhook)
	}
	if _, ok := keys["prefix"]; !ok {
		t.Fatal("google_chat should expose a message prefix field")
	}
}
