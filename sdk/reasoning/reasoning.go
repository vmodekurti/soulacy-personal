// Package reasoning defines the public contracts for Soulacy reasoning
// loops (Story E15). Extension authors implement Strategy to add custom
// loop styles (Tree of Thought, Self-Reflection, Consensus Swarms, …) and
// register them with the SDK factory registry
// (registry.RegisterReasoningStrategy); agents select them by name via the
// reasoning.strategy key in SOUL.yaml.
//
// Compatibility: per the SDK policy (sdk/README.md) these interfaces are
// frozen within a major version — additive capability arrives via extension
// interfaces + type assertion, structs are append-only with zero-value
// compatibility, and the package never gains dependencies beyond stdlib.
package reasoning

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Strategy names shipped with the host. Custom strategies use their own
// names; "auto" is resolved by the host before strategy dispatch.
const (
	StrategyAuto        = "auto"
	StrategyReAct       = "react"
	StrategyPlanExecute = "plan_execute"
)

// ToolCall is a request from the LLM to invoke a tool.
type ToolCall struct {
	Tool string `json:"tool"`
	// Input is the legacy string-only argument map. It remains for SDK
	// compatibility with older strategies and tests.
	Input map[string]string `json:"input,omitempty"`
	// Arguments carries the full JSON argument object, including nested values.
	// Hosts should prefer Arguments when present and fall back to Input.
	Arguments map[string]any `json:"arguments,omitempty"`
}

