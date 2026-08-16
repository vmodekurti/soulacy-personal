// redact_test.go — an approver is shown enough to judge and no more.
package approvals

import (
	"strings"
	"testing"
)

// A blocklist of "password, token, secret" fails on the first argument called
// authorization, pat, bearer, x-api-key or cookie — and fails silently.
func TestCredentialShapedKeysAreElidedHoweverTheyAreSpelled(t *testing.T) {
	for _, key := range []string{
		"password", "Password", "passwd", "api_key", "apiKey", "X-Api-Key",
		"Authorization", "authorization", "bearer_token", "AWS_SECRET_ACCESS_KEY",
		"privateKey", "db-credential", "Cookie", "session_id", "passphrase",
		"otp", "signature", "mnemonic",
	} {
		out := Redact(map[string]any{key: "hunter2"})
		if out[key] != "[redacted]" {
			t.Errorf("%s was shown as %v", key, out[key])
		}
	}
}

// And the ones an approver actually reads survive intact, or the record is
// useless for the decision it exists to support.
func TestTheArgumentsAnApproverJudgesSurviveIntact(t *testing.T) {
	args := map[string]any{
		"command":         "rm -rf /var/lib/data",
		"path":            "/srv/app/config.yaml",
		"url":             "https://api.example.com/v1/customers/42",
		"timeout_seconds": 30,
		"recursive":       true,
	}
	out := Redact(args)
	for key, want := range args {
		if out[key] != want {
			t.Errorf("%s = %v, want %v — the approver cannot judge without it", key, out[key], want)
		}
	}
}

// A long scalar is where a pasted key, a base64 blob or a document body hides.
func TestLongValuesAreSummarisedRatherThanStored(t *testing.T) {
	long := strings.Repeat("A", 5000)
	out := Redact(map[string]any{"body": long})
	shown, ok := out["body"].(string)
	if !ok {
		t.Fatalf("body = %T", out["body"])
	}
	if len(shown) >= len(long) {
		t.Fatal("a 5000-character value was stored in full")
	}
	if !strings.Contains(shown, "more characters") {
		t.Fatalf("truncation is silent: %q", shown)
	}
	// The prefix is kept: an approver needs to recognise WHAT was truncated.
	if !strings.HasPrefix(shown, "AAAA") {
		t.Fatalf("truncation kept nothing recognisable: %q", shown)
	}
}

// Nesting is where a redactor that only looks at top-level keys leaks.
func TestNestedAndListedCredentialsAreElidedToo(t *testing.T) {
	out := Redact(map[string]any{
		"headers": map[string]any{"Authorization": "Bearer sk-live-abcdef", "Accept": "application/json"},
		"targets": []any{
			map[string]any{"host": "db1", "password": "p1"},
			map[string]any{"host": "db2", "password": "p2"},
		},
	})
	headers, _ := out["headers"].(map[string]any)
	if headers["Authorization"] != "[redacted]" {
		t.Errorf("a nested credential leaked: %v", headers["Authorization"])
	}
	if headers["Accept"] != "application/json" {
		t.Errorf("a nested harmless header was destroyed: %v", headers["Accept"])
	}
	targets, _ := out["targets"].([]any)
	if len(targets) != 2 {
		t.Fatalf("list length was lost — 'how many things is this about to touch' is what the approver is judging")
	}
	for i, element := range targets {
		entry, _ := element.(map[string]any)
		if entry["password"] != "[redacted]" {
			t.Errorf("targets[%d] leaked a credential: %v", i, entry["password"])
		}
		if entry["host"] == nil {
			t.Errorf("targets[%d] lost the field the approver needs", i)
		}
	}
}

// A type the redactor has not been taught about is exactly the one whose
// String() might spill something.
func TestAnUnrecognisedShapeIsSummarisedNotRendered(t *testing.T) {
	type opaque struct{ Secret string }
	out := Redact(map[string]any{"thing": opaque{Secret: "sk-live-leak"}})
	shown, _ := out["thing"].(string)
	if strings.Contains(shown, "sk-live-leak") {
		t.Fatalf("an unrecognised type was rendered in full: %q", shown)
	}
	if !strings.Contains(shown, "opaque") {
		t.Fatalf("the summary says nothing useful: %q", shown)
	}
}

// The store is the only way to create a durable approval, so the durable copy
// is redacted whether or not a caller remembered.
func TestTheStoredCopyIsRedactedEvenWhenTheCallerPassesEverything(t *testing.T) {
	store := newStore(t)
	approval := request(t, store, "apr_secret", "ws-a", map[string]any{
		"url":     "https://api.example.com/charge",
		"headers": map[string]any{"Authorization": "Bearer sk-live-REALKEY"},
	})
	stored, err := store.Get(t.Context(), "ws-a", approval.ID)
	if err != nil {
		t.Fatal(err)
	}
	encoded := flatten(stored.Args)
	if strings.Contains(encoded, "sk-live-REALKEY") {
		t.Fatalf("the durable record holds the live credential: %s", encoded)
	}
	// The fingerprint still binds the FULL call, so redaction does not
	// weaken criterion 5.
	full := map[string]any{
		"url":     "https://api.example.com/charge",
		"headers": map[string]any{"Authorization": "Bearer sk-live-REALKEY"},
	}
	if stored.Fingerprint != Fingerprint("shell_exec", full) {
		t.Fatal("the fingerprint was taken over the redacted arguments, so a changed credential would still verify")
	}
}

func flatten(v any) string {
	var sb strings.Builder
	switch typed := v.(type) {
	case map[string]any:
		for key, value := range typed {
			sb.WriteString(key)
			sb.WriteString("=")
			sb.WriteString(flatten(value))
			sb.WriteString(";")
		}
	case []any:
		for _, element := range typed {
			sb.WriteString(flatten(element))
			sb.WriteString(",")
		}
	default:
		sb.WriteString(strings.TrimSpace(strings.Join(strings.Fields(strings.ToValidUTF8(toString(typed), "")), " ")))
	}
	return sb.String()
}

func toString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
