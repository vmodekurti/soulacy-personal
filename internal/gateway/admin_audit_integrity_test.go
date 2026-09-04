// admin_audit_integrity_test.go — MU-031 criteria 1 and 3: an audit record
// carries a redacted change summary, and the trail is append-oriented in a
// sense that survives load.
//
// Both properties failed in the same quiet way before this: the redaction ran
// and matched nothing, and the write was dropped and reported nothing. Neither
// produces an error, a wrong value, or a failing test of the feature itself —
// the evidence in both cases is an absence.
package gateway

import (
	"reflect"
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/soulacy/soulacy/pkg/message"
	"github.com/soulacy/soulacy/sdk/storage"
)

// THE BUG. scrubAuditDetails inspected top-level keys only, so any nesting at
// all went through unchanged. internal/audit/redactArgs was rewritten for
// exactly this reason and says so in its comment; this one had not been.
func TestTheChangeSummaryIsScrubbedAtEveryDepth(t *testing.T) {
	scrubbed := scrubAuditDetails(map[string]any{
		"credential": map[string]any{
			"name":    "deploy-bot",
			"api_key": "sk_live_do_not_log_me",
		},
		"bindings": []any{
			map[string]any{"channel": "slack", "token": "xoxb-secret"},
		},
		"name": "deploy-bot",
	})

	// A secret-named key holding a MAP is descended into, not blanked: the
	// name describes the group and the secret is a leaf inside it. Blanking
	// wholesale would make the audit records for the most sensitive actions
	// the least informative, because this codebase's credential call sites
	// nest exactly like this.
	nested, _ := scrubbed["credential"].(map[string]any)
	if nested == nil || nested["api_key"] != "***" {
		t.Fatalf("a nested api_key survived scrubbing: %v", scrubbed)
	}
	if nested["name"] != "deploy-bot" {
		t.Fatalf("scrubbing removed a non-secret detail: %v", nested)
	}
	list, _ := scrubbed["bindings"].([]any)
	if len(list) != 1 {
		t.Fatalf("bindings lost: %v", scrubbed)
	}
	if inList, _ := list[0].(map[string]any); inList == nil || inList["token"] != "***" {
		t.Fatalf("a secret inside a list survived scrubbing: %v", scrubbed)
	}
	if scrubbed["name"] != "deploy-bot" {
		t.Fatalf("a top-level non-secret was removed: %v", scrubbed)
	}
}

// The key list had gaps that this codebase's own call sites walk into.
func TestTheScrubberKnowsTheNamesThisCodebaseUses(t *testing.T) {
	for _, key := range []string{
		"secret", "token", "password", "passwd", "api_key", "apikey",
		"credential", "private_key", "auth_header", "SECRET_VALUE", "refresh_token",
	} {
		out := scrubAuditDetails(map[string]any{key: "sensitive"})
		if out[key] != "***" {
			t.Errorf("key %q was written to the audit log in the clear", key)
		}
	}
	// A LIST under a secret-named key is blanked rather than descended into:
	// there are no benign siblings to preserve, and the entries are the
	// secrets themselves.
	if out := scrubAuditDetails(map[string]any{"secrets": []any{"a", "b"}}); out["secrets"] != "***" {
		t.Errorf("a list of secrets was written in the clear: %v", out)
	}
	// And it must not scrub everything: a redaction that matches every key
	// produces a record that proves nothing happened.
	out := scrubAuditDetails(map[string]any{"action": "x", "count": 3, "workspace_id": "ws_a"})
	if out["action"] != "x" || out["count"] != 3 || out["workspace_id"] != "ws_a" {
		t.Fatalf("ordinary details were scrubbed: %v", out)
	}
}

// A cyclic or pathologically nested detail map must not hang a request through
// the audit path — but the subtree is dropped rather than passed through, so
// the bound cannot become a way to smuggle a secret past the scrubber.
func TestDeepNestingIsBoundedAndDroppedRatherThanPassed(t *testing.T) {
	deep := map[string]any{"api_key": "sk_live_deep"}
	for i := 0; i < 40; i++ {
		deep = map[string]any{"wrap": deep}
	}
	out := scrubAuditDetails(deep)
	if !containsNoSecret(t, out) {
		t.Fatal("a secret escaped past the depth bound")
	}
}

func containsNoSecret(t *testing.T, v any) bool {
	t.Helper()
	switch typed := v.(type) {
	case map[string]any:
		for _, item := range typed {
			if !containsNoSecret(t, item) {
				return false
			}
		}
	case []any:
		for _, item := range typed {
			if !containsNoSecret(t, item) {
				return false
			}
		}
	case string:
		return typed != "sk_live_deep"
	}
	return true
}

// wedgedLog is an action log whose queue never drains.
type wedgedLog struct {
	storage.ActionLogBackend
	appended []message.Event
	accept   bool
}

