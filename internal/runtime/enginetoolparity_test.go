package runtime

// The runtime's privileged partition and Studio's gated list must not drift.
//
// python_eval runs `python3 -c <whatever the model wrote>` — the same
// capability as shell_exec in different clothing. It sat in the SAFE partition,
// so it was offered to every agent with no `system` capability and no
// allow_system_agents entry, and dispatched without the deterministic guardrail
// privileged tools receive. Combined with a subprocess environment that
// inherited the gateway's own (ANTHROPIC_API_KEY, OPENAI_API_KEY, database
// URLs), any principal who could send a chat message could read every secret
// the gateway held.
//
// Studio already treated it as gated. internal/studio/validate.go says its list
// "mirrors the runtime's privilegedSystemTools plus python_eval" — the gap was
// written down as if it were a deliberate superset. Two lists, one of them
// wrong, and a comment that made the divergence look intentional.

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/internal/sandbox"
)

func TestPythonEvalIsPrivileged(t *testing.T) {
	if !isPrivilegedSystemTool("python_eval") {
		t.Fatal("python_eval executes arbitrary code but is in the SAFE partition — " +
			"every agent gets it with no capability check and no guardrail")
	}
}

func TestPackageInstallRemainsPrivilegedButUsesManagedExecution(t *testing.T) {
	if !isPrivilegedSystemTool("package_install") {
		t.Fatal("package_install must remain privileged for RBAC, policy, approval, and audit")
	}
	if requiresPrivilegedIsolation("package_install") {
		t.Fatal("package_install must use its fixed-argv managed execution path")
	}
	if !requiresPrivilegedIsolation("shell_exec") {
		t.Fatal("shell_exec must continue to require privileged isolation")
	}
}

// The SAFE partition is what any agent gets by default, so anything in it that
// can run code or write to the host is a hole. This asserts the whole set, not
// just the one that was wrong, so the next addition has to be a deliberate act.
func TestSafePartitionContainsNoCodeExecution(t *testing.T) {
	e := &Engine{}
	for _, b := range e.safeSystemTools() {
		switch b.Name {
		case "python_eval", "shell_exec", "run_script", "install_library", "package_install", "write_file", "download_file":
			t.Errorf("%q is offered to every agent with no capability check", b.Name)
		}
	}
}

// gatedSystemTools in Studio and privilegedSystemTools here describe the same
// boundary from two sides. Read Studio's list from source rather than copying
// it, because a hand-copied list is exactly how they diverged.
func TestStudioGatedListMatchesTheRuntimePartition(t *testing.T) {
	src, err := os.ReadFile("../studio/validate.go")
	if err != nil {
		t.Skipf("cannot read Studio's list: %v", err)
	}
	block := regexp.MustCompile(`(?s)gatedSystemTools = map\[string\]bool\{(.*?)\}`).FindStringSubmatch(string(src))
	if len(block) < 2 {
		t.Fatal("gatedSystemTools is gone or renamed — this rule now guards nothing")
	}
	for _, m := range regexp.MustCompile(`"([a-z_]+)"`).FindAllStringSubmatch(block[1], -1) {
		name := m[1]
		if name == "shell" {
			continue // an alias Studio accepts; the runtime has no such tool
		}
		if !isPrivilegedSystemTool(name) {
			t.Errorf("Studio gates %q but the runtime hands it to every agent", name)
		}
	}
}

// A subprocess must not be able to read the gateway's secrets.
func TestShellEnvironWithholdsGatewaySecrets(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-should-not-escape")
	t.Setenv("DATABASE_URL", "postgres://user:pw@host/db")
	t.Setenv("PATH", "/usr/bin")

	e := &Engine{}
	env := strings.Join(e.shellEnviron(), "\n")

	if strings.Contains(env, "sk-ant-should-not-escape") {
		t.Error("the model's subprocess can read ANTHROPIC_API_KEY")
	}
	if strings.Contains(env, "postgres://user:pw@host/db") {
		t.Error("the model's subprocess can read DATABASE_URL")
	}
	if !strings.Contains(env, "PATH=") {
		t.Error("PATH was withheld too — python3 and friends will not resolve")
	}
}

// nil means "inherit everything" to exec.Cmd, which is how this hole existed.
// The function must never return nil, even with no extras configured.
func TestShellEnvironNeverInheritsWholesale(t *testing.T) {
	e := &Engine{}
	if e.shellEnviron() == nil {
		t.Fatal("nil Env makes exec.Cmd inherit the parent environment entirely")
	}
}

// The path hints the gateway sets deliberately must still arrive.
func TestShellEnvironKeepsTheCanonicalPathHints(t *testing.T) {
	e := &Engine{}
	e.SetAgentShellEnv([]string{"SOULACY_WORKSPACE=/tmp/ws"})
	if !strings.Contains(strings.Join(e.shellEnviron(), "\n"), "SOULACY_WORKSPACE=/tmp/ws") {
		t.Error("agents can no longer find the canonical workspace")
	}
}

// Guard the allowlist itself: the operational four, and nothing that smells
// like a credential.
func TestBaseEnvAllowlistStaysMinimal(t *testing.T) {
	for _, k := range sandbox.BaseEnvAllowlist {
		switch k {
		case "PATH", "HOME", "LANG", "TMPDIR":
		default:
			t.Errorf("%q was added to the always-passed env allowlist — is it really secret-free?", k)
		}
	}
}
