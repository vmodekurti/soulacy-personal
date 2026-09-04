// anthropic_backend.go — LLMBackend implementation for the Anthropic Claude API.
//
// All three methods (Think, Plan, Reflect) make POST /v1/messages calls and
// require the LLM to respond with structured JSON. Markdown fences are stripped
// before unmarshalling. The caller (Loop) is responsible for retrying on
// transient errors — this backend makes exactly one attempt per call.
//
// Think() is the hot path: one call per ReAct step. Keep latency low by using
// a small model (e.g. claude-haiku-4-5-20251001) for Think and a larger model
// for Reflect (higher max_tokens, better synthesis).
//
// Reflect() is where the final answer is produced. It is called once per task
// and should use max_tokens >= 2048 to support rich structured output formats.
package reasoning

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const defaultAnthropicBase = "https://api.anthropic.com"
const defaultAnthropicVersion = "2023-06-01"

// AnthropicBackend implements LLMBackend using the Anthropic Messages API.
type AnthropicBackend struct {
	baseURL string
	apiKey  string
	// ThinkModel is used for Think() — default claude-haiku-4-5-20251001.
	ThinkModel string
	// PlanModel is used for Plan() — default claude-sonnet-4-6.
	PlanModel string
	// ReflectModel is used for Reflect() — default claude-sonnet-4-6.
	ReflectModel  string
	client        *http.Client
	ThinkParams   PhaseParams
	PlanParams    PhaseParams
	ReflectParams PhaseParams
}

// NewAnthropicBackend creates an AnthropicBackend.
// If baseURL is empty, the default api.anthropic.com is used.
func NewAnthropicBackend(baseURL, apiKey string) *AnthropicBackend {
	if baseURL == "" {
		baseURL = defaultAnthropicBase
	}
	return &AnthropicBackend{
		baseURL:      strings.TrimRight(baseURL, "/"),
		apiKey:       apiKey,
		ThinkModel:   "claude-haiku-4-5-20251001",
		PlanModel:    "claude-sonnet-4-6",
		ReflectModel: "claude-sonnet-4-6",
		client:       &http.Client{Timeout: 60 * time.Second},
	}
}

// SetClient replaces the internal HTTP client. Intended for testing only
// (inject a fake transport without starting a real server).
func (b *AnthropicBackend) SetClient(c *http.Client) { b.client = c }

// ─── Think ────────────────────────────────────────────────────────────────────

// Think runs one ReAct step. The LLM receives the task, step history, and
// available tools; it must respond with a ThinkResponse JSON object.
func (b *AnthropicBackend) Think(ctx context.Context, req ThinkRequest) (ThinkResponse, error) {
	systemPrompt := req.SystemPrompt
	if len(req.ToolNames) > 0 {
		systemPrompt += fmt.Sprintf("\n\nAvailable tools: %s", strings.Join(req.ToolNames, ", "))
	}
	systemPrompt += thinkInstructions

	userContent := fmt.Sprintf("Task: %s\n\n%s",
		req.TaskInput,
		formatStepHistory(req.StepHistory),
	)

	body, err := b.complete(ctx, b.ThinkModel, systemPrompt, userContent, phaseParamsWithDefaults(b.ThinkParams, 1024, 0.1, "json"))
	if err != nil {
		return ThinkResponse{}, err
	}

	var resp ThinkResponse
	if err := unmarshalJSON(body, &resp); err != nil {
		if recovered, ok := recoverThinkResponseFromRaw(body, req.ToolNames); ok {
			return recovered, nil
		}
		return ThinkResponse{}, fmt.Errorf("reasoning: Think: parse response: %w (raw: %s)", err, truncate(body, 200))
	}
	return resp, nil
}

const thinkInstructions = `

Respond ONLY with a JSON object in this exact schema — no markdown fences, no prose:
{
  "thought":      "your reasoning about the next step",
  "is_done":      false,
  "action":       { "tool": "<tool_name>", "input": { "<key>": "<value>" } },
  "final_answer": ""
}
Keep "thought" under 25 words. Keep tool arguments concise; do not paste fetched documents, HTML, or long code into thought.
Use only one of the exact Available tools as action.tool. If a tool result reports a missing argument, retry the same tool once with corrected concise arguments.
After a successful observation, either call the next needed tool or finish with the actual answer; do not repeat the same tool unless new input is required.
When the next step requires a tool, set "is_done": false and put the tool in "action"; never write tool_name({...}) as prose.
Only set "is_done": true when the user's request is actually complete. Do not use final_answer for progress notes such as "proceeding", "starting", or "next I will".
When the task is complete set "is_done": true, put your completed answer in "final_answer", and omit "action".`

