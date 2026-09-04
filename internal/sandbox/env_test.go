package sandbox

import (
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

// TestFilteredEnv_DropsSecretsKeepsBaseAndDeclared pins the SEC-5 allowlist
// behaviour: a secret variable is withheld, while the base allowlist and an
// explicitly-declared variable pass through.
func TestFilteredEnv_DropsSecretsKeepsBaseAndDeclared(t *testing.T) {
	parent := []string{
		"PATH=/usr/bin:/bin",
		"HOME=/home/agent",
		"LANG=en_US.UTF-8",
		"TMPDIR=/tmp",
		"ANTHROPIC_API_KEY=sk-secret-do-not-leak",
		"OPENAI_API_KEY=sk-also-secret",
		"MY_DECLARED_VAR=hello",
		"UNDECLARED=nope",
	}

	got := FilteredEnv(parent, []string{"MY_DECLARED_VAR"})
	set := map[string]string{}
	for _, kv := range got {
		eq := strings.IndexByte(kv, '=')
		set[kv[:eq]] = kv[eq+1:]
	}

	// Base allowlist present.
	for _, k := range []string{"PATH", "HOME", "LANG", "TMPDIR"} {
		if _, ok := set[k]; !ok {
			t.Errorf("base var %q should pass through, missing from %v", k, got)
		}
	}
	// Declared var present.
	if set["MY_DECLARED_VAR"] != "hello" {
		t.Errorf("declared var MY_DECLARED_VAR should pass through; got %q", set["MY_DECLARED_VAR"])
	}
	// Secrets and undeclared vars withheld.
	for _, k := range []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "UNDECLARED"} {
		if _, ok := set[k]; ok {
			t.Errorf("var %q must NOT pass through the SEC-5 allowlist", k)
		}
	}
}

// TestFilteredEnv_SkipsMissingDeclaredVars confirms a declared name with no
// value in the parent environment is simply omitted (not emitted as "NAME=").
func TestFilteredEnv_SkipsMissingDeclaredVars(t *testing.T) {
	got := FilteredEnv([]string{"PATH=/bin"}, []string{"NOT_PRESENT"})
	for _, kv := range got {
		if strings.HasPrefix(kv, "NOT_PRESENT=") {
			t.Errorf("missing declared var should be skipped, got %q", kv)
		}
	}
}

// TestSpawnedProcessCannotSeeSecret is the end-to-end SEC-5 guarantee: a real
// child process launched with the filtered environment cannot read a secret
// the parent holds (ANTHROPIC_API_KEY) but CAN read an explicitly-declared var
// and the base allowlist (PATH/HOME).
func TestSpawnedProcessCannotSeeSecret(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses /bin/sh; not applicable on windows")
	}

	// Seed the gateway-style environment for this process.
	t.Setenv("ANTHROPIC_API_KEY", "sk-super-secret")
	t.Setenv("AGENT_VISIBLE_VAR", "i-am-allowed")
	t.Setenv("HOME", "/home/agent-test")

	// Spawn /bin/sh with the FILTERED environment, declaring only AGENT_VISIBLE_VAR.
	cmd := exec.Command("/bin/sh", "-c",
		`printf 'SECRET=[%s] VISIBLE=[%s] PATH_SET=[%s] HOME=[%s]' `+
			`"$ANTHROPIC_API_KEY" "$AGENT_VISIBLE_VAR" "${PATH:+yes}" "$HOME"`)
	cmd.Env = FilteredEnviron([]string{"AGENT_VISIBLE_VAR"})

	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("run sh: %v", err)
	}
	got := string(out)

	if !strings.Contains(got, "SECRET=[]") {
		t.Errorf("child process leaked ANTHROPIC_API_KEY: %q", got)
	}
	if !strings.Contains(got, "VISIBLE=[i-am-allowed]") {
		t.Errorf("declared var not visible to child: %q", got)
	}
	if !strings.Contains(got, "PATH_SET=[yes]") {
		t.Errorf("base var PATH not visible to child: %q", got)
	}
	if !strings.Contains(got, "HOME=[/home/agent-test]") {
		t.Errorf("base var HOME not visible/correct in child: %q", got)
	}
	// Belt-and-suspenders: the raw secret string must not appear anywhere.
	if strings.Contains(got, "sk-super-secret") {
		t.Errorf("secret value leaked into child output: %q", got)
	}
}
