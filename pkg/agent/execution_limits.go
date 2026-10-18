package agent

import (
	"strings"
	"time"
)

// Execution defaults are shared by direct, scheduled, Studio and reasoning runs.
// Token/call budgets and workspace ceilings are opt-in. A turn limit remains
// generous by default; global zero or per-agent -1 explicitly removes it.
const (
	DefaultMaxTurns        = 256
	DefaultMaxOutputTokens = 16384
	DefaultRunTimeout      = 48 * time.Hour
	DefaultLLMTimeout      = 5 * time.Minute
	DefaultStepTimeout     = 10 * time.Minute
	DefaultMaxPlanSteps    = 64
)

// ResolveTurns uses 0 for inheritance and -1 for an agent's unlimited request.
// A positive workspace ceiling always applies, including to unlimited requests.
func ResolveTurns(requested, inherited, ceiling int) int {
	turns := requested
	if turns == 0 {
		turns = inherited
	}
	if turns < 0 {
		turns = 0
	}
	if ceiling > 0 && (turns == 0 || turns > ceiling) {
		turns = ceiling
	}
	return turns
}

// ParseExecutionTimeout distinguishes an omitted duration from explicit zero.
// The timer-safe sentinel avoids treating zero as an immediate cancellation;
// parent cancellation/deadlines still apply.
func ParseExecutionTimeout(raw string, fallback time.Duration) time.Duration {
	if strings.TrimSpace(raw) == "" {
		return fallback
	}
	d, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil || d < 0 {
		return fallback
	}
	if d == 0 {
		return time.Duration(1<<63 - 1)
	}
	return d
}
