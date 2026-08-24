// Package llm defines the provider contract for LLM inference backends and
// the provider-agnostic request/response types shared by all of them.
//
// Compatibility: Provider is FROZEN per SDK major version; request/response
// structs grow by APPENDING fields only (zero values must keep old
// behaviour). See the SDK README.
package llm

import (
	"context"

	"github.com/soulacy/soulacy/sdk/message"
)

// CompletionRequest is the provider-agnostic input to an LLM call.
type CompletionRequest struct {
	// Operation identifies the billable inference class. Empty means chat;
	// "embedding" has no generated-output reservation.
	Operation   string
	Model       string
	Messages    []ChatMessage
	Tools       []ToolSchema
	Temperature float64
	TopP        float64
	MaxTokens   int
	Stream      bool
	// DisablePromptCaching lets the governance layer suppress explicit provider
	// cache controls for data classes that have not opted in.
	DisablePromptCaching bool
	// Optional provider-specific tuning. Zero values preserve existing behavior.
	PresencePenalty  float64
	FrequencyPenalty float64
	ReasoningEffort  string

	// ResponseFormat hints the provider to constrain its output. Empty = free
	// text. "json" = the response must be a single JSON value (object/array).
	// "json_schema" + JSONSchema = the response must validate against the
	// supplied JSON Schema (where supported — OpenAI structured outputs,
	// Gemini responseSchema; on Anthropic/Ollama we fall back to JSON-mode
	// + post-validation in the engine).
	ResponseFormat string
	JSONSchema     map[string]any
	// JSONSchemaLenient, when true, asks providers that support strict structured
	// output (OpenAI) to treat the JSONSchema as a best-effort GUIDE rather than a
	// hard, strict-mode contract. This is for schemas that intentionally allow
	// freeform sub-objects (e.g. a workflow builder's node params / trigger
	// config) which strict mode — additionalProperties:false + all-required —
	// would reject. Zero value (false) preserves the existing strict behaviour.
	JSONSchemaLenient bool

	// ToolChoice constrains the model's tool-selection behaviour for this
	// single request. Empty = no constraint. Otherwise one of:
	//   "auto"        — model decides
	//   "none"        — must not call a tool
	//   "required"    — must call at least one tool
	//   "<tool_name>" — must call this specific tool
	// The caller (engine) is responsible for clearing this between turns so
	// only turn 1 forces delegation; subsequent turns can synthesise freely.
	ToolChoice string
}

// ChatMessage mirrors the role/content structure used by all major LLM APIs.
type ChatMessage struct {
	Role    string // "system", "user", "assistant", "tool"
	Content string
	// For tool result messages:
	ToolCallID string
	Name       string // tool name (for tool-role messages)
	// For assistant messages that include tool calls:
	ToolCalls []message.ToolCall
}

// ToolSchema is the JSON Schema description of a callable tool.
type ToolSchema struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// CompletionResponse carries the LLM's answer back to the runtime.
type CompletionResponse struct {
	Content   string
	ToolCalls []message.ToolCall
	// FinishReason is the provider-reported reason generation stopped. Common
	// values include "stop", "length", "max_tokens", and "MAX_TOKENS".
	// The runtime uses this to distinguish a complete answer from a partial one.
	FinishReason string
	// Usage statistics
	InputTokens  int
	OutputTokens int
	// Prompt caching statistics (Anthropic only; zero on other providers).
	// CacheCreationTokens is the number of input tokens written to cache this
	// turn (billed at 1.25× standard input rate).
	// CacheReadTokens is the number of input tokens served from cache this
	// turn (billed at 0.1× standard input rate — 90% discount).
	CacheCreationTokens int
	CacheReadTokens     int
	// ReasoningTokens are provider-reported hidden thinking/reasoning tokens.
	// ToolUsePromptTokens are prompt tokens attributable to tool definitions or
	// tool-use context when the provider reports that dimension separately.
	ReasoningTokens     int
	ToolUsePromptTokens int
	// TotalTokens is the provider-reported total. Callers should fall back to
	// summing the dimensions above when it is zero.
	TotalTokens int
	// ProviderRequestID enables reconciliation with provider usage and billing
	// exports without persisting prompt content.
	ProviderRequestID string
	// ProviderRequestIDs includes every HTTP retry attempt identifier in order.
	// ProviderRequestID remains the final/successful identifier for compatibility.
	ProviderRequestIDs []string
	AttemptCount       int
	// If Stream is true, tokens arrive on this channel. Closed when done.
	// Providers update the usage fields above before closing this channel.
	Stream <-chan string
}

// Provider is the interface every LLM inference backend implements.
type Provider interface {
	// ID returns the provider's unique identifier (e.g. "ollama", "openai").
	ID() string

	// Complete sends a chat-completion request and returns the response.
	// ctx carries deadline and cancellation.
	Complete(ctx context.Context, req CompletionRequest) (*CompletionResponse, error)

	// Models returns the list of available model identifiers.
	Models(ctx context.Context) ([]string, error)
}
