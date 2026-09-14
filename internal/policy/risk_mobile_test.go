package policy

import "testing"

func TestMobileToolsAreNotShellTools(t *testing.T) {
	if RiskTierOf("mobile.command_status") != RiskSafe || RiskTierOf("mobile.list_nodes") != RiskSafe {
		t.Fatalf("reading phone command status must be safe, got %v / %v", RiskTierOf("mobile.command_status"), RiskTierOf("mobile.list_nodes"))
	}
	if RiskTierOf("mobile.invoke") != RiskWrite {
		t.Fatalf("a device action is a write, got %v", RiskTierOf("mobile.invoke"))
	}
	if RiskTierOf("shell_exec") != RiskShellSystem {
		t.Fatal("shell tools unchanged")
	}
}
