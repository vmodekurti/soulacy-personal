package audit

import (
	"strings"
	"testing"
	"time"
)

// redactArgs inspected only TOP-LEVEL keys, with a comment explaining that
// nested objects were left alone to avoid deep-copying large structures. But
// arguments carrying credentials are almost never flat — an http_request call
// puts its bearer in {"headers":{"Authorization":"…"}} — so the redaction ran,
// matched nothing, and wrote the token into the audit log. That log is exactly
// the file an operator ships to someone else when asking for help.
func TestRedactArgs_ReachesNestedSecrets(t *testing.T) {
	out := redactArgs(map[string]any{
		"url": "https://api.example.com/v1/x",
		"headers": map[string]any{
			"Authorization": "Bearer sk-realkeyvalue",
			"Accept":        "application/json",
		},
		"retries": []any{
			map[string]any{"api_key": "ak-realkeyvalue"},
		},
	})

	blob := flatten(out)
	for _, secret := range []string{"sk-realkeyvalue", "ak-realkeyvalue"} {
		if strings.Contains(blob, secret) {
			t.Errorf("the credential %q reached the audit log: %s", secret, blob)
		}
	}
	// Non-secret context must survive, or the log stops being useful.
	if !strings.Contains(blob, "api.example.com") || !strings.Contains(blob, "application/json") {
		t.Errorf("ordinary values were redacted too: %s", blob)
	}
}

// The input must not be mutated — the caller still holds these arguments and is
// about to execute the tool with them.
func TestRedactArgs_DoesNotMutateTheCaller(t *testing.T) {
	nested := map[string]any{"token": "t-realvalue"}
	args := map[string]any{"headers": nested}
	_ = redactArgs(args)
	if nested["token"] != "t-realvalue" {
		t.Fatalf("redaction wrote through into the caller's arguments: %v", nested)
	}
}

// Model-authored arguments can nest arbitrarily; the walk must terminate.
func TestRedactArgs_TerminatesOnDeepNesting(t *testing.T) {
	inner := map[string]any{"password": "p"}
	cur := inner
	for i := 0; i < 200; i++ {
		cur = map[string]any{"a": cur}
	}
	done := make(chan struct{})
	go func() { defer close(done); _ = redactArgs(cur) }()
	select {
	case <-done:
	case <-timeoutAfterASecond():
		t.Fatal("redactArgs did not terminate on deeply nested arguments")
	}
}

func flatten(v any) string {
	var b strings.Builder
	var walk func(any)
	walk = func(v any) {
		switch t := v.(type) {
		case map[string]any:
			for k, item := range t {
				b.WriteString(k)
				b.WriteByte('=')
				walk(item)
				b.WriteByte(' ')
			}
		case []any:
			for _, item := range t {
				walk(item)
			}
		default:
			b.WriteString(strings.TrimSpace(stringify(t)))
		}
	}
	walk(v)
	return b.String()
}

func stringify(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func timeoutAfterASecond() <-chan time.Time { return time.After(time.Second) }
