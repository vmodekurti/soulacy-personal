// openai_backend.go — LLMBackend implementation for OpenAI-compatible APIs.
//
// Works with any OpenAI-compatible endpoint:
//   - OpenAI            (https://api.openai.com)
//   - Groq              (https://api.groq.com/openai)
//   - Together AI       (https://api.together.xyz)
//   - vLLM / LM Studio (local OpenAI-compatible server)
//
// Uses response_format: {"type":"json_object"} to force structured JSON output,
// which is supported by all production OpenAI-compatible APIs.
//
// ⚠ Groq TPM note: reasoning loops are token-heavy. With 8 ReAct steps,
// each Think call ≈ 600 tokens → 8 steps ≈ 4800 tokens + Plan + Reflect ≈ 8000+.
// Groq's free tier (6000 TPM) will hit the limit mid-run. Use max_steps ≤ 4
// or upgrade to a paid Groq plan. The validator will warn about this.
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

// OpenAIBackend implements LLMBackend using any OpenAI-compatible chat API.
type OpenAIBackend struct {
	baseURL string
	apiKey  string
	// ThinkModel is used for Think() — the hot path. Defaults to the model
	// set at construction time, or gpt-4o-mini for low latency.
	ThinkModel string
	// PlanReflectModel is used for Plan() and Reflect(). Defaults to gpt-4o
	// for better reasoning on complex tasks.
	PlanReflectModel string
	client           *http.Client
	ThinkParams      PhaseParams
	PlanParams       PhaseParams
	ReflectParams    PhaseParams
}

// NewOpenAIBackend creates a backend for the OpenAI API.
func NewOpenAIBackend(apiKey, model string) *OpenAIBackend {
	return newOpenAICompatibleBackend("https://api.openai.com/v1", apiKey, model)
}

// NewOpenAICompatibleBackend creates a backend for any OpenAI-compatible endpoint.
// baseURL should include the path prefix, e.g. "https://api.groq.com/openai/v1".
func NewOpenAICompatibleBackend(baseURL, apiKey, model string) *OpenAIBackend {
	return newOpenAICompatibleBackend(baseURL, apiKey, model)
}

func newOpenAICompatibleBackend(baseURL, apiKey, model string) *OpenAIBackend {
	if model == "" {
		model = "gpt-4o"
	}
	return &OpenAIBackend{
		baseURL:          strings.TrimRight(baseURL, "/"),
		apiKey:           apiKey,
		ThinkModel:       model,
		PlanReflectModel: model,
		client:           &http.Client{Timeout: 120 * time.Second},
	}
}

// SetClient replaces the internal HTTP client. Intended for testing only
// (inject a fake transport without starting a real server).
func (b *OpenAIBackend) SetClient(c *http.Client) { b.client = c }

// ─── Think ────────────────────────────────────────────────────────────────────

func (b *OpenAIBackend) Think(ctx context.Context, req ThinkRequest) (ThinkResponse, error) {
	system := req.SystemPrompt
	if len(req.ToolNames) > 0 {
		system += fmt.Sprintf("\n\nAvailable tools: %s", strings.Join(req.ToolNames, ", "))
	}
	system += openaiThinkInstructions

	user := fmt.Sprintf("Task: %s\n\n%s", req.TaskInput, formatStepHistory(req.StepHistory))

	// 4096 default (was 1024): reasoning models spend output tokens on hidden
	// chain-of-thought before the JSON step, and a tight budget made them return
	// empty content ("parse \"\": unexpected end of JSON input"). Agents can still
	// override via reasoning.think.max_tokens.
	raw, err := b.chat(ctx, b.ThinkModel, system, user, phaseParamsWithDefaults(b.ThinkParams, 4096, 0.1, "json"))
	if err != nil {
		return ThinkResponse{}, fmt.Errorf("reasoning/openai: Think: %w", err)
	}
	var resp ThinkResponse
	if err := unmarshalJSON(raw, &resp); err != nil {
		if recovered, ok := recoverThinkResponseFromRaw(raw, req.ToolNames); ok {
			return recovered, nil
		}
		return ThinkResponse{}, fmt.Errorf("reasoning/openai: Think: parse %q: %w", truncate(raw, 120), err)
	}
	return resp, nil
}

const openaiThinkInstructions = `

Respond with ONLY a JSON object — no markdown, no explanation.
Schema: {"thought":"...","is_done":false,"action":{"tool":"name","input":{"key":"value"}},"final_answer":""}
Keep "thought" under 25 words. Keep tool arguments concise; do not paste fetched documents, HTML, or long code into thought.
Use only one of the exact Available tools as action.tool. If a tool result reports a missing argument, retry the same tool once with corrected concise arguments.
After a successful observation, either call the next needed tool or finish with the actual answer; do not repeat the same tool unless new input is required.
When the next step requires a tool, set "is_done":false and put the tool in "action"; never write tool_name({...}) as prose.
Only set "is_done":true when the user's request is actually complete. Do not use final_answer for progress notes such as "proceeding", "starting", or "next I will".
When done: set "is_done":true, put the completed answer in "final_answer", omit "action".`

// ─── Plan ─────────────────────────────────────────────────────────────────────

