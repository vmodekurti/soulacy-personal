package llm

import (
	"context"
	"time"
)

// Support deliberately distinguishes missing evidence from a reported absence.
// These describe API/model features, never a benchmark of intelligence.
type Support string

const (
	SupportUnknown Support = "unknown"
	SupportYes     Support = "supported"
	SupportNo      Support = "unsupported"
)

// ModelProfile is optional provider metadata. Zero limits mean unknown.
// ContextTokens is a shared input+output window; InputTokens is a separate
// input ceiling. Do not substitute an architectural maximum for a smaller
// configured serving window. No prompts, keys, URLs or raw model text belong here.
type ModelProfile struct {
	Provider      string    `json:"provider"`
	Model         string    `json:"model"`
	Source        string    `json:"source"` // provider_metadata, adapter, unknown
	Chat          Support   `json:"chat"`
	NativeTools   Support   `json:"native_tools"`
	JSONMode      Support   `json:"json_mode"`
	Reasoning     Support   `json:"reasoning"`
	Vision        Support   `json:"vision"`
	ContextTokens int       `json:"context_tokens"`
	InputTokens   int       `json:"input_tokens"`
	OutputTokens  int       `json:"output_tokens"`
	ContextSource string    `json:"context_source"`
	CheckedAt     time.Time `json:"checked_at"`
	ExpiresAt     time.Time `json:"expires_at"`
	Warning       string    `json:"warning,omitempty"`
}

// ModelProfiler is an OPTIONAL extension. Existing Provider implementations
// remain source-compatible. Profiling must be read-only metadata discovery:
// never inference, tool execution, model downloads, or a self-assessment prompt.
type ModelProfiler interface {
	ProfileModel(context.Context, string) (ModelProfile, error)
}

// DefaultModelProvider exposes the model used when a completion omits Model.
// Returning an empty value is honest when the provider chooses dynamically.
type DefaultModelProvider interface{ DefaultModel() string }
