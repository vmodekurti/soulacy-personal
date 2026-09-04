// engine_env_test.go — SEC-5: per-agent environment allowlist for tool
// subprocesses. Verifies the engine scrubs the environment of spawned Python
// tools so they cannot read gateway secrets, while still passing through the
// base allowlist and any per-agent declared vars.
package runtime

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
)

// TestRunTool_EnvAllowlist_HidesSecretsExposesDeclared spawns a real Python
// inline tool and asserts it cannot see ANTHROPIC_API_KEY but CAN see a
// declared var and the base allowlist (PATH).
func TestRunTool_EnvAllowlist_HidesSecretsExposesDeclared(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}

	// Gateway-style environment: a secret plus a var the agent will declare.
	t.Setenv("ANTHROPIC_API_KEY", "sk-leak-me-not")
	t.Setenv("AGENT_DECLARED_VAR", "declared-value")

	e := newMinimalEngine(t)
	e.pythonBin = py

	// Inline tool: print env visibility. It ignores stdin args.
	inline := strings.Join([]string{
		"import os",
		"print('SECRET=' + os.environ.get('ANTHROPIC_API_KEY',''))",
		"print('DECLARED=' + os.environ.get('AGENT_DECLARED_VAR',''))",
		"print('PATH_SET=' + ('yes' if os.environ.get('PATH') else 'no'))",
	}, "\n")

	def := &agent.Definition{
		ID:  "env-agent",
		Env: []string{"AGENT_DECLARED_VAR"},
		Tools: []agent.ToolDef{{
			Name:   "envprobe",
			Inline: inline,
		}},
	}

	out, err := e.runTool(context.Background(), def, "sess-env", message.ToolCall{
		ID:        "call-env",
		Name:      "envprobe",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("runTool: %v", err)
	}

	if !strings.Contains(out, "SECRET=") {
		t.Fatalf("unexpected tool output (no SECRET line):\n%s", out)
	}
	if strings.Contains(out, "sk-leak-me-not") {
		t.Errorf("tool leaked ANTHROPIC_API_KEY; output:\n%s", out)
	}
	if !strings.Contains(out, "DECLARED=declared-value") {
		t.Errorf("declared var not visible to tool; output:\n%s", out)
	}
	if !strings.Contains(out, "PATH_SET=yes") {
		t.Errorf("base var PATH not visible to tool; output:\n%s", out)
	}
}
