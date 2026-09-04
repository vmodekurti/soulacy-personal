// Package reasoning implements agentic loop strategies for Soulacy agents:
//
//   - StrategyAuto        (recommended) — detects the right strategy automatically.
//     Simple questions finish in a single LLM call (zero overhead). Multi-step
//     tasks run ReAct. Planning tasks run Plan-Execute.
//   - StrategyReAct       — iterative Thought → Action → Observation cycles.
//   - StrategyPlanExecute — LLM decomposes up-front, then executes steps in order.
//   - any registered name — custom strategies plug in via the SDK registry
//     (registry.RegisterReasoningStrategy, Story E15) and are selected by the
//     agent's reasoning.strategy key in SOUL.yaml.
//
// Tool failures never crash the loop. Errors become observations and the LLM
// adapts; Reflect() always runs so there is always a final output.
//
// The contract types live in the SDK (sdk/reasoning, Story E15) so extension
// authors implement strategies without importing host internals; the names
// below are type ALIASES — identical types, zero conversion cost.
package reasoning

import (
	"context"
	"fmt"
	htmlpkg "html"
	"regexp"
	"strings"
	"time"

	sdkreasoning "github.com/soulacy/soulacy/sdk/reasoning"
	"github.com/soulacy/soulacy/sdk/registry"
)

// LoopStrategy selects the execution mode. Alias of string so registered
// custom strategy names are valid values.
type LoopStrategy = string

const (
	// StrategyAuto is the recommended default. It detects the right strategy
	// from the task text so users never need to configure react vs plan_execute.
	//
	// Detection rules (in priority order):
	//  1. Planning keywords ("plan", "decide", "strategy", "roadmap", "compare",
	//     "pros and cons", "should I") → StrategyPlanExecute
	//  2. Research/action keywords ("research", "find", "search", "look up",
	//     "what is", "how does", "latest") → StrategyReAct
	//  3. Simple task (answer fits in one turn, no tools needed) → single-call
	//     ReAct that returns IsDone=true on step 1 (zero tool overhead)
	StrategyAuto LoopStrategy = sdkreasoning.StrategyAuto
	// StrategyReAct runs interleaved think/act/observe cycles.
	StrategyReAct LoopStrategy = sdkreasoning.StrategyReAct
	// StrategyPlanExecute decomposes the task into a plan then executes it.
	StrategyPlanExecute LoopStrategy = sdkreasoning.StrategyPlanExecute
)

// Contract aliases — canonical definitions live in sdk/reasoning (E15).
type (
	ToolCall        = sdkreasoning.ToolCall
	Observation     = sdkreasoning.Observation
	Step            = sdkreasoning.Step
	PlannedStep     = sdkreasoning.PlannedStep
	Plan            = sdkreasoning.Plan
	LoopConfig      = sdkreasoning.Config
	PhaseParams     = sdkreasoning.PhaseParams
	ThinkRequest    = sdkreasoning.ThinkRequest
	ThinkResponse   = sdkreasoning.ThinkResponse
	ReflectRequest  = sdkreasoning.ReflectRequest
	ReflectResponse = sdkreasoning.ReflectResponse
	LLMBackend      = sdkreasoning.LLMBackend
	ToolExecutor    = sdkreasoning.ToolExecutor
	Strategy        = sdkreasoning.Strategy
	Env             = sdkreasoning.Env
)

// Result is the output of Loop.Run().
type Result struct {
	// Output is the final synthesised answer from LLMBackend.Reflect().
	Output string `json:"output"`
	// Steps is the ordered trace of every think-act-observe cycle.
	Steps []Step `json:"steps"`
	// Confident is false when any step recorded a tool error.
	Confident bool `json:"confident"`
	// Duration is the total wall-clock time for the run.
	Duration time.Duration `json:"duration"`
	// UpdatedRules carries Reflect's revised operating rules (Story E23).
	// Empty = the model learned nothing worth keeping. Hosts persist it
	// ONLY when the agent opted in via brain_memory.procedural.auto_update,
	// and always through the versioned rulebook.
	UpdatedRules string `json:"updated_rules,omitempty"`
}

