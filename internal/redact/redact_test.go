package redact

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestValueRedactsNestedAndOpaqueOperatorConfig(t *testing.T) {
	secrets := []string{"header-secret", "innocent-env-value", "postgres-password", "jwt-signing-secret"}
	in := map[string]any{
		"headers": map[string]any{"X-Custom": secrets[0]},
		"env":     map[string]any{"ORDINARY_NAME": secrets[1]},
		"storage": map[string]any{"postgres_dsn": "postgres://user:" + secrets[2] + "@db/soulacy"},
		"nested":  []any{map[string]any{"signing_key": secrets[3]}},
		"safe":    "visible",
	}
	b, _ := json.Marshal(Value(in))
	got := string(b)
	for _, secret := range secrets {
		if strings.Contains(got, secret) {
			t.Fatalf("secret %q leaked: %s", secret, got)
		}
	}
	for _, key := range []string{"X-Custom", "ORDINARY_NAME", "postgres_dsn", "signing_key"} {
		if !strings.Contains(got, key) {
			t.Fatalf("key name %q was not retained: %s", key, got)
		}
	}
}

func TestTextRedactsCredentialsInResults(t *testing.T) {
	got := Text("Authorization: Bearer abcdefghijklmnopqrstuvwxyz token=short-secret postgres://u:inline-password@db/x")
	for _, bad := range []string{"abcdefghijklmnopqrstuvwxyz", "short-secret", "inline-password"} {
		if strings.Contains(got, bad) {
			t.Fatalf("%q leaked in %q", bad, got)
		}
	}
}

// MU-019 requires support bundles to redact secrets *and* personal data, and
// those are different sets. The personal pass is opt-in because losing a
// password from a diagnostic is free, while losing a user_id would make a
// bundle useless for tracing a run.
func TestValueForExportRemovesPersonalDataButValueDoesNot(t *testing.T) {
	in := map[string]any{
		"email":    "alice@example.com",
		"user_id":  "usr_alice",
		"note":     "ping bob@example.com about it",
		"api_key":  "sk-live-abcdef",
		"nested":   map[string]any{"phone": "+1 555 0100"},
		"contacts": []any{"carol@example.com"},
	}

	// The persistence path keeps personal data: an operator debugging their
	// own deployment needs their own logs intact.
	kept, _ := Value(in).(map[string]any)
	if kept["email"] != "alice@example.com" {
		t.Errorf("Value stripped personal data from the persistence path: %v", kept["email"])
	}

	out, ok := ValueForExport(in).(map[string]any)
	if !ok {
		t.Fatalf("ValueForExport returned %T", ValueForExport(in))
	}
	if out["email"] == "alice@example.com" {
		t.Error("an email field survived export redaction")
	}
	if nested, _ := out["nested"].(map[string]any); nested["phone"] == "+1 555 0100" {
		t.Error("a nested personal field survived export redaction")
	}
	if list, _ := out["contacts"].([]any); len(list) > 0 && list[0] == "carol@example.com" {
		t.Error("an email inside a list survived export redaction")
	}
	if note, _ := out["note"].(string); strings.Contains(note, "bob@example.com") {
		t.Errorf("an email in free text survived export redaction: %q", note)
	}
	// Identifiers a bundle is useless without must survive.
	if out["user_id"] != "usr_alice" {
		t.Errorf("export redaction removed a routing identifier: %v", out["user_id"])
	}
	// And credentials are still gone, as before.
	if key, _ := out["api_key"].(string); strings.Contains(key, "sk-live") {
		t.Errorf("a credential survived export redaction: %q", key)
	}
}

func TestPersonalTextScrubsEmailsOnTopOfSecrets(t *testing.T) {
	got := PersonalText("contact alice@example.com token=sk-live-abcdef")
	if strings.Contains(got, "alice@example.com") {
		t.Errorf("email survived: %q", got)
	}
	if strings.Contains(got, "sk-live-abcdef") {
		t.Errorf("credential survived: %q", got)
	}
}
