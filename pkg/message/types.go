// Package message re-exports the canonical Soulacy message types from the
// versioned SDK module (github.com/soulacy/soulacy/sdk/message, Story E9).
// Every name here is a type alias or re-export — existing imports compile
// unchanged and values are interchangeable with SDK values.
package message

import sdkmsg "github.com/soulacy/soulacy/sdk/message"

// Role identifies the origin of a message in a conversation.
type Role = sdkmsg.Role

const (
	RoleUser      = sdkmsg.RoleUser
	RoleAssistant = sdkmsg.RoleAssistant
	RoleSystem    = sdkmsg.RoleSystem
	RoleTool      = sdkmsg.RoleTool
)

// ContentType identifies the kind of a message part.
type ContentType = sdkmsg.ContentType

const (
	ContentText  = sdkmsg.ContentText
	ContentImage = sdkmsg.ContentImage
	ContentFile  = sdkmsg.ContentFile
	ContentAudio = sdkmsg.ContentAudio
)

// Part is one piece of message content.
type Part = sdkmsg.Part

// Message is the canonical message envelope.
type Message = sdkmsg.Message

// Reserved Message.Metadata keys (see sdk/message).
const (
	MetaReasoningDegraded = sdkmsg.MetaReasoningDegraded
	MetaReasoningSteps    = sdkmsg.MetaReasoningSteps
	MetaOutcome           = sdkmsg.MetaOutcome
	MetaOutcomeSummary    = sdkmsg.MetaOutcomeSummary
)

// Text builds a single-text-part content slice.
var Text = sdkmsg.Text

// ToolCall is a model-requested tool invocation.
type ToolCall = sdkmsg.ToolCall

// ToolResult is the outcome of one tool invocation.
type ToolResult = sdkmsg.ToolResult

// Event is one observability event (message.in, tool.call, …).
type Event = sdkmsg.Event
