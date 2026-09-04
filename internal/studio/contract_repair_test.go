package studio

import "testing"

func TestRepairContractStructure_ReplacesEmptyWorkflowWithSafeAgent(t *testing.T) {
	intent := "Every morning send an AI research digest to Telegram"
	d := Draft{Name: "Broken", Intent: intent, Trigger: Trigger{Type: "manual"}}
	contract := AssessContract(d, Catalog{Tools: []string{"web_search"}, Channels: []string{"telegram"}}, PreflightInput{})
	if contract.OK {
		t.Fatal("empty workflow should not pass contract")
	}
	changed := RepairContractStructure(&d, intent, Catalog{Tools: []string{"web_search"}, Channels: []string{"telegram"}}, nil, contract)
	if !changed {
		t.Fatal("expected structural repair to replace empty workflow")
	}
	if !d.IsAgent() || d.Strategy != "plan_execute" {
		t.Fatalf("expected repaired Plan-Execute agent, got strategy %q", d.Strategy)
	}
	if len(d.Flow.Nodes) != 0 {
		t.Fatalf("expected agent repair without workflow nodes")
	}
}
