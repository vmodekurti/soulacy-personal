package autopilot

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/soulacy/soulacy/pkg/agent"
	"gopkg.in/yaml.v3"
)

func floatPtr(v float64) *float64 { return &v }

func TestEvaluateMissionDeterministicChecksAndLimits(t *testing.T) {
	allowed := []string{"web_search", "deliver"}
	contract := &agent.MissionContract{
		ID: "daily-brief",
		Acceptance: []agent.MissionCheck{
			{ID: "title", Type: agent.MissionCheckOutputContains, Value: "Daily Brief"},
			{ID: "secret", Type: agent.MissionCheckOutputNotContains, Value: "password="},
			{ID: "date", Type: agent.MissionCheckOutputRegex, Value: `2026-\d{2}-\d{2}`},
			{ID: "research", Type: agent.MissionCheckRequiredTool, Tool: "web_search"},
		},
		Limits: agent.MissionLimits{
			MaxCostUSD: floatPtr(0.25), MaxDuration: "2m", AllowedTools: &allowed,
		},
	}
	duration := 90 * time.Second
	cost := 0.20
	result := EvaluateMission(contract, Observation{
		Output:   "Daily Brief — 2026-09-12",
		Tools:    []ToolUse{{Name: "web_search", Status: "succeeded"}, {Name: "deliver", Status: "succeeded"}},
		Duration: &duration, CostUSD: &cost,
	})
	if result.Verification != CheckPass {
		t.Fatalf("verification = %q, checks = %+v", result.Verification, result.Checks)
	}
	if len(result.Checks) != 7 {
		t.Fatalf("checks = %d, want 7", len(result.Checks))
	}
	for _, check := range result.Checks {
		if check.Status != CheckPass {
			t.Errorf("check %s = %s (%s)", check.ID, check.Status, check.Detail)
		}
		if strings.Contains(check.Actual, "Daily Brief") {
			t.Errorf("check %s duplicated final output into receipt", check.ID)
		}
	}
}

func TestRequiredToolNeedsConfirmedSuccess(t *testing.T) {
	contract := &agent.MissionContract{Acceptance: []agent.MissionCheck{{
		ID: "send", Type: agent.MissionCheckRequiredTool, Tool: "deliver",
	}}}
	tests := []struct {
		name  string
		tools []ToolUse
		want  CheckStatus
	}{
		{name: "absent", want: CheckFail},
		{name: "failed", tools: []ToolUse{{Name: "deliver", Status: "failed"}}, want: CheckFail},
		{name: "simulated", tools: []ToolUse{{Name: "deliver", Status: "simulated"}}, want: CheckUnknown},
		{name: "unmeasured", tools: []ToolUse{{Name: "deliver"}}, want: CheckUnknown},
		{name: "failed then succeeded", tools: []ToolUse{{Name: "deliver", Status: "failed"}, {Name: "deliver", Status: "succeeded"}}, want: CheckPass},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := EvaluateMission(contract, Observation{Tools: tc.tools})
			if result.Verification != tc.want || len(result.Checks) != 1 || result.Checks[0].Status != tc.want {
				t.Fatalf("evaluation = %+v, want %s", result, tc.want)
			}
		})
	}
}

func TestMissionSOULYAMLRoundTripPreservesExplicitDenyAll(t *testing.T) {
	source := []byte(`
id: governed
name: Governed
mission:
  id: safe-run
  goal: produce a bounded answer
  limits:
    max_cost_usd: 0
    max_duration: 30s
    allowed_tools: []
  acceptance:
    - id: answer
      type: output_contains
      value: Answer
`)
	var definition agent.Definition
	if err := yaml.Unmarshal(source, &definition); err != nil {
		t.Fatal(err)
	}
	if definition.Mission == nil || definition.Mission.Limits.AllowedTools == nil ||
		len(*definition.Mission.Limits.AllowedTools) != 0 || definition.Mission.Limits.MaxCostUSD == nil {
		t.Fatalf("mission limits did not load: %+v", definition.Mission)
	}
	raw, err := yaml.Marshal(&definition)
	if err != nil {
		t.Fatal(err)
	}
	var again agent.Definition
	if err := yaml.Unmarshal(raw, &again); err != nil {
		t.Fatal(err)
	}
	if again.Mission == nil || again.Mission.Limits.AllowedTools == nil || len(*again.Mission.Limits.AllowedTools) != 0 {
		t.Fatalf("explicit deny-all was lost after round trip:\n%s", raw)
	}
}

