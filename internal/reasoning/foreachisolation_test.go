package reasoning

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sync"
	"testing"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

// The for_each path built each iteration's variables with a flat range-copy,
// while the parallel-branch path next to it used copyFlowVars — a real deep
// copy. A flat copy leaves every concurrent iteration ALIASING the same nested
// map[string]any and []any values.
//
// Nothing writes through those nested values today, which is why this never
// showed up: it is latent, not live. But RepairInput is handed that exact map,
// and the day something writes into a nested value the symptom is
// "fatal error: concurrent map writes" with a stack pointing nowhere near the
// cause. Isolation between concurrent iterations is the property; asserting it
// directly is cheaper than discovering it in production.
func TestForEach_IterationsDoNotShareNestedVariables(t *testing.T) {
	g, err := CompileFlow(sdkr.FlowSpec{
		Nodes: []sdkr.FlowNode{{
			ID:          "fan",
			Tool:        "t",
			ForEach:     `["a","b","c","d"]`,
			ItemVar:     "item",
			MaxParallel: 4,
			// {{ .missing }} cannot render, which is what routes each iteration
			// through RepairInput — the hook that receives the vars map.
			Input:  `{"q":"{{ .missing.deeper }}"}`,
			Output: "out",
		}},
	})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	// A nested value in the starting vars. If iterations share it, every
	// RepairInput call sees the same underlying map.
	shared := map[string]any{"config": map[string]any{"depth": 1}}

	var mu sync.Mutex
	seen := map[uintptr]int{}

	hooks := FlowHooks{
		RepairInput: func(_ context.Context, _ sdkr.FlowNode, _ string, _ error, vars map[string]any) (string, bool) {
			nested, ok := vars["config"].(map[string]any)
			if !ok {
				return `{"q":"x"}`, true
			}
			// Identity of the nested map, per iteration.
			ptr := reflect.ValueOf(nested).Pointer()
			mu.Lock()
			seen[ptr]++
			mu.Unlock()
			return `{"q":"x"}`, true
		},
	}

	run := func(_ context.Context, _ sdkr.FlowNode, input string) (json.RawMessage, error) {
		return json.RawMessage(fmt.Sprintf(`{"in":%q}`, input)), nil
	}

	if _, err := RunFlow(context.Background(), g, shared, run, hooks); err != nil {
		t.Fatalf("RunFlow: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seen) == 0 {
		t.Fatal("RepairInput never saw the nested variable; this test would prove nothing")
	}
	for ptr, count := range seen {
		if count > 1 {
			t.Fatalf("%d concurrent iterations were handed the SAME nested map (%v) — a write through it from any of them is a data race", count, ptr)
		}
	}
}