// UnmarshalJSON accepts both the original string-only input contract and full
// JSON tool arguments. Models usually emit {"input":{...}}, while some newer
// prompts emit {"arguments":{...}}; both populate Arguments. The decoder also
// accepts common LLM aliases so ReAct agents do not burn loop steps on harmless
// schema drift.
func (c *ToolCall) UnmarshalJSON(data []byte) error {
	var raw struct {
		Tool        string         `json:"tool"`
		ToolName    string         `json:"tool_name"`
		Name        string         `json:"name"`
		Input       map[string]any `json:"input"`
		Arguments   map[string]any `json:"arguments"`
		Parameters  map[string]any `json:"parameters"`
		Params      map[string]any `json:"params"`
		ActionInput map[string]any `json:"action_input"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	c.Tool = firstNonEmpty(raw.Tool, raw.ToolName, raw.Name)
	args := firstNonNilMap(raw.Arguments, raw.Parameters, raw.Params, raw.Input, raw.ActionInput)
	if args == nil {
		args = map[string]any{}
	}
	if len(args) > 0 {
		c.Arguments = args
		c.Input = stringifyArgs(args)
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func firstNonNilMap(values ...map[string]any) map[string]any {
	for _, value := range values {
		if len(value) > 0 {
			return value
		}
	}
	return nil
}

// Observation is the result returned by ToolExecutor.Execute().
type Observation struct {
	// Content is the tool output, truncated to 8192 bytes before returning to
	// the LLM. Empty string is valid (e.g. write operations with no output).
	Content string `json:"content"`
	// Error, when non-nil, indicates the tool call failed. Loops wrap it
	// into Content as "tool error: <msg>" and continue — they do not abort.
	Error error `json:"-"`
	// Source is an optional provenance hint (URL, file path, tool name).
	Source string `json:"source,omitempty"`
}

// Step captures one full think-act-observe cycle.
type Step struct {
	ID       string        `json:"id"`
	Thought  string        `json:"thought"`
	Action   ToolCall      `json:"action"`
	Obs      Observation   `json:"observation"`
	Duration time.Duration `json:"duration_ms"`
}

// PlannedStep is one entry in a Plan produced by LLMBackend.Plan().
type PlannedStep struct {
	ID          string   `json:"id"`
	Description string   `json:"description"`
	Tool        string   `json:"tool"`
	DependsOn   []string `json:"depends_on,omitempty"`
	// Input is the legacy string-only argument map for the planned tool call.
	// It remains optional so older plans that only include a description still
	// execute with the historical {"task": description} fallback.
	Input map[string]string `json:"input,omitempty"`
	// Arguments carries the full JSON argument object for the planned tool call.
	// Hosts prefer Arguments, then Input, then the legacy task fallback.
	Arguments map[string]any `json:"arguments,omitempty"`
}

// UnmarshalJSON accepts common model aliases for planned-step tool arguments
// so Plan-Execute does not fail or silently fall back to vague task strings when
// a model emits "input", "params", "parameters", or "action_input".
func (s *PlannedStep) UnmarshalJSON(data []byte) error {
	var raw struct {
		ID          string         `json:"id"`
		Description string         `json:"description"`
		Tool        string         `json:"tool"`
		DependsOn   []string       `json:"depends_on"`
		Input       map[string]any `json:"input"`
		Arguments   map[string]any `json:"arguments"`
		Parameters  map[string]any `json:"parameters"`
		Params      map[string]any `json:"params"`
		ActionInput map[string]any `json:"action_input"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	s.ID = raw.ID
	s.Description = raw.Description
	s.Tool = raw.Tool
	s.DependsOn = raw.DependsOn
	s.Arguments = firstNonNilMap(raw.Arguments, raw.Input, raw.Parameters, raw.Params, raw.ActionInput)
	if len(s.Arguments) > 0 {
		s.Input = stringifyJSONArgs(s.Arguments)
	}
	return nil
}

func stringifyJSONArgs(args map[string]any) map[string]string {
	input := make(map[string]string, len(args))
	for k, v := range args {
		switch t := v.(type) {
		case string:
			input[k] = t
		case nil:
			input[k] = ""
		case bool, float64:
			input[k] = fmt.Sprint(t)
		default:
			b, err := json.Marshal(t)
			if err != nil {
				return nil
			}
			input[k] = string(b)
		}
	}
	return input
}

// Plan is the full decomposition produced by a plan_execute-style strategy.
type Plan struct {
	Goal  string        `json:"goal"`
	Steps []PlannedStep `json:"steps"`
}

// Config controls reasoning loop behaviour. Hosts populate it from the
// agent's SOUL.yaml reasoning block; strategies must respect the limits.
type Config struct {
	// Strategy selects the loop by registered name ("react", "plan_execute",
	// custom names). "auto" is resolved by the host before dispatch.
	Strategy string
	// MaxSteps is the hard ceiling for iterative strategies (default 8).
	MaxSteps int
	// MaxPlanSteps caps plan decomposition depth (default 6).
	MaxPlanSteps int
	// MaxParallelSteps caps how many planned steps plan_execute may execute
	// CONCURRENTLY within one dependency level (default 4). Set to 1 to force
	// the historical strictly-serial walk — the escape hatch for a custom
	// ToolExecutor that is not safe for concurrent calls (see ToolExecutor).
	MaxParallelSteps int
	// StepTimeout is the context deadline for each individual step (default 30s).
	StepTimeout time.Duration
	// TotalTimeout is the whole-task deadline (default 180s).
	TotalTimeout time.Duration
	// SystemPrompt is prepended to every LLM call.
	SystemPrompt string
	// ToolNames is the list of tool names available to this agent.
	ToolNames []string
	// OutputFormat hints Reflect() how to format the final answer
	// (e.g. "structured_markdown", "decision_brief", "plain").
	OutputFormat string
	// Flow carries the compiled graph for the "flow" strategy (Story E25).
	// nil for every other strategy. Appended field — zero-value compatible.
	Flow *FlowSpec
	// PhaseParams optionally tunes the LLM calls made by built-in reasoning
	// phases. Zero values keep the strategy defaults.
	ThinkParams   PhaseParams
	PlanParams    PhaseParams
	ReflectParams PhaseParams
}

// PhaseParams tunes one internal reasoning LLM phase.
type PhaseParams struct {
	Temperature    float64
	TopP           float64
	MaxTokens      int
	ResponseFormat string
}

// ThinkRequest is the input to LLMBackend.Think().
type ThinkRequest struct {
	TaskInput    string
	StepHistory  []Step
	SystemPrompt string
	ToolNames    []string
}

// ThinkResponse is the structured JSON the LLM must return for an iterative
// step. The backend must strip any markdown fences before unmarshalling.
type ThinkResponse struct {
	Thought     string   `json:"thought"`
	IsDone      bool     `json:"is_done"`
	Action      ToolCall `json:"action"`
	FinalAnswer string   `json:"final_answer,omitempty"`
}

// UnmarshalJSON accepts the canonical ReAct response shape plus common aliases
// produced by local and OpenAI-compatible models. This keeps harmless schema
// drift from becoming an "invalid reasoning step" in every ReAct loop.
func (r *ThinkResponse) UnmarshalJSON(data []byte) error {
	var raw struct {
		Thought     string          `json:"thought"`
		Reasoning   string          `json:"reasoning"`
		IsDone      *bool           `json:"is_done"`
		Done        *bool           `json:"done"`
		Final       *bool           `json:"final"`
		Action      json.RawMessage `json:"action"`
		Tool        string          `json:"tool"`
		ToolName    string          `json:"tool_name"`
		Name        string          `json:"name"`
		Input       map[string]any  `json:"input"`
		Arguments   map[string]any  `json:"arguments"`
		Parameters  map[string]any  `json:"parameters"`
		Params      map[string]any  `json:"params"`
		ActionInput map[string]any  `json:"action_input"`
		FinalAnswer string          `json:"final_answer"`
		Output      string          `json:"output"`
		Answer      string          `json:"answer"`
		Response    string          `json:"response"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	r.Thought = firstNonEmpty(raw.Thought, raw.Reasoning)
	r.IsDone = firstBool(false, raw.IsDone, raw.Done, raw.Final)
	r.FinalAnswer = firstNonEmpty(raw.FinalAnswer, raw.Output, raw.Answer, raw.Response)
	r.Action = ToolCall{}
	if len(raw.Action) > 0 && string(raw.Action) != "null" {
		if err := json.Unmarshal(raw.Action, &r.Action); err != nil {
			return err
		}
	}
	if strings.TrimSpace(r.Action.Tool) == "" {
		r.Action.Tool = firstNonEmpty(raw.Tool, raw.ToolName, raw.Name)
	}
	if len(r.Action.Arguments) == 0 {
		args := firstNonNilMap(raw.Arguments, raw.Parameters, raw.Params, raw.Input, raw.ActionInput)
		if len(args) > 0 {
			r.Action.Arguments = args
			r.Action.Input = stringifyArgs(args)
		}
	}
	return nil
}

func firstBool(fallback bool, values ...*bool) bool {
	for _, value := range values {
		if value != nil {
			return *value
		}
	}
	return fallback
}

func stringifyArgs(args map[string]any) map[string]string {
	if len(args) == 0 {
		return nil
	}
	out := make(map[string]string, len(args))
	for k, v := range args {
		switch t := v.(type) {
		case string:
			out[k] = t
		case nil:
			out[k] = ""
		case bool, float64:
			out[k] = fmt.Sprint(t)
		default:
			b, err := json.Marshal(t)
			if err != nil {
				out[k] = fmt.Sprint(t)
			} else {
				out[k] = string(b)
			}
		}
	}
	return out
}

// ReflectRequest is the input to LLMBackend.Reflect().
type ReflectRequest struct {
	TaskInput    string
	Steps        []Step
	SystemPrompt string
	OutputFormat string
}

// ReflectResponse is the JSON the LLM returns for a Reflect call.
// updated_rules is only populated when the agent has auto_update: true.
type ReflectResponse struct {
	Output       string `json:"output"`
	UpdatedRules string `json:"updated_rules,omitempty"`
}

// LLMBackend is the interface strategies use for all LLM calls.
//
//   - Think runs once per iterative step (hot path).
//   - Plan runs once at the start of a plan-style task.
//   - Reflect synthesises the final answer from the full step trace.
type LLMBackend interface {
	Think(ctx context.Context, req ThinkRequest) (ThinkResponse, error)
	Plan(ctx context.Context, systemPrompt, taskInput string, maxSteps int) (Plan, error)
	Reflect(ctx context.Context, req ReflectRequest) (ReflectResponse, error)
}

// ToolExecutor dispatches a ToolCall to the correct handler.
// Implementations must respect ctx.Done() and must not spawn subprocesses.
//
// Execute MUST be safe for concurrent use. plan_execute runs the steps within
// one dependency level in parallel (bounded by Config.MaxParallelSteps), so an
// executor that mutates unguarded shared state across calls will race. An
// executor that cannot meet this is accommodated by setting
// Config.MaxParallelSteps to 1, which restores the strictly-serial walk.
type ToolExecutor interface {
	Execute(ctx context.Context, call ToolCall) Observation
}

// Env is everything a strategy needs at execution time. The host owns
// construction of the backend and executor; strategies are pure loop logic.
type Env struct {
	Config Config
	LLM    LLMBackend
	Tools  ToolExecutor
}

// Strategy is a pluggable reasoning loop. Run must ALWAYS return a usable
// ReflectResponse — tool failures become observations, never panics or bare
// errors; degraded output beats no output. The returned steps are the
// ordered think-act-observe trace for observability surfaces.
type Strategy interface {
	Run(ctx context.Context, env Env, taskInput string) ([]Step, ReflectResponse)
}
