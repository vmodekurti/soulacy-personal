// ollama.go — Ollama provider (default, local, air-gapped).
// Ollama exposes an OpenAI-compatible /api/chat endpoint.
// This adapter communicates with it directly over HTTP/JSON.
package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/soulacy/soulacy/pkg/message"
)

// OllamaProvider talks to a running Ollama instance.
type OllamaProvider struct {
	baseURL   string
	model     string
	keepAlive string
	options   map[string]any
	client    *http.Client
}

const DefaultOllamaKeepAlive = "30m"

// DefaultOllamaOptions are applied to every Ollama request unless the operator
// overrides them in config.
//
// num_ctx is 16384 rather than 4096 because 4096 could not hold a Soulacy agent.
// The shared Operating Contract is ~835 tokens before an agent adds its own role
// prompt, strategy contract, tool schemas, skills and knowledge — and a single
// news or search tool result then dwarfs all of it. Real runs were measured at
// 9.5k and 10.8k prompt tokens, so every one of them was being silently
// truncated to 4096 and answered from a mangled prompt.
//
// The cost is KV-cache memory, which is why this is not set higher: 16384 covers
// the observed working set with headroom, and an operator running a large model
// on a small machine can still lower it deliberately.
var DefaultOllamaOptions = map[string]any{
	"num_ctx":   16384,
	"num_batch": 128,
}

// contextLimit returns the configured num_ctx, or 0 when it is not set or is not
// a number this can compare against.
func (o *OllamaProvider) contextLimit() int {
	if o == nil {
		return 0
	}
	switch v := o.options["num_ctx"].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64: // config decoded from JSON/YAML arrives as float64
		return int(v)
	}
	return 0
}

// nextContextSize suggests the next power-of-two window that would hold a prompt
// of n tokens with room to answer, so the error message names a concrete value
// rather than telling the operator to "increase it".
func nextContextSize(n int) int {
	size := 4096
	for size < n+2048 && size < 262144 {
		size *= 2
	}
	return size
}

// NewOllamaProvider creates a provider targeting the given Ollama base URL.
func NewOllamaProvider(baseURL, defaultModel string, keepAlive string, options map[string]any) *OllamaProvider {
	if keepAlive == "" {
		keepAlive = DefaultOllamaKeepAlive
	}
	return &OllamaProvider{
		baseURL:   baseURL,
		model:     defaultModel,
		keepAlive: keepAlive,
		options:   ollamaOptionsWithDefaults(options),
		// No hard HTTP timeout AND no response-header timeout — the engine's
		// context already governs the upper bound (per-agent run_timeout). Big
		// local models (e.g. qwen3:32b, llama3.3:70b) can take minutes to load
		// from disk on first invocation; the shared transport's 120s
		// ResponseHeaderTimeout would abort that cold load with "timeout
		// awaiting response headers". LocalHTTPClient drops the header cap while
		// keeping the warm shared connection pool.
		client: LocalHTTPClient(0),
	}
}

func (o *OllamaProvider) ID() string { return "ollama" }

