package agent_test

import (
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/soulacy/soulacy/pkg/agent"
)

func TestOutcomeContractRoundTrip(t *testing.T) {
	src := `
id: podcast
name: Podcast
outcome:
  goal: deliver a daily AI podcast brief
  enforce: fail
  assertions:
    - target: add_source_pack
      op: count_gte
      value: "3"
      describe: three sources were added
    - target: deliver_audio_status
      op: delivered
      value: telegram
`
	var def agent.Definition
	if err := yaml.Unmarshal([]byte(src), &def); err != nil {
		t.Fatal(err)
	}
	if !def.Outcome.HasAssertions() || len(def.Outcome.Assertions) != 2 {
		t.Fatalf("assertions did not load: %+v", def.Outcome)
	}
	if def.Outcome.EnforcementMode() != agent.EnforceFail {
		t.Errorf("enforce not read: %q", def.Outcome.Enforce)
	}
	if def.Outcome.Assertions[0].Describe != "three sources were added" {
		t.Errorf("describe not read: %+v", def.Outcome.Assertions[0])
	}
	// Re-marshal and reload: the contract must survive a save cycle.
	out, err := yaml.Marshal(&def)
	if err != nil {
		t.Fatal(err)
	}
	var again agent.Definition
	if err := yaml.Unmarshal(out, &again); err != nil {
		t.Fatal(err)
	}
	if len(again.Outcome.Assertions) != 2 || again.Outcome.Assertions[1].Op != "delivered" {
		t.Fatalf("contract did not survive round-trip: %+v", again.Outcome)
	}
	// A clone must deep-copy the assertions.
	clone := def.Clone()
	clone.Outcome.Assertions[0].Value = "99"
	if def.Outcome.Assertions[0].Value != "3" {
		t.Error("Clone must deep-copy assertions")
	}
}
