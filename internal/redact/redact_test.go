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