// ─── Plan ─────────────────────────────────────────────────────────────────────

// Plan decomposes taskInput into an ordered list of PlannedStep objects.
// Only called by StrategyPlanExecute agents.
func (b *AnthropicBackend) Plan(ctx context.Context, systemPrompt, taskInput string, maxSteps int) (Plan, error) {
	sp := systemPrompt + fmt.Sprintf(planInstructions, maxSteps)
	body, err := b.complete(ctx, b.PlanModel, sp, "Task: "+taskInput, phaseParamsWithDefaults(b.PlanParams, 1024, 0.1, "json"))
	if err != nil {
		return Plan{}, err
	}

	var plan Plan
	if err := unmarshalJSON(body, &plan); err != nil {
		return Plan{}, fmt.Errorf("reasoning: Plan: parse response: %w (raw: %s)", err, truncate(body, 200))
	}
	return plan, nil
}

const planInstructions = `

Decompose the task into at most %d ordered steps. Respond ONLY with a JSON object:
{
  "goal": "one-sentence goal",
  "steps": [
    { "id": "step-1", "description": "...", "tool": "<tool_name>", "arguments": { "<arg>": "<value>" }, "depends_on": [] },
    { "id": "step-2", "description": "...", "tool": "<tool_name>", "arguments": { "<arg>": "<value>" }, "depends_on": ["step-1"] }
  ]
}
No markdown fences. List steps in execution order. Use only exact Available tools when listed. Put concise JSON tool arguments in "arguments"; use "{{step-id.output}}" style placeholders when a later step needs an earlier result. Use depends_on to express dependencies.`

// ─── Reflect ──────────────────────────────────────────────────────────────────

// Reflect synthesises the full step trace into the final answer.
// This is the only call where max_tokens is high (2048).
func (b *AnthropicBackend) Reflect(ctx context.Context, req ReflectRequest) (ReflectResponse, error) {
	sp := req.SystemPrompt
	if req.OutputFormat != "" {
		sp += fmt.Sprintf("\n\nOutput format: %s", req.OutputFormat)
	}
	sp += reflectInstructions

	userContent := fmt.Sprintf(
		"Task: %s\n\nStep trace:\n%s\n\nNow produce the final answer.",
		req.TaskInput,
		formatStepHistory(req.Steps),
	)

	body, err := b.complete(ctx, b.ReflectModel, sp, userContent, phaseParamsWithDefaults(b.ReflectParams, 2048, 0.1, "json"))
	if err != nil {
		return ReflectResponse{}, err
	}

	var resp ReflectResponse
	if err := unmarshalJSON(body, &resp); err != nil {
		// Fallback: treat the entire body as the output string.
		return ReflectResponse{Output: body}, nil
	}
	return resp, nil
}

const reflectInstructions = `

Synthesise the task result from the step trace. Respond ONLY with a JSON object:
{
  "output":        "the final answer in the requested output format",
  "updated_rules": "optional: revised operating rules in Markdown (only if you learned something worth remembering)"
}
No markdown fences around the JSON itself. The "output" field may contain Markdown.`

// ─── HTTP ─────────────────────────────────────────────────────────────────────