func TestEvaluateMissionUnknownIsNotZeroAndClaimsNeverSelfPass(t *testing.T) {
	contract := &agent.MissionContract{Acceptance: []agent.MissionCheck{
		{ID: "cost", Type: agent.MissionCheckMaxCost, CostUSD: floatPtr(0)},
		{ID: "latency", Type: agent.MissionCheckMaxDuration, Duration: "1s"},
		{ID: "business", Type: agent.MissionCheckClaim, Claim: "customer received a refund"},
	}}
	result := EvaluateMission(contract, Observation{})
	if result.Verification != CheckUnknown {
		t.Fatalf("verification = %q, want unknown", result.Verification)
	}
	for _, check := range result.Checks {
		if check.Status != CheckUnknown {
			t.Errorf("check %s = %s, want unknown", check.ID, check.Status)
		}
	}
}

func TestEvaluateMissionFailDominatesUnknown(t *testing.T) {
	contract := &agent.MissionContract{Acceptance: []agent.MissionCheck{
		{Type: agent.MissionCheckClaim, Claim: "invoice was paid"},
		{Type: agent.MissionCheckOutputContains, Value: "receipt"},
	}}
	result := EvaluateMission(contract, Observation{Output: "nothing"})
	if result.Verification != CheckFail {
		t.Fatalf("verification = %q, want fail", result.Verification)
	}
}

func TestValidateMissionLimits(t *testing.T) {
	wildcard := []string{"*"}
	many := make([]string, 129)
	for i := range many {
		many[i] = "tool-" + string(rune(i+1000))
	}
	tests := []agent.MissionContract{
		{Limits: agent.MissionLimits{MaxCostUSD: floatPtr(math.NaN())}},
		{Limits: agent.MissionLimits{MaxDuration: "25h"}},
		{Limits: agent.MissionLimits{AllowedTools: &wildcard}},
		{Limits: agent.MissionLimits{AllowedTools: &many}},
		{Acceptance: []agent.MissionCheck{{Type: agent.MissionCheckOutputRegex, Value: "["}}},
	}
	for i := range tests {
		if err := ValidateMission(&tests[i]); err == nil {
			t.Errorf("case %d unexpectedly valid", i)
		}
	}
	empty := []string{}
	if err := ValidateMission(&agent.MissionContract{Limits: agent.MissionLimits{
		MaxCostUSD: floatPtr(0), MaxDuration: "1ms", AllowedTools: &empty,
	}}); err != nil {
		t.Fatalf("explicit deny-all/zero-cost limits should be valid: %v", err)
	}
}

func TestDefinitionCloneDeepCopiesMissionLimits(t *testing.T) {
	allowed := []string{"web_search"}
	cost := 1.0
	definition := &agent.Definition{Mission: &agent.MissionContract{
		Acceptance: []agent.MissionCheck{{Type: agent.MissionCheckMaxCost, CostUSD: &cost}},
		Limits:     agent.MissionLimits{MaxCostUSD: &cost, AllowedTools: &allowed},
	}}
	clone := definition.Clone()
	*clone.Mission.Acceptance[0].CostUSD = 2
	*clone.Mission.Limits.MaxCostUSD = 3
	(*clone.Mission.Limits.AllowedTools)[0] = "shell_exec"
	if *definition.Mission.Acceptance[0].CostUSD != 1 || *definition.Mission.Limits.MaxCostUSD != 1 ||
		(*definition.Mission.Limits.AllowedTools)[0] != "web_search" {
		t.Fatal("Definition.Clone shared mission pointer state")
	}
}
