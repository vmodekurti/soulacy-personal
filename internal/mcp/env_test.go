// env_test.go — MU-017 criterion 5: an MCP subprocess does not inherit the
// gateway's credentials.
package mcp

import (
	"strings"
	"testing"
)

func envMap(entries []string) map[string]string {
	out := map[string]string{}
	for _, entry := range entries {
		if k, v, ok := strings.Cut(entry, "="); ok {
			out[k] = v
		}
	}
	return out
}

// The gateway's environment holds provider keys, the static server key, and
// database DSNs. An MCP server is third-party code installed precisely because
// nobody wanted to write it — handing it all of that is the wrong default even
// with one tenant, and with several it is one tenant's extension holding
// another tenant's keys.
func TestASubprocessDoesNotInheritGatewayCredentials(t *testing.T) {
	parent := []string{
		"PATH=/usr/bin",
		"HOME=/home/gateway",
		"ANTHROPIC_API_KEY=sk-ant-secret",
		"OPENAI_API_KEY=sk-openai-secret",
		"SOULACY_API_KEY=static-server-key",
		"DATABASE_URL=postgres://user:password@host/db",
		"AWS_SECRET_ACCESS_KEY=aws-secret",
		"GITHUB_TOKEN=ghp_secret",
	}
	env, withheld := ProcessEnv(ServerConfig{}, parent)
	got := envMap(env)

	if got["PATH"] != "/usr/bin" || got["HOME"] != "/home/gateway" {
		t.Fatalf("a child must still be able to run: %v", got)
	}
	for _, secret := range []string{
		"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "SOULACY_API_KEY",
		"DATABASE_URL", "AWS_SECRET_ACCESS_KEY", "GITHUB_TOKEN",
	} {
		if _, leaked := got[secret]; leaked {
			t.Errorf("%s was passed to an MCP subprocess", secret)
		}
	}
	if withheld != 6 {
		t.Errorf("withheld count = %d, want 6", withheld)
	}
	// The whole environment must not be reachable by joining what was passed.
	joined := strings.Join(env, "\n")
	for _, value := range []string{"sk-ant-secret", "sk-openai-secret", "static-server-key", "password", "aws-secret", "ghp_secret"} {
		if strings.Contains(joined, value) {
			t.Errorf("secret value %q reached the child environment", value)
		}
	}
}

// Infrastructure a process needs to work at all is not a credential. A child
// without a TLS trust store fails every HTTPS call with what looks like a
// network fault, and one without a proxy setting cannot reach the network in
// the deployments that have one.
func TestOperationalVariablesStillReachTheChild(t *testing.T) {
	parent := []string{
		"PATH=/usr/bin", "HOME=/home/gateway", "LANG=en_US.UTF-8", "TZ=UTC",
		"TMPDIR=/tmp", "SSL_CERT_FILE=/etc/ssl/cert.pem",
		"HTTPS_PROXY=http://proxy:3128", "NO_PROXY=localhost",
		"NODE_PATH=/usr/lib/node_modules", "PYTHONPATH=/opt/py",
	}
	env, withheld := ProcessEnv(ServerConfig{}, parent)
	if withheld != 0 {
		t.Fatalf("withheld %d operational variables", withheld)
	}
	got := envMap(env)
	for _, name := range []string{"PATH", "HOME", "LANG", "TZ", "TMPDIR", "SSL_CERT_FILE", "HTTPS_PROXY", "NO_PROXY", "NODE_PATH", "PYTHONPATH"} {
		if _, ok := got[name]; !ok {
			t.Errorf("%s did not reach the child", name)
		}
	}
}

// An operator can grant a specific variable, one at a time. Granting is the
// documented fix when a server genuinely needs something.
func TestInheritEnvGrantsExactlyWhatItNames(t *testing.T) {
	parent := []string{"PATH=/usr/bin", "GITHUB_TOKEN=ghp_secret", "GITLAB_TOKEN=glpat_secret"}
	env, _ := ProcessEnv(ServerConfig{InheritEnv: []string{"GITHUB_TOKEN"}}, parent)
	got := envMap(env)
	if got["GITHUB_TOKEN"] != "ghp_secret" {
		t.Fatal("an explicitly granted variable did not reach the child")
	}
	if _, leaked := got["GITLAB_TOKEN"]; leaked {
		t.Fatal("granting one variable granted another")
	}
}

// A per-server Env value wins over anything inherited: the operator who set it
// meant that value, not the gateway's.
func TestExplicitEnvOverridesTheInheritedValue(t *testing.T) {
	parent := []string{"PATH=/usr/bin", "HOME=/home/gateway"}
	env, _ := ProcessEnv(ServerConfig{
		Env:        map[string]string{"HOME": "/srv/sandbox"},
		InheritEnv: []string{"HOME"},
	}, parent)
	got := envMap(env)
	if got["HOME"] != "/srv/sandbox" {
		t.Fatalf("HOME = %q, want the explicitly configured value", got["HOME"])
	}
	count := 0
	for _, entry := range env {
		if strings.HasPrefix(entry, "HOME=") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("HOME appears %d times; a duplicated variable leaves the effective value to the child's parser", count)
	}
}

// The escape hatch exists and is exact: everything passes, and nothing is
// reported as withheld, so the log does not claim a protection that is off.
func TestInheritAllIsAnExplicitOptOut(t *testing.T) {
	parent := []string{"PATH=/usr/bin", "ANTHROPIC_API_KEY=sk-ant-secret"}
	env, withheld := ProcessEnv(ServerConfig{InheritAll: true}, parent)
	if withheld != 0 {
		t.Errorf("withheld = %d under InheritAll", withheld)
	}
	if envMap(env)["ANTHROPIC_API_KEY"] != "sk-ant-secret" {
		t.Error("InheritAll did not pass the full environment")
	}
}