type anthropicRequest struct {
	Model       string             `json:"model"`
	MaxTokens   int                `json:"max_tokens"`
	System      string             `json:"system"`
	Messages    []anthropicMessage `json:"messages"`
	Temperature float64            `json:"temperature,omitempty"`
	TopP        float64            `json:"top_p,omitempty"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// complete makes one POST /v1/messages call and returns the text content.
func (b *AnthropicBackend) complete(ctx context.Context, model, system, userMsg string, params PhaseParams) (string, error) {
	payload := anthropicRequest{
		Model:     model,
		MaxTokens: params.MaxTokens,
		System:    system,
		Messages:  []anthropicMessage{{Role: "user", Content: userMsg}},
	}
	if params.Temperature > 0 {
		payload.Temperature = params.Temperature
	} else if params.TopP > 0 {
		payload.TopP = params.TopP
	}
	if anthropicReasoningModelDisallowsSampling(model) {
		payload.Temperature = 0
		payload.TopP = 0
	}

	var ar anthropicResponse
	var raw []byte
	var status int
	strippedDeprecated := map[string]bool{}
	for {
		var err error
		raw, status, err = b.doComplete(ctx, payload)
		if err != nil {
			return "", err
		}

		ar = anthropicResponse{}
		if err := json.Unmarshal(raw, &ar); err != nil {
			return "", fmt.Errorf("reasoning: anthropic: decode: %w (status %d)", err, status)
		}
		if ar.Error == nil {
			break
		}
		param := anthropicDeprecatedParam(ar.Error.Message)
		if param == "" || strippedDeprecated[param] {
			return "", fmt.Errorf("reasoning: anthropic: API error %s: %s", ar.Error.Type, ar.Error.Message)
		}
		switch param {
		case "temperature":
			payload.Temperature = 0
		case "top_p":
			if payload.TopP > 0 {
				payload.TopP = 0
			} else if payload.Temperature > 0 {
				payload.Temperature = 0
			}
		}
		strippedDeprecated[param] = true
	}
	if status >= 300 {
		return "", fmt.Errorf("reasoning: anthropic: HTTP %d: %s", status, truncate(string(raw), 200))
	}
	for _, block := range ar.Content {
		if block.Type == "text" {
			return block.Text, nil
		}
	}
	return "", fmt.Errorf("reasoning: anthropic: no text block in response")
}

func (b *AnthropicBackend) doComplete(ctx context.Context, payload anthropicRequest) ([]byte, int, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, 0, fmt.Errorf("reasoning: anthropic: marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		b.baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, 0, fmt.Errorf("reasoning: anthropic: new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", b.apiKey)
	req.Header.Set("anthropic-version", defaultAnthropicVersion)

	resp, err := b.client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("reasoning: anthropic: http: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("reasoning: anthropic: read body: %w", err)
	}
	return raw, resp.StatusCode, nil
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

// formatStepHistory renders the step trace as thought/action/observation triplets.
// It deduplicates consecutive identical thoughts to prevent prompt context repetition loops.
func formatStepHistory(steps []Step) string {
	if len(steps) == 0 {
		return "(no steps yet)"
	}
	var sb strings.Builder
	var lastThought string
	for _, s := range steps {
		trimmedThought := strings.TrimSpace(s.Thought)
		if trimmedThought != "" && trimmedThought != lastThought {
			sb.WriteString(fmt.Sprintf("Thought: %s\n", trimmedThought))
			lastThought = trimmedThought
		}
		if s.Action.Tool != "" {
			sb.WriteString(fmt.Sprintf("Action: %s(%s)\n", s.Action.Tool, formatActionArgsForHistory(s.Action)))
		}
		obs := strings.TrimSpace(s.Obs.Content)
		if obs == "" && s.Obs.Error != nil {
			obs = s.Obs.Error.Error()
		}
		sb.WriteString(fmt.Sprintf("Observation: %s\n\n", obs))
	}
	return sb.String()
}

func formatActionArgsForHistory(call ToolCall) string {
	args := call.Arguments
	if len(args) == 0 && len(call.Input) > 0 {
		args = make(map[string]any, len(call.Input))
		for k, v := range call.Input {
			args[k] = v
		}
	}
	if len(args) == 0 {
		return "{}"
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return "{}"
	}
	return string(raw)
}

// unmarshalJSON strips markdown fences then unmarshals.
func unmarshalJSON(s string, v any) error {
	s = strings.TrimSpace(s)
	// Strip ```json ... ``` or ``` ... ``` fences
	if strings.HasPrefix(s, "```") {
		if i := strings.Index(s, "\n"); i >= 0 {
			s = s[i+1:]
		}
		if j := strings.LastIndex(s, "```"); j >= 0 {
			s = s[:j]
		}
		s = strings.TrimSpace(s)
	}
	return json.Unmarshal([]byte(s), v)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

func anthropicDeprecatedParam(message string) string {
	msg := strings.ToLower(message)
	if !(strings.Contains(msg, "deprecated") ||
		strings.Contains(msg, "not supported") ||
		strings.Contains(msg, "cannot both be specified") ||
		strings.Contains(msg, "please use only one")) {
		return ""
	}
	if strings.Contains(msg, "cannot both be specified") || strings.Contains(msg, "please use only one") {
		return "top_p"
	}
	for _, name := range []string{"temperature", "top_p"} {
		if strings.Contains(msg, name) {
			return name
		}
	}
	return ""
}

func anthropicReasoningModelDisallowsSampling(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "" {
		return false
	}
	for _, marker := range []string{
		"claude-sonnet-5",
		"claude-opus-5",
		"claude-haiku-5",
		"claude-5",
	} {
		if strings.Contains(m, marker) {
			return true
		}
	}
	return false
}
