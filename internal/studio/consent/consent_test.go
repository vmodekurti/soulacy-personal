package consent

import (
	"testing"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

const sysCode = "import subprocess\ndef run(i):\n    subprocess.run(['ls'])"

func pyNode(code string, c *sdkr.FlowConsent) sdkr.FlowNode {
	return sdkr.FlowNode{ID: "step", Kind: sdkr.FlowNodePython, Code: code, Consent: c}
}

func TestAuthorize_FailClosed(t *testing.T) {
	// ReadOnly always allowed, no stamp needed.
	if err := Authorize(pyNode("import json\ndef run(i):\n    return i", nil), false); err != nil {
		t.Fatalf("readonly should be allowed: %v", err)
	}

	// Beyond guardrails, no stamp -> refused.
	if err := Authorize(pyNode(sysCode, nil), true); err == nil {
		t.Fatal("system code without consent must be refused")
	}

	// Valid stamp + ceiling on -> allowed.
	good := &sdkr.FlowConsent{Hash: HashCode(sysCode), Capabilities: []string{"system"}, Scope: ScopeWorkflow}
	if err := Authorize(pyNode(sysCode, good), true); err != nil {
		t.Fatalf("granted system code should run: %v", err)
	}

	// Ceiling off -> refused even with consent.
	if err := Authorize(pyNode(sysCode, good), false); err == nil {
		t.Fatal("system code must be refused when allow_system_tools is off")
	}

	// Stale hash (code edited after consent) -> refused.
	stale := &sdkr.FlowConsent{Hash: "deadbeefdead", Capabilities: []string{"system"}, Scope: ScopeWorkflow}
	if err := Authorize(pyNode(sysCode, stale), true); err == nil {
		t.Fatal("edited code must void consent (hash mismatch)")
	}

	// Insufficient capability (granted network, code needs system) -> refused.
	wrongCap := &sdkr.FlowConsent{Hash: HashCode(sysCode), Capabilities: []string{"network"}, Scope: ScopeWorkflow}
	if err := Authorize(pyNode(sysCode, wrongCap), true); err == nil {
		t.Fatal("consent must cover the required capability")
	}
}

func TestApplyGrants(t *testing.T) {
	nodes := []sdkr.FlowNode{
		pyNode(sysCode, nil),
		{ID: "pure", Kind: sdkr.FlowNodePython, Code: "import json\ndef run(i):\n    return i"},
	}

	// Missing grant for the system node -> save refused.
	if err := ApplyGrants(nodes, nil); err == nil {
		t.Fatal("ungranted beyond-guardrail node must block save")
	}

	// With a matching grant -> stamped, and the runtime then authorizes it.
	err := ApplyGrants(nodes, []Grant{{NodeID: "step", Hash: HashCode(sysCode), Capabilities: []string{"system"}, Scope: ScopeWorkflow, GrantedBy: "u@example.com"}})
	if err != nil {
		t.Fatalf("ApplyGrants: %v", err)
	}
	if nodes[0].Consent == nil || nodes[0].Consent.GrantedBy != "u@example.com" {
		t.Fatalf("system node not stamped: %+v", nodes[0].Consent)
	}
	if nodes[1].Consent != nil {
		t.Fatal("readonly node must not carry a stamp")
	}
	if err := Authorize(nodes[0], true); err != nil {
		t.Fatalf("stamped node should authorize: %v", err)
	}
}
