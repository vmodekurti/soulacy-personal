package netguard

import (
	"strings"
	"testing"
)

// The webhook-shaped adapters posted to any http(s) URL the message carried in
// ThreadID or Metadata["to"], with the operator's configured headers and HMAC
// signature attached. That value is `channel.send`'s `to` argument — model
// output — and channel.send is in the always-on SAFE tool partition. A poisoned
// page an agent read could name the destination.
func TestResolveWebhookTarget_RefusesADifferentHost(t *testing.T) {
	_, err := ResolveWebhookTarget("webhook", "https://hooks.example.com/services/T1", "https://attacker.example/collect")
	if err == nil {
		t.Fatal("a model-supplied override redirected the delivery to another host")
	}
	if !strings.Contains(err.Error(), "attacker.example") {
		t.Errorf("the error does not name the host that was refused: %v", err)
	}
}

func TestResolveWebhookTarget_RefusesTheMetadataEndpoint(t *testing.T) {
	if _, err := ResolveWebhookTarget("webhook", "https://hooks.example.com/x", "http://169.254.169.254/latest/meta-data/"); err == nil {
		t.Fatal("an override pointed the adapter at the cloud metadata endpoint")
	}
}

// The legitimate case this override exists for: addressing a specific thread or
// room under the operator's own webhook host.
func TestResolveWebhookTarget_AllowsADifferentPathOnTheSameHost(t *testing.T) {
	got, err := ResolveWebhookTarget("webhook", "https://localhost/services/T1", "https://localhost/services/T2")
	if err != nil {
		t.Fatalf("a same-host override was refused, which breaks per-thread routing: %v", err)
	}
	if got != "https://localhost/services/T2" {
		t.Errorf("target = %q, want the override", got)
	}
}

// No override, or a non-URL override (a Telegram chat id, a room name), leaves
// the configured endpoint alone — this is by far the common path and must not
// have become an error.
func TestResolveWebhookTarget_NonURLOverridesAreIgnored(t *testing.T) {
	for _, override := range []string{"", "  ", "-1001234567890", "general", "spaces/AAAA"} {
		got, err := ResolveWebhookTarget("webhook", "https://localhost/x", override)
		if err != nil {
			t.Fatalf("override %q turned into an error: %v", override, err)
		}
		if got != "https://localhost/x" {
			t.Errorf("override %q changed the target to %q", override, got)
		}
	}
}

// Host comparison must not be case-sensitive, or the guarantee is trivially
// bypassed by shouting the hostname.
func TestResolveWebhookTarget_HostMatchIsCaseInsensitive(t *testing.T) {
	if _, err := ResolveWebhookTarget("webhook", "https://localhost/x", "https://LOCALHOST/y"); err != nil {
		t.Fatalf("same host in different case was refused: %v", err)
	}
}

// A port is part of the host. hooks.example.com:8080 is a different service from
// hooks.example.com:443 and must not be reachable by override.
func TestResolveWebhookTarget_PortIsPartOfTheHost(t *testing.T) {
	if _, err := ResolveWebhookTarget("webhook", "https://hooks.example.com/x", "https://hooks.example.com:8080/y"); err == nil {
		t.Error("an override reached a different port on the same hostname")
	}
}