func (o *OllamaProvider) Complete(ctx context.Context, req CompletionRequest) (*CompletionResponse, error) {
	model := req.Model
	if model == "" {
		model = o.model
	}

	// Build the Ollama chat request. We must preserve the full tool-call loop —
	// assistant messages carry their tool_calls and tool messages carry the tool
	// name — otherwise the model can't see that it already called a tool and will
	// loop, re-calling the same tool every turn.
	msgs := make([]map[string]any, 0, len(req.Messages))
	for _, m := range req.Messages {
		om := map[string]any{"role": m.Role, "content": m.Content}
		if tcs := ollamaStyleToolCalls(m.ToolCalls); tcs != nil {
			om["tool_calls"] = tcs
		}
		if m.Role == "tool" && m.Name != "" {
			om["tool_name"] = m.Name
		}
		msgs = append(msgs, om)
	}

	body := map[string]any{
		"model":    model,
		"messages": msgs,
		"stream":   false,
	}
	if o.keepAlive != "" {
		body["keep_alive"] = o.keepAlive
	}
	options := cloneOptions(o.options)
	options["temperature"] = req.Temperature
	if req.TopP > 0 {
		options["top_p"] = req.TopP
	}
	if req.MaxTokens > 0 {
		options["num_predict"] = req.MaxTokens
	}
	if len(options) > 0 {
		body["options"] = options
	}

	// Structured output: Ollama accepts either format:"json" (free-form JSON)
	// or format: <JSON schema> (structured outputs, Ollama >= 0.5).
	switch req.ResponseFormat {
	case "json":
		body["format"] = "json"
	case "json_schema":
		if req.JSONSchema != nil {
			body["format"] = req.JSONSchema
		} else {
			body["format"] = "json"
		}
	}

	// Include tools if present (Ollama >= 0.3 supports function calling).
	// Ollama speaks the OpenAI function-calling wire format, so the tool
	// definitions and tool_choice constraint are built by the shared
	// translate.go helpers. The caller (engine) already strips ToolChoice
	// between turns so we don't trap the model in a forced-tool loop.
	if len(req.Tools) > 0 {
		body["tools"] = openAIStyleTools(req.Tools)
		applyOpenAIToolChoice(body, req.ToolChoice)
	}

	// Streaming mode: only when the caller requests it AND there are no tools
	// (streaming + tool_calls requires careful reassembly; we use the
	// non-streaming path for tool turns and fall through to streaming only on
	// the final synthesis turn when the caller opts in).
	if req.Stream && len(req.Tools) == 0 {
		body["stream"] = true
		streamPayload, _ := json.Marshal(body)
		streamReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
			o.baseURL+"/api/chat", bytes.NewReader(streamPayload))
		if err != nil {
			return nil, fmt.Errorf("ollama: build stream request: %w", err)
		}
		streamReq.Header.Set("Content-Type", "application/json")
		streamResp, err := o.client.Do(streamReq)
		if err != nil {
			return nil, fmt.Errorf("ollama: stream request failed: %w", err)
		}
		if streamResp.StatusCode != http.StatusOK {
			streamResp.Body.Close()
			return nil, fmt.Errorf("ollama: stream unexpected status %d", streamResp.StatusCode)
		}
		ch := make(chan string, 64)
		result := &CompletionResponse{}
		go func() {
			defer close(ch)
			defer streamResp.Body.Close()
			scanner := bufio.NewScanner(streamResp.Body)
			for scanner.Scan() {
				line := scanner.Text()
				if line == "" {
					continue
				}
				var chunk struct {
					Message struct {
						Content string `json:"content"`
					} `json:"message"`
					Done            bool `json:"done"`
					EvalCount       int  `json:"eval_count"`
					PromptEvalCount int  `json:"prompt_eval_count"`
				}
				if err := json.Unmarshal([]byte(line), &chunk); err != nil {
					continue
				}
				if chunk.Message.Content != "" {
					ch <- chunk.Message.Content
				}
				if chunk.Done {
					result.InputTokens = chunk.PromptEvalCount
					result.OutputTokens = chunk.EvalCount
					result.TotalTokens = chunk.PromptEvalCount + chunk.EvalCount
					return
				}
			}
		}()
		result.Stream = ch
		return result, nil
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("ollama: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		o.baseURL+"/api/chat", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("ollama: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := o.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ollama: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama: unexpected status %d", resp.StatusCode)
	}

	var result struct {
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
			// Reasoning models (qwen3, deepseek-r1, …) on Ollama >= 0.9 return
			// their chain-of-thought in a SEPARATE "thinking" field and the
			// user-facing answer in "content". When the model wraps its whole
			// reply in <think>…</think>, Ollama extracts the block into
			// "thinking" and "content" can come back EMPTY — which previously
			// caused the engine to discard a completed run as "(no final
			// response produced)". We capture it so the engine can recover the
			// post-think answer when content is empty.
			Thinking  string `json:"thinking"`
			ToolCalls []struct {
				Function struct {
					Name      string         `json:"name"`
					Arguments map[string]any `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"message"`
		EvalCount       int `json:"eval_count"`
		PromptEvalCount int `json:"prompt_eval_count"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("ollama: decode response: %w", err)
	}

	// Surface the real answer. Some Ollama builds leave inline <think>…</think>
	// blocks in content; strip them to the post-think tail. If content is then
	// empty but the model put a final answer after its reasoning inside the
	// "thinking" field, recover that tail so a completed run is never lost.
	content := stripThinkBlocks(result.Message.Content)
	if strings.TrimSpace(content) == "" {
		content = stripThinkBlocks(result.Message.Thinking)
	}

	// Ollama TRUNCATES a prompt longer than num_ctx instead of refusing it, and
	// says nothing. The model then answers a prompt whose beginning — the system
	// contract, the instructions, often the user's own question — has been cut
	// off, and typically emits a single token. Upstream that surfaced as
	// "(no final response produced)": a total failure with no stated cause, on a
	// run where every tool call had succeeded.
	//
	// The numbers to diagnose it are right here in the response, so say it.
	// Checked only when the answer is empty AND there were no tool calls: a model
	// legitimately returning just a tool call is not a failure.
	if strings.TrimSpace(content) == "" && len(result.Message.ToolCalls) == 0 {
		if limit := o.contextLimit(); limit > 0 && result.PromptEvalCount >= limit {
			return nil, fmt.Errorf(
				"ollama: the prompt was %d tokens but this provider's context window is %d, "+
					"so Ollama truncated it and %s returned an empty answer. "+
					"Raise llm.providers.<id>.options.num_ctx to at least %d, or reduce the agent's "+
					"system prompt, skills and tools",
				result.PromptEvalCount, limit, model, nextContextSize(result.PromptEvalCount))
		}
	}

	r := &CompletionResponse{
		Content:      content,
		InputTokens:  result.PromptEvalCount,
		OutputTokens: result.EvalCount,
		TotalTokens:  result.PromptEvalCount + result.EvalCount,
	}

	// Map tool calls
	for _, tc := range result.Message.ToolCalls {
		r.ToolCalls = append(r.ToolCalls, toolCallFromFunc(tc.Function.Name, tc.Function.Arguments))
	}

	return r, nil
}

// stripThinkBlocks removes <think>…</think> reasoning blocks that some Ollama
// builds leave inline in the message content and returns the surrounding
// user-facing text. Reasoning models (qwen3, deepseek-r1) emit their
// chain-of-thought in such blocks; when the block is the entire message we want
// the post-think tail, not the reasoning. A defensively-unclosed <think> (no
// closing tag) drops everything from the tag onward. Returns the input trimmed
// when there is no think block to strip.
func stripThinkBlocks(s string) string {
	if !strings.Contains(s, "<think>") && !strings.Contains(s, "</think>") {
		return strings.TrimSpace(s)
	}
	var b strings.Builder
	rest := s
	for {
		open := strings.Index(rest, "<think>")
		if open < 0 {
			// No further opening tag. A stray closing tag (reasoning that began
			// before this chunk) means everything up to it was think content.
			if close := strings.Index(rest, "</think>"); close >= 0 {
				rest = rest[close+len("</think>"):]
			}
			b.WriteString(rest)
			break
		}
		b.WriteString(rest[:open])
		after := rest[open+len("<think>"):]
		close := strings.Index(after, "</think>")
		if close < 0 {
			// Unclosed think block — drop the remainder (it's all reasoning).
			break
		}
		rest = after[close+len("</think>"):]
	}
	return strings.TrimSpace(b.String())
}

func cloneOptions(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func ollamaOptionsWithDefaults(in map[string]any) map[string]any {
	out := cloneOptions(DefaultOllamaOptions)
	for k, v := range in {
		out[k] = v
	}
	return out
}

func (o *OllamaProvider) Models(ctx context.Context) ([]string, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, o.baseURL+"/api/tags", nil)
	resp, err := o.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama: list models: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	names := make([]string, len(result.Models))
	for i, m := range result.Models {
		names[i] = m.Name
	}
	return names, nil
}

// --- helpers ---

// toolCallSeq monotonically increases across all toolCallFromFunc invocations
// in the process. Combined with the tool name it produces unique CallIDs
// even when the model emits multiple parallel calls to the same tool with
// different arguments (PRODUCTION_AUDIT → HIGH: OpenAI-style providers
// reject duplicate tool_call_id on the next turn).
var toolCallSeq atomic.Uint64

func toolCallFromFunc(name string, args map[string]any) message.ToolCall {
	n := toolCallSeq.Add(1)
	return message.ToolCall{ID: fmt.Sprintf("call_%s_%d", name, n), Name: name, Arguments: args}
}
