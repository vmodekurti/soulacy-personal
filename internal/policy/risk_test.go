package policy

import "testing"

func TestRiskTierOf(t *testing.T) {
	cases := map[string]RiskTier{
		"read_file":       RiskSafe,
		"list_dir":        RiskSafe,
		"kb_search":       RiskSafe,
		"write_file":      RiskWrite,
		"kb_write":        RiskWrite,
		"queue_put":       RiskWrite,
		"fetch_url":       RiskNetwork,
		"http_request":    RiskNetwork,
		"web_search":      RiskNetwork,
		"download_file":   RiskNetwork,
		"channel.send":    RiskNetwork,
		"install_library": RiskPrivileged,
		"shell_exec":      RiskShellSystem,
		"run_script":      RiskShellSystem,
		"python_eval":     RiskShellSystem,
		// prefixes
		"mcp__github__create_issue": RiskNetwork,
		"plugin__stripe__charge":    RiskNetwork,
		// heuristics for unknown tools
		"exec_something":  RiskShellSystem,
		"install_thing":   RiskPrivileged,
		"post_to_webhook": RiskNetwork,
		"delete_record":   RiskWrite,
		"get_status":      RiskSafe,
	}
	for tool, want := range cases {
		if got := RiskTierOf(tool); got != want {
			t.Errorf("RiskTierOf(%q) = %s, want %s", tool, got, want)
		}
	}
}

func TestRiskTierStringStable(t *testing.T) {
	want := []string{"safe", "write", "network", "privileged", "shell_system"}
	for i, s := range want {
		if got := RiskTier(i).String(); got != s {
			t.Errorf("RiskTier(%d).String() = %q, want %q", i, got, s)
		}
	}
}

func TestHighRisk(t *testing.T) {
	if RiskSafe.HighRisk() || RiskWrite.HighRisk() || RiskNetwork.HighRisk() {
		t.Error("low/medium tiers should not be high-risk")
	}
	if !RiskPrivileged.HighRisk() || !RiskShellSystem.HighRisk() {
		t.Error("privileged and shell_system should be high-risk")
	}
}

func TestMaxRiskTier(t *testing.T) {
	if got := MaxRiskTier([]string{"read_file", "write_file", "fetch_url"}); got != RiskNetwork {
		t.Errorf("max = %s, want network", got)
	}
	if got := MaxRiskTier([]string{"read_file", "shell_exec"}); got != RiskShellSystem {
		t.Errorf("max = %s, want shell_system", got)
	}
	if got := MaxRiskTier(nil); got != RiskSafe {
		t.Errorf("empty max = %s, want safe", got)
	}
}