func (b *OpenAIBackend) Plan(ctx context.Context, systemPrompt, taskInput string, maxSteps int) (Plan, error) {
	system := systemPrompt + fmt.Sprintf(openaiPlanInstructions, maxSteps)
	raw, err := b.chat(ctx, b.PlanReflectModel, system, "Task: "+taskInput, phaseParamsWithDefaults(b.PlanParams, 1024, 0.1, "json"))
	if err != nil {
		return Plan{}, fmt.Errorf("reasoning/openai: Plan: %w", err)
	}
	var plan Plan
	if err := unmarshalJSON(raw, &plan); err != nil {
		return Plan{}, fmt.Errorf("reasoning/openai: Plan: parse %q: %w", truncate(raw, 120), err)
	}
	return plan, nil
}

const openaiPlanInstructions = `

Respond with ONLY a JSON object. Max %d steps.
Schema: {"goal":"...","steps":[{"id":"step-1","description":"...","tool":"name","arguments":{"arg":"value"},"depends_on":[]},...]}
Use only exact Available tools when listed. Put concise JSON tool arguments in "arguments"; use "{{step-id.output}}" placeholders for earlier results.`

// ─── Reflect ──────────────────────────────────────────────────────────────────

func (b *OpenAIBackend) Reflect(ctx context.Context, req ReflectRequest) (ReflectResponse, error) {
	system := req.SystemPrompt
	if req.OutputFormat != "" {
		system += "\n\nOutput format: " + req.OutputFormat
	}
	system += openaiReflectInstructions

	user := fmt.Sprintf("Task: %s\n\nStep trace:\n%s\n\nProduce the final answer.",
		req.TaskInput, formatStepHistory(req.Steps))

	raw, err := b.chat(ctx, b.PlanReflectModel, system, user, phaseParamsWithDefaults(b.ReflectParams, 4096, 0.1, "json"))
	if err != nil {
		return ReflectResponse{}, fmt.Errorf("reasoning/openai: Reflect: %w", err)
	}
	var resp ReflectResponse
	if err := unmarshalJSON(raw, &resp); err != nil {
		return ReflectResponse{Output: raw}, nil
	}
	return resp, nil
}

const openaiReflectInstructions = `

Respond with ONLY a JSON object.
Schema: {"output":"final answer here","updated_rules":"revised operating rules in Markdown, or empty"}`

// ─── HTTP ─────────────────────────────────────────────────────────────────────

type openaiChatRequest struct {
	Model          string              `json:"model"`
	Messages       []openaiChatMessage `json:"messages"`
	MaxTokens      int                 `json:"max_tokens,omitempty"`
	Temperature    float64             `json:"temperature"`
	TopP           float64             `json:"top_p,omitempty"`
	Stream         bool                `json:"stream"`
	ResponseFormat map[string]string   `json:"response_format"`
}

type openaiChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openaiChatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
			// ReasoningContent is where GLM / DeepSeek-style REASONING models put
			// their chain-of-thought. When such a model spends its whole token
			// budget thinking, `content` comes back empty while the intended JSON
			// often sits (or can be recovered) here — so we fall back to it rather
			// than treating the step as a blank failure.
			ReasoningContent string `json:"reasoning_content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error,omitempty"`
}

func (b *OpenAIBackend) chat(ctx context.Context, model, system, user string, params PhaseParams) (string, error) {
	payload := openaiChatRequest{
		Model: model,
		Messages: []openaiChatMessage{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		},
		MaxTokens:      params.MaxTokens,
		Temperature:    params.Temperature,
		TopP:           params.TopP,
		Stream:         false,
		ResponseFormat: map[string]string{"type": "json_object"},
	}
	if strings.TrimSpace(params.ResponseFormat) == "" {
		params.ResponseFormat = "json"
	}
	if params.ResponseFormat != "json" && params.ResponseFormat != "json_schema" {
		payload.ResponseFormat = nil
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("reasoning/openai: marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		b.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("reasoning/openai: new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if b.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+b.apiKey)
	}

	resp, err := b.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("reasoning/openai: http: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reasoning/openai: read body: %w", err)
	}

	var or openaiChatResponse
	if err := json.Unmarshal(raw, &or); err != nil {
		return "", fmt.Errorf("reasoning/openai: decode (status %d): %w", resp.StatusCode, err)
	}
	if or.Error != nil {
		return "", fmt.Errorf("reasoning/openai: API error [%s]: %s", or.Error.Type, or.Error.Message)
	}
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("reasoning/openai: HTTP %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}
	if len(or.Choices) == 0 {
		return "", fmt.Errorf("reasoning/openai: no choices in response")
	}
	choice := or.Choices[0]
	content := strings.TrimSpace(choice.Message.Content)
	if content == "" {
		// Reasoning-model fallback: the visible answer is empty, so try to recover
		// a JSON object the model left in its reasoning trace (chain-of-thought).
		if obj := firstJSONObject(choice.Message.ReasoningContent); obj != "" {
			return obj, nil
		}
		// Still nothing usable. If the model was cut off mid-thought (finish
		// reason "length"), say so — that's a token-budget problem the caller can
		// act on, not a mysterious empty parse.
		if strings.EqualFold(choice.FinishReason, "length") {
			return "", fmt.Errorf("reasoning/openai: model %q returned no content — it hit the token limit while reasoning (raise reasoning think/reflect max_tokens, or use a non-reasoning model)", model)
		}
		return "", fmt.Errorf("reasoning/openai: model %q returned empty content", model)
	}
	return content, nil
}
