package llm

import (
	"context"
	"encoding/json"
	"strings"
	"unicode"
)

// CallMetadata is immutable attribution attached by an ingress or runtime.
// Empty fields are allowed so internal and plugin calls remain metered even
// before their caller has been upgraded to provide richer attribution.
type CallMetadata struct {
	Subject   string
	Workspace string
	// Organization is the tenant above the workspace. Carried because MU-024
	// lets a limit be set at the organization level, and an organization
	// ceiling that could not be resolved from a call's attribution would be a
	// setting with no effect.
	Organization       string
	AgentID            string
	SessionID          string
	RunID              string
	CallID             string
	Source             string
	Trigger            string
	DataClassification string
	OverrideAuthorized bool
	CostConfirmed      bool
}

type callMetadataKey struct{}

// WithCallMetadata attaches trusted billing attribution to an inference call.
func WithCallMetadata(ctx context.Context, metadata CallMetadata) context.Context {
	return context.WithValue(ctx, callMetadataKey{}, metadata)
}

// CallMetadataFromContext returns attribution previously attached to ctx.
func CallMetadataFromContext(ctx context.Context) CallMetadata {
	if ctx == nil {
		return CallMetadata{}
	}
	metadata, _ := ctx.Value(callMetadataKey{}).(CallMetadata)
	return metadata
}

// Reservation identifies capacity reserved by a Controller before a provider
// request starts. The value is opaque to the Router.
type Reservation struct{ ID string }

// Controller is the authoritative admission and accounting boundary for LLM
// requests. Before may clamp request limits or reject the call. After is
// invoked exactly once, including for provider errors and completed/cancelled
// streams.
type Controller interface {
	Before(ctx context.Context, provider string, req *CompletionRequest) (context.Context, Reservation, error)
	After(ctx context.Context, reservation Reservation, provider string, req CompletionRequest, resp *CompletionResponse, callErr error)
}

// RejectionRecorder is an optional controller extension for requests rejected
// before provider access. It makes admission failures visible without invoking
// After, whose reservation lifecycle assumes provider capacity was acquired.
type RejectionRecorder interface {
	Rejected(ctx context.Context, provider string, req CompletionRequest, err error)
}

// EstimateRequestTokens provides a conservative, provider-neutral fallback
// when a provider does not expose token counting. It intentionally includes
// roles, tool schemas, names, and call identifiers rather than message text
// alone. Provider-reported usage always supersedes this estimate.
func EstimateRequestTokens(req CompletionRequest) int {
	tokens := 0
	for _, msg := range req.Messages {
		// Provider chat encodings add framing tokens even to empty messages.
		tokens += 4 + EstimateTextTokens(msg.Role) + EstimateTextTokens(msg.Content)
		tokens += EstimateTextTokens(msg.Name) + EstimateTextTokens(msg.ToolCallID)
		for _, tc := range msg.ToolCalls {
			encoded, _ := json.Marshal(tc.Arguments)
			tokens += 4 + EstimateTextTokens(tc.Name) + EstimateTextTokens(string(encoded))
		}
	}
	for _, tool := range req.Tools {
		encoded, _ := json.Marshal(tool.Parameters)
		tokens += 6 + EstimateTextTokens(tool.Name) + EstimateTextTokens(tool.Description) + EstimateTextTokens(string(encoded))
	}
	return tokens
}

func estimateTextTokens(text string) int {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0
	}
	return EstimateTextTokens(text)
}

// EstimateTextTokens is the conservative fallback used only when a provider
// does not return usage. ASCII prose tends toward four characters per token,
// but source code is materially denser and CJK commonly approaches one token
// per code point. Counting ASCII at three bytes/token and CJK/other non-ASCII
// at one token/code point avoids the systematic under-counts that caused
// proactive context trimming to miss overflows. The target tolerance on the
// pinned code and CJK fixtures is 35%, always in the safe (over-count) direction.
func EstimateTextTokens(text string) int {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0
	}
	asciiBytes, nonASCII := 0, 0
	for _, r := range text {
		if r <= unicode.MaxASCII {
			asciiBytes++
		} else {
			// utf8 validation is intentionally implicit in range: invalid bytes
			// become RuneError and are conservatively one token each.
			nonASCII++
		}
	}
	return (asciiBytes+2)/3 + nonASCII
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
