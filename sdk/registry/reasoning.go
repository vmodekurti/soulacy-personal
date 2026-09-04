package registry

// Reasoning-strategy registry (Story E15). Follows the E10 factory pattern:
// drivers self-register from init(), hosts resolve by the agent's
// reasoning.strategy key and fall back to built-in wiring on unknown names.

import "github.com/soulacy/soulacy/sdk/reasoning"

// ReasoningStrategyFactory builds a reasoning.Strategy from a schemaless
// config map (strategy-specific tuning; may be nil). Per-run inputs (LLM
// backend, tool executor, loop config) arrive later via reasoning.Env —
// factories construct stateless loop logic, not wired instances.
type ReasoningStrategyFactory func(cfg map[string]any) (reasoning.Strategy, error)

var reasoningStrategies registry[ReasoningStrategyFactory]

// RegisterReasoningStrategy registers a reasoning loop factory under name.
// Duplicate names and nil factories error (call from init(); treat a
// non-nil error as a programmer mistake).
func RegisterReasoningStrategy(name string, f ReasoningStrategyFactory) error {
	return reasoningStrategies.register("reasoning strategy", name, f, f == nil)
}

// MustRegisterReasoningStrategy is RegisterReasoningStrategy that panics on
// error — the idiomatic form inside driver init() functions.
func MustRegisterReasoningStrategy(name string, f ReasoningStrategyFactory) {
	if err := RegisterReasoningStrategy(name, f); err != nil {
		panic(err)
	}
}

// NewReasoningStrategy instantiates the named strategy ("" or unknown names
// report ok=false so hosts can fall back to built-in wiring).
func NewReasoningStrategy(name string, cfg map[string]any) (reasoning.Strategy, bool, error) {
	f, ok := reasoningStrategies.lookup(name)
	if !ok {
		return nil, false, nil
	}
	s, err := f(cfg)
	return s, true, err
}

// ReasoningStrategies lists registered strategy names (sorted).
func ReasoningStrategies() []string { return reasoningStrategies.names() }
