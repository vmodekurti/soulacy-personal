// ollama_backend.go — LLMBackend implementation using the local Ollama API.
//
// This is the recommended backend for self-hosted Soulacy deployments. It
// uses Ollama's /api/chat endpoint with "format":"json" to force structured
// output without relying on any cloud API or credits.
//
// Recommended models (in order of preference):
//   - qwen2.5:72b     — best JSON adherence, strong reasoning
//   - gemma4:latest   — reliable tool calling, already used by financial-agent
//   - qwen2.5:7b      — fast, good for Think() hot path on weaker hardware
//
// All three methods (Think, Plan, Reflect) use the same model by default.
// Override ThinkModel for a lighter/faster model on the hot path.
//
// JSON contract:
// Ollama's "format":"json" guarantees the response is parseable JSON but does
// NOT guarantee specific fields. The system prompt must be explicit: the model
// is instructed to respond only with the required JSON schema, and field names
// are described inline. Both qwen2.5 and gemma4 reliably follow this when
// the instruction is clear.
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

const defaultOllamaBase = "http://localhost:11434"

// OllamaBackend implements LLMBackend using the local Ollama /api/chat endpoint.
type OllamaBackend struct {
	baseURL string
	// Model used for Think() — runs once per ReAct step (hot path).
	// Defaults to "gemma4:latest". Use a smaller/faster model here if latency matters.
	ThinkModel string
	// Model used for Plan() and Reflect(). Defaults to "qwen2.5:72b" for better
	// reasoning and JSON structure adherence on complex tasks.
	PlanReflectModel string
	client           *http.Client
	ThinkParams      PhaseParams
	PlanParams       PhaseParams
	ReflectParams    PhaseParams
}

// NewOllamaBackend creates an OllamaBackend pointed at baseURL.
// Pass "" to use the default localhost:11434.
func NewOllamaBackend(baseURL string) *OllamaBackend {
	if baseURL == "" {
		baseURL = defaultOllamaBase
	}
	return &OllamaBackend{
		baseURL:          strings.TrimRight(baseURL, "/"),
		ThinkModel:       "gemma4:latest",
		PlanReflectModel: "qwen2.5:72b",
		client:           &http.Client{Timeout: 120 * time.Second},
	}
}

// NewOllamaBackendSingleModel creates an OllamaBackend that uses the same
// model for Think, Plan, and Reflect. Useful when only one model is pulled.
func NewOllamaBackendSingleModel(baseURL, model string) *OllamaBackend {
	b := NewOllamaBackend(baseURL)
	b.ThinkModel = model
	b.PlanReflectModel = model
	return b
}

// SetClient replaces the internal HTTP client. Intended for testing only
// (inject a fake transport without starting a real server).
func (b *OllamaBackend) SetClient(c *http.Client) { b.client = c }

// ─── Think ────────────────────────────────────────────────────────────────────

// Think runs one ReAct step. The model must return a ThinkResponse JSON object.
func (b *OllamaBackend) Think(ctx context.Context, req ThinkRequest) (ThinkResponse, error) {
	system := req.SystemPrompt
	if len(req.ToolNames) > 0 {
		system += fmt.Sprintf("\n\nAvailable tools: %s", strings.Join(req.ToolNames, ", "))
	}
	system += ollamaThinkInstructions

	user := fmt.Sprintf("Task: %s\n\n%s", req.TaskInput, formatStepHistory(req.StepHistory))

	raw, err := b.chat(ctx, b.ThinkModel, system, user, phaseParamsWithDefaults(b.ThinkParams, 4096, 0.1, "json"))
	if err != nil {
		return ThinkResponse{}, fmt.Errorf("reasoning/ollama: Think: %w", err)
	}

	var resp ThinkResponse
	if err := unmarshalJSON(raw, &resp); err != nil {
		if recovered, ok := recoverThinkResponseFromRaw(raw, req.ToolNames); ok {
			return recovered, nil
		}
		return ThinkResponse{}, fmt.Errorf("reasoning/ollama: Think: parse %q: %w", truncate(raw, 120), err)
	}
	return resp, nil
}

const ollamaThinkInstructions = `

You must respond with ONLY a JSON object — no explanation, no markdown, no prose.
Required schema:
{"thought":"your reasoning","is_done":false,"action":{"tool":"tool_name","input":{"key":"value"}},"final_answer":""}
Keep "thought" under 25 words. Keep tool arguments concise; do not paste fetched documents, HTML, or long code into thought.
Use only one of the exact Available tools as action.tool. If a tool result reports a missing argument, retry the same tool once with corrected concise arguments.
After a successful observation, either call the next needed tool or finish with the actual answer; do not repeat the same tool unless new input is required.
When the next step requires a tool, set "is_done":false and put the tool in "action"; never write tool_name({...}) as prose.
Only set "is_done":true when the user's request is actually complete. Do not use final_answer for progress notes such as "proceeding", "starting", or "next I will".
When the task is complete: set "is_done":true, put your completed answer in "final_answer", omit "action".`

