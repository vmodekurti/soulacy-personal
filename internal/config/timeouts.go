package config

import "time"

// TimeoutDurations is the parsed, ordered request timeout hierarchy.
type TimeoutDurations struct {
	Tool time.Duration
	LLM  time.Duration
	Step time.Duration
	Run  time.Duration
	HTTP time.Duration
}

// DefaultTimeoutHierarchy derives every outer deadline from the deadline it
// contains. Keeping the relationships in one function prevents independent
// defaults from drifting into an impossible request path.
func DefaultTimeoutHierarchy() TimeoutDurations {
	tool := 2 * time.Minute
	llm := tool + time.Minute
	step := llm + time.Minute
	run := step + 11*time.Minute
	httpRequest := run + time.Minute
	return TimeoutDurations{Tool: tool, LLM: llm, Step: step, Run: run, HTTP: httpRequest}
}
