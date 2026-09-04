package studio

import (
	"testing"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

// A workflow with a beyond-guardrail Custom Python node must require per-node
// code consent — independent of channels — carrying capabilities + a content
// hash (§13). A pure-data code node must NOT.
func TestPlan_CodeConsent(t *testing.T) {
	mk := func(code string) Draft {
		return Draft{
			Name:    "Coder",
			Trigger: Trigger{Type: "manual"},
			Flow: Flow{Entry: "step", Nodes: []sdkr.FlowNode{
				{ID: "step", Kind: sdkr.FlowNodePython, Code: code},
			}},
		}
	}

	// Beyond guardrails: subprocess.
	res, err := Plan(mk("import subprocess\ndef run(i):\n    subprocess.run(['ls'])"))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if !res.RequiresConsent {
		t.Fatal("a subprocess code node must require consent (no channel needed)")
	}
	var code *ConsentItem
	for i := range res.ConsentItems {
		if res.ConsentItems[i].Kind == "code" {
			code = &res.ConsentItems[i]
		}
	}
	if code == nil {
		t.Fatalf("expected a kind=code consent item, got %+v", res.ConsentItems)
	}
	if code.Name != "step" || code.Hash == "" || len(code.Capabilities) == 0 {
		t.Fatalf("code consent item incomplete: %+v", code)
	}

	// Editing the code must change the binding hash.
	res2, _ := Plan(mk("import subprocess\ndef run(i):\n    subprocess.run(['pwd'])"))
	var hash2 string
	for _, it := range res2.ConsentItems {
		if it.Kind == "code" {
			hash2 = it.Hash
		}
	}
	if hash2 == "" || hash2 == code.Hash {
		t.Fatalf("editing code must change the consent hash: %q vs %q", code.Hash, hash2)
	}

	// Pure data: no code consent.
	resPure, _ := Plan(mk("import json\ndef run(i):\n    return i"))
	for _, it := range resPure.ConsentItems {
		if it.Kind == "code" {
			t.Fatalf("pure-data code must not require consent, got %+v", it)
		}
	}
}
