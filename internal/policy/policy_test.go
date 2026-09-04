package policy

import "testing"

func TestEvaluate_DisabledAllowsEverything(t *testing.T) {
	if a, _ := Evaluate(Config{Enabled: false}, "shell_exec", map[string]any{"command": "rm -rf /"}); a != ActionAllow {
		t.Fatalf("disabled policy should allow, got %s", a)
	}
}

func TestEvaluate_ShellDefaultsToPrompt(t *testing.T) {
	a, reason := Evaluate(Config{Enabled: true}, "shell_exec", map[string]any{"command": "ls"})
	if a != ActionPrompt {
		t.Fatalf("shell default = %s, want prompt", a)
	}
	if reason == "" {
		t.Fatalf("expected a reason for prompt")
	}
}

func TestEvaluate_ShellDenyOverride(t *testing.T) {
	a, _ := Evaluate(Config{Enabled: true, Shell: "deny"}, "run_script", nil)
	if a != ActionDeny {
		t.Fatalf("shell deny = %s, want deny", a)
	}
}

func TestEvaluate_OtherToolsUngated(t *testing.T) {
	if a, _ := Evaluate(Config{Enabled: true, Shell: "deny"}, "kb_search", nil); a != ActionAllow {
		t.Fatalf("non-risk tool should be allowed, got %s", a)
	}
}

func TestEvaluate_NetworkDomainAllowList(t *testing.T) {
	cfg := Config{Enabled: true, AllowDomains: []string{"example.com"}}
	if a, _ := Evaluate(cfg, "http_request", map[string]any{"url": "https://api.example.com/x"}); a != ActionAllow {
		t.Fatalf("allowed subdomain should pass, got %s", a)
	}
	if a, reason := Evaluate(cfg, "http_request", map[string]any{"url": "https://evil.test/x"}); a != ActionDeny {
		t.Fatalf("off-allowlist host should deny, got %s (%s)", a, reason)
	}
}

func TestEvaluate_NetworkDomainDenyList(t *testing.T) {
	cfg := Config{Enabled: true, DenyDomains: []string{"tracker.io"}}
	if a, _ := Evaluate(cfg, "fetch_url", map[string]any{"url": "https://ads.tracker.io/pixel"}); a != ActionDeny {
		t.Fatalf("denylisted host should deny, got %s", a)
	}
	if a, _ := Evaluate(cfg, "fetch_url", map[string]any{"url": "https://good.example/x"}); a != ActionAllow {
		t.Fatalf("network default allow expected, got %s", a)
	}
}

func TestEvaluate_MCPTreatedAsNetwork(t *testing.T) {
	cfg := Config{Enabled: true, Network: "prompt"}
	if a, _ := Evaluate(cfg, "mcp__playwright__navigate", map[string]any{"url": "https://x.test"}); a != ActionPrompt {
		t.Fatalf("mcp tool should follow network policy, got %s", a)
	}
}

func TestEvaluate_FileDenyPath(t *testing.T) {
	cfg := Config{Enabled: true, File: "allow", DenyPaths: []string{"*.env", "/etc/*"}}
	if a, _ := Evaluate(cfg, "write_file", map[string]any{"path": "secrets.env"}); a != ActionDeny {
		t.Fatalf("*.env should be denied, got %s", a)
	}
	if a, _ := Evaluate(cfg, "write_file", map[string]any{"path": "/etc/passwd"}); a != ActionDeny {
		t.Fatalf("/etc/* should be denied, got %s", a)
	}
	if a, _ := Evaluate(cfg, "write_file", map[string]any{"path": "notes.txt"}); a != ActionAllow {
		t.Fatalf("ordinary file with File=allow should pass, got %s", a)
	}
}

func TestHostFromArgs_BareHost(t *testing.T) {
	if h := hostFromArgs(map[string]any{"endpoint": "internal-host:8080/path"}); h != "internal-host" {
		t.Fatalf("host = %q, want internal-host", h)
	}
}

func TestClassify(t *testing.T) {
	cases := map[string]Category{
		"shell_exec":   CategoryShell,
		"write_file":   CategoryFile,
		"http_request": CategoryNetwork,
		"mcp__x__y":    CategoryNetwork,
		"kb_search":    CategoryOther,
	}
	for tool, want := range cases {
		if got := Classify(tool); got != want {
			t.Fatalf("Classify(%q) = %s, want %s", tool, got, want)
		}
	}
}