// ─── Loop ─────────────────────────────────────────────────────────────────────

// Loop is the main reasoning engine. Create one per task run (or reuse across
// runs — it carries no mutable per-run state).
type Loop struct {
	cfg      LoopConfig
	llm      LLMBackend
	executor ToolExecutor
}

// New creates a Loop. Both llm and executor must be non-nil.
func New(cfg LoopConfig, llm LLMBackend, executor ToolExecutor) *Loop {
	if cfg.MaxSteps <= 0 {
		cfg.MaxSteps = 8
	}
	if cfg.MaxPlanSteps <= 0 {
		cfg.MaxPlanSteps = 6
	}
	if cfg.StepTimeout <= 0 {
		cfg.StepTimeout = 30 * time.Second
	}
	if cfg.TotalTimeout <= 0 {
		cfg.TotalTimeout = 180 * time.Second
	}
	return &Loop{cfg: cfg, llm: llm, executor: executor}
}

// Run executes the reasoning loop for the given task and returns a Result.
//
// Strategy dispatch goes through the SDK registry (Story E15): the built-in
// react/plan_execute strategies self-register from init() below, and any
// custom strategy registered under the agent's reasoning.strategy name is
// picked up the same way. Unknown names and factory errors fall back to
// ReAct — degraded output beats no output, matching the loop's tool-error
// philosophy.
//
// Run always returns a Result — even when tools fail or the LLM hits its step
// limit the strategy calls Reflect() to produce graceful degraded output.
func (l *Loop) Run(ctx context.Context, agentID, taskInput string) Result {
	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, l.cfg.TotalTimeout)
	defer cancel()

	name := l.cfg.Strategy
	if name == StrategyAuto || name == "" {
		name = detectStrategy(taskInput)
	}

	// A strategy name that resolves to nothing is a CONFIGURATION ERROR, not a
	// runtime condition to paper over. This used to silently substitute ReAct on
	// the reasoning that "degraded output beats no output" — but that trade only
	// holds for failures discovered mid-run. A misspelled or unregistered
	// strategy is knowable before any work happens, and substituting silently
	// meant an operator who wrote `strategy: plan-execute` (hyphen) got ReAct
	// forever, with the banner still naming their intended strategy and no
	// event anywhere saying otherwise.
	//
	// The loop's contract is that Run ALWAYS returns a Result, so this surfaces
	// as a failed, not-confident Result carrying the available names — not a
	// panic and not a silent substitution.
	strat, ok, err := registry.NewReasoningStrategy(name, nil)
	if !ok || err != nil || strat == nil {
		return Result{
			Output:    unknownStrategyMessage(name, err),
			Steps:     []Step{{ID: "config-error", Thought: "Strategy resolution failed before any step ran.", Obs: Observation{Content: unknownStrategyMessage(name, err), Source: "controller"}}},
			Confident: false,
			Duration:  time.Since(start),
		}
	}

	env := Env{Config: l.cfg, LLM: l.llm, Tools: l.executor}
	steps, reflectResp := strat.Run(ctx, env, taskInput)

	return Result{
		// Guard against the model leaking internal control JSON (thought/action/
		// is_done) as the answer — extract a real answer or fall back gracefully.
		Output:       SanitizeFinalOutput(reflectResp.Output, steps),
		Steps:        steps,
		Confident:    !containsToolErrors(steps),
		Duration:     time.Since(start),
		UpdatedRules: reflectResp.UpdatedRules,
	}
}

