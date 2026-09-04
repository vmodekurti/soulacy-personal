// Package message defines the canonical message types that flow through Soulacy.
// All channel adapters translate their platform-specific formats into these types,
// ensuring the runtime never has to know which channel a message came from.
package message

import "time"

// Role identifies the origin of a message in a conversation.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleSystem    Role = "system"
	RoleTool      Role = "tool"
)

// ContentType describes the media type of a message part.
type ContentType string

const (
	ContentText  ContentType = "text"
	ContentImage ContentType = "image"
	ContentAudio ContentType = "audio"
	ContentFile  ContentType = "file"
)

// Part is one piece of a (possibly multi-modal) message body.
type Part struct {
	Type     ContentType `json:"type"`
	Text     string      `json:"text,omitempty"`
	MimeType string      `json:"mime_type,omitempty"`
	Data     []byte      `json:"data,omitempty"` // base64 decoded
	URL      string      `json:"url,omitempty"`
}

// Message is the canonical inbound/outbound message shared across all subsystems.
type Message struct {
	ID        string            `json:"id"`
	SessionID string            `json:"session_id"`
	AgentID   string            `json:"agent_id"`
	Channel   string            `json:"channel"`   // e.g. "telegram", "discord", "http"
	ThreadID  string            `json:"thread_id"` // channel-native thread/conversation id
	UserID    string            `json:"user_id"`
	Username  string            `json:"username"`
	Role      Role              `json:"role"`
	Parts     []Part            `json:"parts"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
}

// Reserved Message.Metadata keys.
//
// A reply carrying MetaReasoningDegraded="true" came out of a reasoning run
// that ended without confidence — a tool errored, or the controller had to
// recover from a malformed reasoning step. The text is still the model's best
// available output, but it may be partial, or a progress note rather than a
// finished deliverable. Consumers that present a reply as a completed result
// (scheduled channel delivery above all) should mark or withhold it rather
// than passing it off as a clean run. Absent = the run was confident.
const (
	MetaReasoningDegraded = "reasoning_degraded"
	MetaReasoningSteps    = "reasoning_steps"
	// MetaOutcome carries the business-outcome class of a run whose agent
	// declares an outcome contract: complete | partial | empty | failed. Present
	// only when the contract went unmet, alongside MetaReasoningDegraded — a run
	// can execute every node without error and still have achieved nothing, and
	// that distinction is what this records.
	MetaOutcome = "outcome"
	// MetaOutcomeSummary explains the unmet contract in the author's own words
	// ("three sources were added — 0 items"), for display where the full
	// assertion list would be too much.
	MetaOutcomeSummary = "outcome_summary"
)

// Text is a convenience constructor for a plain-text message.
func Text(text string) []Part {
	return []Part{{Type: ContentText, Text: text}}
}

// ToolCall represents a request from the LLM to call a specific tool.
type ToolCall struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`

	// ThoughtSignature is an opaque base64 blob returned by Gemini 2.5 thinking
	// models alongside each functionCall. It must be echoed back verbatim in
	// subsequent turns or Gemini returns a 400 INVALID_ARGUMENT.
	// Other providers leave this field empty and it is not transmitted.
	ThoughtSignature string `json:"thought_signature,omitempty"`
}

// ToolResult carries the result back from a tool execution.
//
// Trust and Source are Cohort F S1 additions. They record how the
// runtime classified the payload — "untrusted" content is wrapped in
// an <external_content> envelope inside Content and is subject to the
// S3 tool-call intent gate; "trusted" content is framework-minted
// status/metadata. Source names the origin category ("network", "file",
// "kb", "queue", "channel", "mcp", …) so trace UIs can group results
// without re-parsing the envelope. Both fields are omit-empty for
// backward compatibility with older event consumers.
type ToolResult struct {
	CallID  string `json:"call_id"`
	Name    string `json:"name"`
	Content string `json:"content"`
	IsError bool   `json:"is_error"`
	Trust   string `json:"trust,omitempty"`
	Source  string `json:"source,omitempty"`
}

// Event is a structured log event streamed over WebSocket to the GUI.
type Event struct {
	Type      string    `json:"type"` // message.in, message.out, tool.call, tool.result, error
	AgentID   string    `json:"agent_id"`
	SessionID string    `json:"session_id"`
	Payload   any       `json:"payload"`
	Timestamp time.Time `json:"timestamp"`

	// Parts carries typed media attachments associated with this event.
	// Nil for events that carry no attachment context.
	Parts []TypedPart `json:"parts,omitempty"`
}
