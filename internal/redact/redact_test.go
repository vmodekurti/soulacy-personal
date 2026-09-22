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

func TestSecretKeyNameIsTheSharedCredentialPredicate(t *testing.T) {
	for _, key := range []string{"Authorization", "X-Api-Key", "client_secret", "access_token"} {
		if !SecretKeyName(key) {
			t.Errorf("%q was not classified as sensitive", key)
		}
	}
	for _, key := range []string{"region", "tenant", "locale"} {
		if SecretKeyName(key) {
			t.Errorf("%q was incorrectly classified as sensitive", key)
		}
	}
}

// #197 — a wide markdown table separator is not a secret; real keys still are.
func TestTextKeepsTableSeparators(t *testing.T) {
	table := "| # | Airline | Outbound | Return | Stops | Price |\n|---|---------|------------------|-----------------|-------|-------|\n| 1 | BA | 4:29pm | 6:49am | 1 | $770 |"
	if got := Text(table); got != table {
		t.Fatalf("table separator was redacted:\n%s", got)
	}
	// Fake shapes for the scanner's benefit: not real keys. gitleaks:allow
	for _, secret := range []string{
		"sk-abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJ", // gitleaks:allow
		"ghp_A1b2C3d4E5f6G7h8I9j0K1l2M3n4O5p6Q7r8S9t0",      // gitleaks:allow
		"a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2",
	} {
		if got := Text("token " + secret + " here"); got == "token "+secret+" here" {
			t.Errorf("secret survived: %s", secret)
		}
	}
}
