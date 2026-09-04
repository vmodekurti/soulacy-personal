// translate.go — shared message/tool-schema translation for LLM providers.
//
// PRODUCTION_AUDIT → MEDIUM/Maintainability: the four Complete() implementations
// (Ollama, OpenAI, Anthropic, Gemini) each re-derived the request wire format
// by hand, and the tool-calling glue in particular was copy-pasted between the
// OpenAI-compatible providers. A fix to one (e.g. the tool_choice constraint
// shape, or how parallel tool calls are serialized) had to be hand-applied to
// every copy or they silently drifted. This file is the single translation
// path, following the same "extract the genuinely shared layer" pattern as
// retry.go and httpclient.go.
//
// IMPORTANT: the three families (OpenAI-compatible, Anthropic, Gemini) have
// genuinely different wire formats. We do NOT force one format on all of them.
// What is shared lives here; what is provider-specific (Anthropic input_schema +
// cache_control, Gemini functionDeclarations + schema sanitization) stays in the
// provider file. Only byte-for-byte identical logic is unified.

package llm

import (
	"encoding/json"
	"strings"

	"github.com/soulacy/soulacy/pkg/message"
)

// --- OpenAI-compatible family (OpenAI, OpenRouter, Together, Groq, vLLM, Ollama) ---

// openAIStyleTools renders the tool list in the OpenAI function-calling shape:
//
//	[{"type":"function","function":{"name":...,"description":...,"parameters":...}}]
//
// This wire shape is identical for OpenAI-compatible endpoints and Ollama's
// /api/chat, so both providers share this one renderer. Returns nil for an
// empty tool list so callers can skip setting the "tools" field entirely.
func openAIStyleTools(tools []ToolSchema) []map[string]any {
	if len(tools) == 0 {
		return nil
	}
	out := make([]map[string]any, len(tools))
	for i, t := range tools {
		out[i] = map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        t.Name,
				"description": t.Description,
				"parameters":  t.Parameters,
			},
		}
	}
	return out
}

// applyOpenAIToolChoice writes the OpenAI/Ollama tool_choice constraint into
// body, mirroring OpenAI's schema:
//
//	"auto" / "none" / "required" → raw string
//	"<tool_name>"                → {"type":"function","function":{"name":"X"}}
//
// An empty (or whitespace-only) choice leaves body untouched. The caller
// (engine) is responsible for clearing the choice between turns so the model
// isn't trapped in a forced-tool loop after the first call.
func applyOpenAIToolChoice(body map[string]any, toolChoice string) {
	tc := strings.TrimSpace(toolChoice)
	if tc == "" {
		return
	}
	switch tc {
	case "auto", "none", "required":
		body["tool_choice"] = tc
	default:
		body["tool_choice"] = map[string]any{
			"type":     "function",
			"function": map[string]any{"name": tc},
		}
	}
}

// openAIStyleToolCalls renders an assistant message's tool calls in OpenAI's
// shape: each call carries its id, type:"function", and a function object whose
// arguments are a JSON-encoded *string*. Returns nil when there are no calls.
func openAIStyleToolCalls(calls []message.ToolCall) []map[string]any {
	if len(calls) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(calls))
	for _, tc := range calls {
		args, _ := json.Marshal(tc.Arguments)
		call := map[string]any{
			"id":   tc.ID,
			"type": "function",
			"function": map[string]any{
				"name":      tc.Name,
				"arguments": string(args),
			},
		}
		if tc.ThoughtSignature != "" {
			// Gemini thinking models require this opaque signature to be echoed on
			// later function-call turns. OpenAI-compatible Gemini routers may accept
			// either extension spelling.
			call["thought_signature"] = tc.ThoughtSignature
			call["thoughtSignature"] = tc.ThoughtSignature
		}
		out = append(out, call)
	}
	return out
}

// ollamaStyleToolCalls renders an assistant message's tool calls in Ollama's
// shape: a bare function object with arguments as a *map* (not a string), and
// no id/type fields. Returns nil when there are no calls.
func ollamaStyleToolCalls(calls []message.ToolCall) []map[string]any {
	if len(calls) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(calls))
	for _, tc := range calls {
		out = append(out, map[string]any{
			"function": map[string]any{"name": tc.Name, "arguments": tc.Arguments},
		})
	}
	return out
}
