// openai.go — OpenAI-compatible provider (OpenAI, OpenRouter, Together, Groq,
// vLLM, and other endpoints speaking the /chat/completions wire format).
//
// Moved out of ollama.go (Story ARCH-5): the OpenAI adapter is its own family,
// not an Ollama detail. The shared OpenAI-style message/tool translation lives
// in translate.go.
package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/soulacy/soulacy/pkg/message"
)

// OpenAIProvider is a thin wrapper for any OpenAI-compatible endpoint
// (OpenAI, Anthropic via compatibility layer, Together, Groq, etc.)
type OpenAIProvider struct {
	id                string
	baseURL           string
	apiKey            string
	model             string
	organization      string // OpenAI-Organization header (enterprise/team accounts)
	parallelToolCalls *bool  // nil=default(true), false=serialize tool calls
	client            *http.Client
}

// DefaultOpenAITimeout is the overall per-request timeout for an
// OpenAI-compatible provider. It is generous because a large NON-STREAMING
// generation from a cloud reasoning model (e.g. glm/qwen served over an
// OpenAI-compatible endpoint) can legitimately run for minutes before it
// returns the full body — and the Studio builder makes exactly such calls. The
// header cap is dropped (LongGenHTTPClient) so a slow-to-first-header
// generation isn't aborted; this timeout and the request context are the bound.
// Override per provider with the `request_timeout` config key.
const DefaultOpenAITimeout = 300 * time.Second

func NewOpenAIProvider(id, baseURL, apiKey, model string) *OpenAIProvider {
	return NewOpenAIProviderWithOptions(id, baseURL, apiKey, model, "", nil)
}

// NewOpenAIProviderWithOptions creates an OpenAI-compatible provider with extra settings.
func NewOpenAIProviderWithOptions(id, baseURL, apiKey, model, organization string, parallelToolCalls *bool) *OpenAIProvider {
	return &OpenAIProvider{
		id: id, baseURL: baseURL, apiKey: apiKey, model: model,
		organization: organization, parallelToolCalls: parallelToolCalls,
		client: LongGenHTTPClient(DefaultOpenAITimeout),
	}
}

// SetRequestTimeout overrides the overall HTTP timeout for this provider's
// requests. A value <= 0 is ignored (the default is kept). Used to honour a
// per-provider `request_timeout` config for slow cloud reasoning models whose
// large (non-streaming) generations exceed the default.
func (p *OpenAIProvider) SetRequestTimeout(d time.Duration) {
	if d > 0 {
		p.client = LongGenHTTPClient(d)
	}
}

func (p *OpenAIProvider) ID() string { return p.id }

