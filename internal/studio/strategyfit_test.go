package studio

import (
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/pkg/message"
)

func TestStrategyFitThresholdAndPromptGuardrail(t *testing.T) {
	store := NewStrategyFitStore(filepath.Join(t.TempDir(), "fit.json"))
	for i := 0; i < 39; i++ {
		if err := store.Record("model-x", "plan_execute", true); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 11; i++ {
		if err := store.Record("model-x", "plan_execute", false); err != nil {
			t.Fatal(err)
		}
	}
	rows := store.ForModel("model-x")
	if len(rows) != 1 || !rows[0].Unreliable || rows[0].SuccessRate != 0.78 {
		t.Fatalf("unexpected fit: %+v", rows)
	}
	guard := UnreliableStrategiesPromptBlock("model-x", store.Unreliable("model-x"))
	if !strings.Contains(guard, "Do NOT recommend") || !strings.Contains(guard, "plan_execute") {
		t.Fatalf("missing guardrail: %q", guard)
	}
	prompt := BuildRefinePromptInstruction("build an analyst", Catalog{ActiveModel: "model-x", UnreliableStrategies: []string{"plan_execute"}})
	if !strings.Contains(prompt, "STRATEGY RELIABILITY GUARDRAIL") {
		t.Fatal("refinement prompt omitted strategy-fit evidence")
	}
}

func TestStrategyFitNeedsMinimumEvidence(t *testing.T) {
	store := NewStrategyFitStore(filepath.Join(t.TempDir(), "fit.json"))
	for i := 0; i < StrategyFitMinRuns-1; i++ {
		if err := store.Record("model-x", "react", false); err != nil {
			t.Fatal(err)
		}
	}
	if got := store.Unreliable("model-x"); len(got) != 0 {
		t.Fatalf("flagged before minimum evidence: %v", got)
	}
}

func TestStrategyFitCollectorRecordsOneTerminalOutcome(t *testing.T) {
	store := NewStrategyFitStore(filepath.Join(t.TempDir(), "fit.json"))
	collector := NewStrategyFitCollector(SingleStrategyFitStore(store), func(_, agentID string) (string, string, bool) {
		return "model-y", "react", agentID == "agent"
	})
	completed := func(run string, success bool) message.Event {
		return message.Event{Type: "run.completed", AgentID: "agent", SessionID: "shared", Payload: map[string]any{"run_id": run, "provider": "p", "model": "model-y", "strategy": "react", "success": success}}
	}
	collector.Observe(completed("ok", true))
	collector.Observe(completed("ok", true))
	collector.Observe(completed("bad", false))
	collector.Observe(completed("degraded", false))
	collector.Wait()
	rows := store.ForModel("model-y")
	if len(rows) != 1 || rows[0].Passes != 1 || rows[0].Failures != 2 {
		t.Fatalf("unexpected collected outcomes: %+v", rows)
	}
}

func TestStrategyFitSeparatesProvidersAndAdvisorEnforcesHistory(t *testing.T) {
	store := NewStrategyFitStore(filepath.Join(t.TempDir(), "fit.json"))
	for i := 0; i < StrategyFitMinRuns; i++ {
		_ = store.RecordProviderRun("provider-a", "shared-model", "plan_execute", "a-"+strconv.Itoa(i), false)
		_ = store.RecordProviderRun("provider-b", "shared-model", "plan_execute", "b-"+strconv.Itoa(i), true)
	}
	if got := store.UnreliableProvider("provider-a", "shared-model"); len(got) != 1 {
		t.Fatalf("provider-a=%v", got)
	}
	if got := store.UnreliableProvider("provider-b", "shared-model"); len(got) != 0 {
		t.Fatalf("provider-b=%v", got)
	}
	advice := AdviseStrategy("plan and execute a long report", Catalog{UnreliableStrategies: []string{"plan_execute"}}, "plan_execute", false)
	if advice.Mode != "auto" {
		t.Fatalf("unreliable strategy was retained: %+v", advice)
	}
}
