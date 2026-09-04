package registry

import (
	"context"
	"testing"

	"github.com/soulacy/soulacy/sdk/reasoning"
)

type fakeStrategy struct{ id string }

func (f fakeStrategy) Run(ctx context.Context, env reasoning.Env, taskInput string) ([]reasoning.Step, reasoning.ReflectResponse) {
	return nil, reasoning.ReflectResponse{Output: f.id + ":" + taskInput}
}

func TestRegisterReasoningStrategy(t *testing.T) {
	if err := RegisterReasoningStrategy("tree_of_thought_test", func(cfg map[string]any) (reasoning.Strategy, error) {
		return fakeStrategy{id: "tot"}, nil
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := RegisterReasoningStrategy("tree_of_thought_test", func(cfg map[string]any) (reasoning.Strategy, error) {
		return fakeStrategy{}, nil
	}); err == nil {
		t.Fatal("duplicate name must error")
	}
	if err := RegisterReasoningStrategy("", func(cfg map[string]any) (reasoning.Strategy, error) {
		return fakeStrategy{}, nil
	}); err == nil {
		t.Fatal("empty name must error")
	}
	if err := RegisterReasoningStrategy("nilfac_test", nil); err == nil {
		t.Fatal("nil factory must error")
	}
}

func TestNewReasoningStrategy(t *testing.T) {
	MustRegisterReasoningStrategy("swarm_test", func(cfg map[string]any) (reasoning.Strategy, error) {
		return fakeStrategy{id: "swarm"}, nil
	})
	s, ok, err := NewReasoningStrategy("swarm_test", nil)
	if !ok || err != nil {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	_, resp := s.Run(context.Background(), reasoning.Env{}, "task")
	if resp.Output != "swarm:task" {
		t.Fatalf("output = %q", resp.Output)
	}
	// unknown name: ok=false so hosts fall back to built-in wiring
	if _, ok, err := NewReasoningStrategy("never_registered", nil); ok || err != nil {
		t.Fatalf("unknown: ok=%v err=%v", ok, err)
	}
}

func TestReasoningStrategiesSorted(t *testing.T) {
	MustRegisterReasoningStrategy("zz_test_strat", func(cfg map[string]any) (reasoning.Strategy, error) {
		return fakeStrategy{}, nil
	})
	MustRegisterReasoningStrategy("aa_test_strat", func(cfg map[string]any) (reasoning.Strategy, error) {
		return fakeStrategy{}, nil
	})
	names := ReasoningStrategies()
	last := ""
	seenAA, seenZZ := false, false
	for _, n := range names {
		if n < last {
			t.Fatalf("names not sorted: %v", names)
		}
		last = n
		if n == "aa_test_strat" {
			seenAA = true
		}
		if n == "zz_test_strat" {
			seenZZ = true
		}
	}
	if !seenAA || !seenZZ {
		t.Fatalf("missing registered names in %v", names)
	}
}