// unknownStrategyMessage explains an unresolvable reasoning.strategy and lists
// what IS registered, so the fix is visible without reading source. Registered
// names include any custom strategy the host added through the E15 registry,
// not just the built-ins — which is exactly the case where a typo is hardest to
// spot unaided.
func unknownStrategyMessage(name string, err error) string {
	var b strings.Builder
	b.WriteString("configuration error: reasoning.strategy ")
	if strings.TrimSpace(name) == "" {
		b.WriteString("is empty")
	} else {
		b.WriteString(fmt.Sprintf("%q is not registered", name))
	}
	if err != nil {
		b.WriteString(" (" + err.Error() + ")")
	}
	if names := registry.ReasoningStrategies(); len(names) > 0 {
		b.WriteString(". Available strategies: " + strings.Join(names, ", "))
	}
	b.WriteString(". Fix the strategy name in the agent's SOUL.yaml — this run did not execute.")
	return b.String()
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

// detectStrategy inspects the task text and returns the most appropriate loop
// strategy. Called when StrategyAuto is configured so the user never needs to
// choose between react and plan_execute manually.
//
// Heuristic (intentionally simple — wrong classification is harmless; the
// worst case is running react on a planning task which still produces a
// good answer via Reflect):
//   - Planning keywords  → StrategyPlanExecute
//   - Research keywords  → StrategyReAct
//   - Everything else    → StrategyReAct (IsDone on step 1 = zero overhead)
func detectStrategy(taskInput string) LoopStrategy {
	t := strings.ToLower(taskInput)
	planningKeywords := []string{
		"plan", "planning", "strategy", "strategic", "roadmap",
		"decide", "decision", "should i", "pros and cons", "trade-off", "tradeoff",
		"compare", "comparison", "recommend", "recommendation",
		"prioritise", "prioritize", "milestone",
	}
	for _, kw := range planningKeywords {
		if strings.Contains(t, kw) {
			return StrategyPlanExecute
		}
	}
	// Default to ReAct — if the LLM answers in one step (IsDone=true, no tool
	// call), the loop returns immediately with zero extra overhead.
	return StrategyReAct
}

var (
	htmlScriptRE = regexp.MustCompile(`(?is)<script\b[^>]*>.*?</script>|<style\b[^>]*>.*?</style>|<noscript\b[^>]*>.*?</noscript>|<svg\b[^>]*>.*?</svg>|<iframe\b[^>]*>.*?</iframe>`)
	htmlTagRE    = regexp.MustCompile(`(?s)<[^>]+>`)
	spaceRE      = regexp.MustCompile(`[ \t\r\n]+`)
)

// boundObservation enforces the 8192-byte cap and wraps tool errors as content.
func boundObservation(obs Observation) Observation {
	if obs.Error != nil {
		obs.Content = fmt.Sprintf("tool error: %s", obs.Error.Error())
	}
	obs.Content = compactObservation(obs.Content)
	if len(obs.Content) > 8192 {
		obs.Content = obs.Content[:8192]
	}
	// A 200-OK fetch that yielded a paywall teaser instead of an article is a
	// SUCCESS at the tool layer and a dead end at the task layer. Say so, before
	// the model burns its remaining steps re-fetching the same domain.
	// Applied after the cap so the steer is never the part that gets truncated.
	obs.Content = annotateThinContent(obs.Content)
	return obs
}

func compactObservation(content string) string {
	if !looksLikeHTMLObservation(content) {
		return content
	}
	header, body, ok := strings.Cut(content, "\n\n")
	if !ok {
		body = content
		header = ""
	}
	text := htmlScriptRE.ReplaceAllString(body, " ")
	text = htmlTagRE.ReplaceAllString(text, " ")
	text = htmlpkg.UnescapeString(text)
	text = strings.TrimSpace(spaceRE.ReplaceAllString(text, " "))
	if header != "" {
		header = strings.TrimSpace(header)
	}
	if text == "" {
		return content
	}
	if header == "" {
		return "HTML fetched; readable text excerpt:\n" + text
	}
	return header + "\n\nHTML fetched; readable text excerpt:\n" + text
}

func looksLikeHTMLObservation(content string) bool {
	s := strings.ToLower(content)
	if strings.Contains(s, "content-type: text/html") {
		return true
	}
	return strings.Contains(s, "<html") || strings.Contains(s, "<!doctype html")
}

func containsToolErrors(steps []Step) bool {
	for _, s := range steps {
		if s.Obs.Error != nil || strings.HasPrefix(s.Obs.Content, "tool error:") {
			return true
		}
		if s.Obs.Source == "controller" {
			return true
		}
	}
	return false
}

// AvailableToolNames returns the tools this loop was configured with.
func (l *Loop) AvailableToolNames() []string {
	return l.cfg.ToolNames
}