func (w *wedgedLog) Append(ev message.Event)                   { w.appended = append(w.appended, ev) }
func (w *wedgedLog) AppendDurable(ev message.Event) bool       { return w.accept }
func (w *wedgedLog) Tail(string, int) ([]message.Event, error) { return nil, nil }
func (w *wedgedLog) EventFilePath(string) string               { return "" }
func (w *wedgedLog) Close() error                              { return nil }

// The durable surface must actually be the one the audit path uses. Asserted
// through recordAdminAudit rather than by calling AppendDurable directly:
// the helper working proves nothing about whether the audit path reaches it,
// which is the shape of test that has passed for the wrong reason repeatedly
// on this branch.
func TestTheAuditPathUsesTheDurableSurface(t *testing.T) {
	log := &wedgedLog{accept: true}
	var _ storage.DurableActionLogBackend = log

	srv := newTestGateway(t, "secret")
	srv.actions = log
	srv.recordAdminAudit(nil, "credential.create", "credential", "cred_1", "ok", nil)

	if len(log.appended) != 0 {
		t.Fatalf("the audit path used the droppable Append: %+v", log.appended)
	}
}

// And a refused durable write must not look like a successful one. There is no
// return value to assert on — recordAdminAudit is fire-and-forget by design —
// so what is pinned is that the refusal reaches the logger rather than being
// swallowed, which is the difference between a reportable loss and an absence.
func TestARefusedAuditWriteIsNotSilentlySwallowed(t *testing.T) {
	log := &wedgedLog{accept: false}
	srv := newTestGateway(t, "secret")
	srv.actions = log

	recorded := &capturingCore{}
	srv.log = recorded.logger()
	// A directly-constructed Server has a nil logger, and the loudest thing
	// this path does — reporting a LOST audit record — dereferenced it. A
	// fail-loud path that panics instead of reporting is worse than a silent
	// one, so recordAdminAudit goes through s.logger(). Pinned here because
	// the crash surfaced in an unrelated test and looked like that test's bug.
	srv.recordAdminAudit(nil, "credential.create", "credential", "cred_1", "ok", nil)

	if !recorded.sawError("credential.create") {
		t.Fatalf("a lost audit record produced no error naming the action: %v", recorded.messages)
	}
	if len(log.appended) != 0 {
		t.Fatal("a refused durable write silently fell back to the droppable path")
	}
}

func TestTheScrubberIsAppliedByTheRecorder(t *testing.T) {
	log := &wedgedLog{accept: true}
	srv := newTestGateway(t, "secret")
	srv.actions = log
	var captured message.Event
	log.accept = true
	srv.actions = &capturingLog{onDurable: func(ev message.Event) { captured = ev }}

	srv.recordAdminAudit(nil, "credential.create", "credential", "cred_1", "ok",
		map[string]any{"detail": map[string]any{"api_key": "sk_live_x"}})

	rec, ok := captured.Payload.(adminAuditRecord)
	if !ok {
		t.Fatalf("payload is not an audit record: %T", captured.Payload)
	}
	nested, _ := rec.Details["detail"].(map[string]any)
	if nested == nil || nested["api_key"] != "***" {
		t.Fatalf("the recorder wrote an unscrubbed nested secret: %v", rec.Details)
	}
	if !reflect.DeepEqual(rec.Action, "credential.create") {
		t.Fatalf("action lost: %v", rec.Action)
	}
}

type capturingLog struct {
	storage.ActionLogBackend
	onDurable func(message.Event)
}

func (c *capturingLog) Append(message.Event) {}
func (c *capturingLog) AppendDurable(ev message.Event) bool {
	c.onDurable(ev)
	return true
}
func (c *capturingLog) Tail(string, int) ([]message.Event, error) { return nil, nil }
func (c *capturingLog) EventFilePath(string) string               { return "" }
func (c *capturingLog) Close() error                              { return nil }

// capturingCore records what was logged, so "the loss was reported" is
// testable without a real sink.
type capturingCore struct {
	messages []string
}

func (c *capturingCore) logger() *zap.Logger {
	return zap.New(zapcore.NewCore(
		zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()),
		zapcore.AddSync(&coreWriter{core: c}),
		zapcore.DebugLevel,
	))
}

func (c *capturingCore) sawError(needle string) bool {
	for _, line := range c.messages {
		if strings.Contains(line, needle) && strings.Contains(line, "LOST") {
			return true
		}
	}
	return false
}

type coreWriter struct{ core *capturingCore }

func (w *coreWriter) Write(p []byte) (int, error) {
	w.core.messages = append(w.core.messages, string(p))
	return len(p), nil
}

func TestTheAuditRecorderSurvivesAServerWithNoLogger(t *testing.T) {
	srv := &Server{actions: &wedgedLog{accept: false}}
	// Must not panic. The nil logger is real: several handlers construct a
	// Server directly, and this is the path that reports a lost record.
	srv.recordAdminAudit(nil, "credential.create", "credential", "cred_1", "ok", nil)
}