// ─── Plan ─────────────────────────────────────────────────────────────────────

// Plan decomposes the task into an ordered list of PlannedStep objects.
func (b *OllamaBackend) Plan(ctx context.Context, systemPrompt, taskInput string, maxSteps int) (Plan, error) {
	system := systemPrompt + fmt.Sprintf(ollamaPlanInstructions, maxSteps)
	raw, err := b.chat(ctx, b.PlanReflectModel, system, "Task: "+taskInput, phaseParamsWithDefaults(b.PlanParams, 1024, 0.1, "json"))
	if err != nil {
		return Plan{}, fmt.Errorf("reasoning/ollama: Plan: %w", err)
	}

	var plan Plan
	if err := unmarshalJSON(raw, &plan); err != nil {
		return Plan{}, fmt.Errorf("reasoning/ollama: Plan: parse %q: %w", truncate(raw, 120), err)
	}
	return plan, nil
}

const ollamaPlanInstructions = `

Respond with ONLY a JSON object — no explanation, no markdown. Max %d steps.
Required schema:
{"goal":"one-sentence goal","steps":[{"id":"step-1","description":"what to do","tool":"tool_name","arguments":{"arg":"value"},"depends_on":[]},{"id":"step-2","description":"...","tool":"tool_name","arguments":{"arg":"{{step-1.output}}"},"depends_on":["step-1"]}]}
Use only exact Available tools when listed. Put concise JSON tool arguments in "arguments".`

// ─── Reflect ──────────────────────────────────────────────────────────────────

// Reflect synthesises the full step trace into the final answer.
// Uses a higher token budget (2048) for rich structured output.
func (b *OllamaBackend) Reflect(ctx context.Context, req ReflectRequest) (ReflectResponse, error) {
	system := req.SystemPrompt
	if req.OutputFormat != "" {
		system += "\n\nOutput format: " + req.OutputFormat
	}
	system += ollamaReflectInstructions

	user := fmt.Sprintf("Task: %s\n\nStep trace:\n%s\n\nProduce the final answer.",
		req.TaskInput, formatStepHistory(req.Steps))

	raw, err := b.chat(ctx, b.PlanReflectModel, system, user, phaseParamsWithDefaults(b.ReflectParams, 4096, 0.1, "json"))
	if err != nil {
		return ReflectResponse{}, fmt.Errorf("reasoning/ollama: Reflect: %w", err)
	}

	var resp ReflectResponse
	if err := unmarshalJSON(raw, &resp); err != nil {
		// Fallback: treat the whole response as the output.
		return ReflectResponse{Output: raw}, nil
	}
	return resp, nil
}

const ollamaReflectInstructions = `

Respond with ONLY a JSON object — no markdown fences around it.
Required schema:
{"output":"your final answer here","updated_rules":"optional revised operating rules in Markdown, only if you learned something worth keeping — otherwise leave empty"}`

// ─── HTTP ─────────────────────────────────────────────────────────────────────

type ollamaChatRequest struct {
	Model    string              `json:"model"`
	Messages []ollamaChatMessage `json:"messages"`
	Stream   bool                `json:"stream"`
	Format   string              `json:"format"`
	Options  ollamaOptions       `json:"options"`
}

type ollamaChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ollamaOptions struct {
	Temperature float64 `json:"temperature"`
	TopP        float64 `json:"top_p,omitempty"`
	NumPredict  int     `json:"num_predict"`
}

type ollamaChatResponse struct {
	Message struct {
		Content string `json:"content"`
	} `json:"message"`
	Error string `json:"error,omitempty"`
}

// chat makes one POST /api/chat call with format:"json" and returns the content string.
func (b *OllamaBackend) chat(ctx context.Context, model, system, user string, params PhaseParams) (string, error) {
	format := strings.TrimSpace(params.ResponseFormat)
	if format == "" || format == "json_schema" {
		format = "json"
	}
	payload := ollamaChatRequest{
		Model: model,
		Messages: []ollamaChatMessage{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		},
		Stream: false,
		Format: format, // forces valid JSON output regardless of prose tendencies
		Options: ollamaOptions{
			Temperature: params.Temperature, // low temperature for reliable JSON structure
			TopP:        params.TopP,
			NumPredict:  params.MaxTokens,
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("reasoning/ollama: marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		b.baseURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("reasoning/ollama: new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := b.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("reasoning/ollama: http: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reasoning/ollama: read body: %w", err)
	}

	var or ollamaChatResponse
	if err := json.Unmarshal(raw, &or); err != nil {
		return "", fmt.Errorf("reasoning/ollama: decode (status %d): %w — body: %s",
			resp.StatusCode, err, truncate(string(raw), 200))
	}
	if or.Error != "" {
		return "", fmt.Errorf("reasoning/ollama: API error: %s", or.Error)
	}
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("reasoning/ollama: HTTP %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}
	return strings.TrimSpace(or.Message.Content), nil
}