func (p *OpenAIProvider) Complete(ctx context.Context, req CompletionRequest) (*CompletionResponse, error) {
	model := req.Model
	if model == "" {
		model = p.model
	}

	// Build OpenAI-style messages. CRITICAL: preserve tool_calls on assistant
	// messages and tool_call_id on tool-role messages — otherwise the model
	// can't see that it already called a tool and will loop, re-calling the
	// same tool every turn (the same trap we hit with Ollama).
	msgs := make([]map[string]any, 0, len(req.Messages))
	for _, m := range req.Messages {
		om := map[string]any{"role": m.Role}
		// Content can be empty when an assistant message carries only tool_calls.
		if m.Content != "" || (m.Role != "assistant") {
			om["content"] = m.Content
		} else {
			om["content"] = nil
		}
		if tcs := openAIStyleToolCalls(m.ToolCalls); tcs != nil {
			om["tool_calls"] = tcs
		}
		if m.Role == "tool" {
			if m.ToolCallID != "" {
				om["tool_call_id"] = m.ToolCallID
			}
			if m.Name != "" {
				om["name"] = m.Name
			}
		}
		msgs = append(msgs, om)
	}

	body := map[string]any{
		"model":       model,
		"messages":    msgs,
		"temperature": req.Temperature,
		"stream":      false,
	}
	if req.MaxTokens > 0 {
		body["max_tokens"] = req.MaxTokens
	}
	if req.TopP > 0 {
		body["top_p"] = req.TopP
	}
	if req.PresencePenalty != 0 {
		body["presence_penalty"] = req.PresencePenalty
	}
	if req.FrequencyPenalty != 0 {
		body["frequency_penalty"] = req.FrequencyPenalty
	}
	if strings.TrimSpace(req.ReasoningEffort) != "" {
		body["reasoning_effort"] = strings.TrimSpace(req.ReasoningEffort)
	}

	if len(req.Tools) > 0 {
		body["tools"] = openAIStyleTools(req.Tools)
		// parallel_tool_calls: default is true (provider default); false serializes
		// tool calls which reduces agent loops on weaker models.
		if p.parallelToolCalls != nil {
			body["parallel_tool_calls"] = *p.parallelToolCalls
		} else if strings.Contains(strings.ToLower(model), "gemini") {
			// Gemini thinking models require opaque thought signatures to be echoed
			// for every functionCall part. Some OpenAI-compatible routers expose or
			// replay that metadata inconsistently on multi-tool turns, so default
			// Gemini tool use to one call at a time unless the provider config opts
			// into a specific parallel_tool_calls value.
			body["parallel_tool_calls"] = false
		}

		// Tool-choice constraint (OpenAI / OpenRouter / Together / Groq /
		// vLLM all accept this). Same semantics as Ollama: bare strings for
		// auto/none/required, object form for a specific tool name.
		applyOpenAIToolChoice(body, req.ToolChoice)
	}

	// Structured outputs.
	switch req.ResponseFormat {
	case "json":
		body["response_format"] = map[string]any{"type": "json_object"}
	case "json_schema":
		if req.JSONSchema != nil {
			body["response_format"] = map[string]any{
				"type": "json_schema",
				"json_schema": map[string]any{
					"name":   "output",
					"strict": !req.JSONSchemaLenient,
					"schema": req.JSONSchema,
				},
			}
		} else {
			body["response_format"] = map[string]any{"type": "json_object"}
		}
	}

	// Streaming mode: enabled when caller opts in and no tools are present
	// (tool-call reassembly from delta chunks is complex; we use non-streaming
	// for tool turns and stream only on the final synthesis turn).
	if req.Stream && len(req.Tools) == 0 {
		body["stream"] = true
		// Native OpenAI supports a final usage chunk. Compatibility providers vary;
		// the router's tokenizer fallback accounts for those streams safely.
		if p.id == "openai" {
			body["stream_options"] = map[string]any{"include_usage": true}
		}
		streamPayload, _ := json.Marshal(body)
		streamReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
			p.baseURL+"/chat/completions", bytes.NewReader(streamPayload))
		if err != nil {
			return nil, fmt.Errorf("%s: build stream request: %w", p.id, err)
		}
		streamReq.Header.Set("Content-Type", "application/json")
		streamReq.Header.Set("Accept", "text/event-stream")
		if p.apiKey != "" {
			streamReq.Header.Set("Authorization", "Bearer "+p.apiKey)
		}
		streamResp, err := p.client.Do(streamReq)
		if err != nil {
			return nil, fmt.Errorf("%s: stream request failed: %w", p.id, err)
		}
		if streamResp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(streamResp.Body)
			streamResp.Body.Close()
			return nil, fmt.Errorf("%s: stream http %d: %s", p.id, streamResp.StatusCode, string(body))
		}
		ch := make(chan string, 64)
		result := &CompletionResponse{
			ProviderRequestID: firstNonEmpty(streamResp.Header.Get("x-request-id"), streamResp.Header.Get("request-id")),
		}
		go func() {
			defer close(ch)
			defer streamResp.Body.Close()
			scanner := bufio.NewScanner(streamResp.Body)
			for scanner.Scan() {
				line := scanner.Text()
				if !strings.HasPrefix(line, "data: ") {
					continue
				}
				data := strings.TrimPrefix(line, "data: ")
				if data == "[DONE]" {
					return
				}
				var chunk struct {
					Choices []struct {
						Delta struct {
							Content string `json:"content"`
						} `json:"delta"`
						FinishReason string `json:"finish_reason"`
					} `json:"choices"`
					Usage *struct {
						PromptTokens      int `json:"prompt_tokens"`
						CompletionTokens  int `json:"completion_tokens"`
						TotalTokens       int `json:"total_tokens"`
						CompletionDetails struct {
							ReasoningTokens int `json:"reasoning_tokens"`
						} `json:"completion_tokens_details"`
						PromptDetails struct {
							CachedTokens int `json:"cached_tokens"`
						} `json:"prompt_tokens_details"`
					} `json:"usage"`
				}
				if err := json.Unmarshal([]byte(data), &chunk); err != nil {
					continue
				}
				if len(chunk.Choices) > 0 && chunk.Choices[0].Delta.Content != "" {
					ch <- chunk.Choices[0].Delta.Content
				}
				if len(chunk.Choices) > 0 && chunk.Choices[0].FinishReason != "" {
					result.FinishReason = chunk.Choices[0].FinishReason
				}
				if chunk.Usage != nil {
					result.CacheReadTokens = chunk.Usage.PromptDetails.CachedTokens
					result.InputTokens = max(0, chunk.Usage.PromptTokens-result.CacheReadTokens)
					result.OutputTokens = chunk.Usage.CompletionTokens
					result.TotalTokens = chunk.Usage.TotalTokens
					result.ReasoningTokens = chunk.Usage.CompletionDetails.ReasoningTokens
				}
			}
		}()
		result.Stream = ch
		return result, nil
	}

	strippedOptional := map[string]bool{}
	var bodyBytes []byte
	var providerRequestID string
	for {
		payload, _ := json.Marshal(body)
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
			p.baseURL+"/chat/completions", bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		httpReq.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(payload)), nil
		}
		httpReq.Header.Set("Content-Type", "application/json")
		if p.apiKey != "" {
			httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
		}
		if p.organization != "" {
			httpReq.Header.Set("OpenAI-Organization", p.organization)
		}

		// Retry on transient errors (429 / 5xx / network) — OpenAI's the most
		// rate-limit-prone provider, and a single blip used to kill agent runs.
		// PRODUCTION_AUDIT → HIGH/Reliability.
		resp, err := DoWithRetry(ctx, p.client, httpReq, RetryConfig{})
		if err != nil {
			return nil, fmt.Errorf("%s: request failed: %w", p.id, err)
		}
		bodyBytes, _ = io.ReadAll(resp.Body)
		providerRequestID = firstNonEmpty(resp.Header.Get("x-request-id"), resp.Header.Get("request-id"))
		_ = resp.Body.Close()

		if resp.StatusCode < 300 {
			break
		}
		param := openAIUnsupportedOptionalParam(bodyBytes)
		if param == "" || strippedOptional[param] {
			return nil, fmt.Errorf("%s: http %d: %s", p.id, resp.StatusCode, string(bodyBytes))
		}
		if param == "max_tokens" {
			if v, ok := body["max_tokens"]; ok && body["max_completion_tokens"] == nil {
				body["max_completion_tokens"] = v
			}
		}
		delete(body, param)
		strippedOptional[param] = true
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					ID                    string `json:"id"`
					ThoughtSignature      string `json:"thought_signature"`
					ThoughtSignatureCamel string `json:"thoughtSignature"`
					Function              struct {
						Name                  string `json:"name"`
						Arguments             string `json:"arguments"`
						ThoughtSignature      string `json:"thought_signature"`
						ThoughtSignatureCamel string `json:"thoughtSignature"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
			PromptDetails    struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
			CompletionDetails struct {
				ReasoningTokens int `json:"reasoning_tokens"`
			} `json:"completion_tokens_details"`
		} `json:"usage"`
	}

	if err := json.Unmarshal(bodyBytes, &result); err != nil {
		return nil, fmt.Errorf("%s: decode: %w", p.id, err)
	}
	if len(result.Choices) == 0 {
		return nil, fmt.Errorf("%s: empty choices", p.id)
	}

	r := &CompletionResponse{
		Content:           result.Choices[0].Message.Content,
		FinishReason:      result.Choices[0].FinishReason,
		InputTokens:       max(0, result.Usage.PromptTokens-result.Usage.PromptDetails.CachedTokens),
		OutputTokens:      result.Usage.CompletionTokens,
		TotalTokens:       result.Usage.TotalTokens,
		CacheReadTokens:   result.Usage.PromptDetails.CachedTokens,
		ReasoningTokens:   result.Usage.CompletionDetails.ReasoningTokens,
		ProviderRequestID: providerRequestID,
	}
	for _, tc := range result.Choices[0].Message.ToolCalls {
		var args map[string]any
		_ = json.Unmarshal([]byte(tc.Function.Arguments), &args)
		sig := tc.ThoughtSignature
		if sig == "" {
			sig = tc.ThoughtSignatureCamel
		}
		if sig == "" {
			sig = tc.Function.ThoughtSignature
		}
		if sig == "" {
			sig = tc.Function.ThoughtSignatureCamel
		}
		r.ToolCalls = append(r.ToolCalls, toolCallWithIDAndThoughtSignature(tc.ID, tc.Function.Name, args, sig))
	}
	if strings.Contains(strings.ToLower(model), "gemini") && len(r.ToolCalls) > 1 {
		// Gemini/OpenAI-compatible routers can return one thought signature for a
		// multi-function-call turn, then fail when later calls are replayed. Run
		// Gemini tools serially; the model can request the next tool after seeing
		// the first result.
		r.ToolCalls = r.ToolCalls[:1]
	}
	return r, nil
}

// Models queries the /models endpoint when available (OpenAI, OpenRouter,
// Together, Groq, vLLM all expose it). Returns the real error on failure
// (PRODUCTION_AUDIT → LOW/LLM) instead of pretending the list is
// `[configured-default]` — that was masking misconfiguration in the GUI.
// The Providers page now shows the actual error.
func (p *OpenAIProvider) Models(ctx context.Context) ([]string, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/models", nil)
	if err != nil {
		return nil, fmt.Errorf("%s: build /models request: %w", p.id, err)
	}
	if p.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("%s: /models request: %w", p.id, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("%s: /models returned %d: %s", p.id, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("%s: /models decode: %w", p.id, err)
	}
	if len(out.Data) == 0 {
		return []string{p.model}, nil // empty list — keep at least the default
	}
	ids := make([]string, 0, len(out.Data))
	for _, m := range out.Data {
		ids = append(ids, m.ID)
	}
	return ids, nil
}

func openAIUnsupportedOptionalParam(body []byte) string {
	msg := strings.ToLower(string(body))
	if msg == "" {
		return ""
	}
	if !(strings.Contains(msg, "unsupported") ||
		strings.Contains(msg, "unrecognized") ||
		strings.Contains(msg, "unknown") ||
		strings.Contains(msg, "deprecated") ||
		strings.Contains(msg, "not supported") ||
		strings.Contains(msg, "cannot both") ||
		strings.Contains(msg, "extra_forbidden") ||
		strings.Contains(msg, "invalid_request_error") ||
		strings.Contains(msg, "bad_request")) {
		return ""
	}
	for _, name := range []string{
		"parallel_tool_calls",
		"reasoning_effort",
		"response_format",
		"presence_penalty",
		"frequency_penalty",
		"tool_choice",
		"top_p",
		"temperature",
		"max_tokens",
		"max_completion_tokens",
	} {
		if strings.Contains(msg, name) {
			return name
		}
	}
	return ""
}

// toolCallWithID builds a ToolCall preserving the provider-assigned id.
// OpenAI-compatible providers return stable tool_call ids we must echo back
// verbatim on the following turn, so unlike toolCallFromFunc we do not mint a
// new one here.
func toolCallWithID(id, name string, args map[string]any) message.ToolCall {
	return toolCallWithIDAndThoughtSignature(id, name, args, "")
}

func toolCallWithIDAndThoughtSignature(id, name string, args map[string]any, thoughtSignature string) message.ToolCall {
	return message.ToolCall{ID: id, Name: name, Arguments: args, ThoughtSignature: thoughtSignature}
}
