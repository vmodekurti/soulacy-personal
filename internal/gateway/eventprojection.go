package gateway

import (
	"time"

	"github.com/soulacy/soulacy/pkg/message"
)

// eventprojection.go — MU-026 criterion 2: "every event carries workspace and
// authorization-relevant ownership metadata internally, while public payloads
// expose only permitted fields."
//
// TWO HALVES, AND ONLY ONE OF THEM IS A CHANGE.
//
// The internal half is already true and load-bearing: Engine.emit stamps the
// run's workspace onto every event, and authorizeEvent compares it to the
// subscriber's before anything is delivered (MU-026 criterion 7). Removing that
// metadata to satisfy "public payloads expose only permitted fields" would
// break the authorization that criterion exists for. The metadata has to be
// there; the question is only what crosses the wire.
//
// WHY ONE SERIALIZATION IS STILL CORRECT. The tempting reading is that a shared
// json.Marshal cannot satisfy this criterion, because one payload cannot be
// redacted differently per recipient. But authorization here is a boolean gate:
// a subscriber either receives an event or does not. Nothing about the payload
// varies by WHO receives it, only whether they do. So a single public
// projection, serialized once, is sufficient — and it keeps Emit off the
// per-recipient serialization cost, which matters because Emit runs on the
// agent execution path and must never block.
//
// WHAT THIS ACTUALLY BUYS, STATED HONESTLY. No field on message.Event today is
// something a permitted recipient must not see: a subscriber only ever receives
// its own workspace's events, so its own workspace ID is not a disclosure.
// This is therefore a BOUNDARY, not a fix. Its value is that the next field
// added to message.Event — a principal, a credential ID, a policy snapshot,
// the kind of thing an authorizer wants and a browser must not have — cannot
// reach the wire by default. TestEveryEventFieldIsClassified fails the build
// until somebody decides which side it belongs on.

// publicEvent is the wire form of an event.
//
// Field-for-field explicit rather than embedding message.Event and hiding
// fields with json tags: an embedded struct silently gains whatever is added to
// its parent, which is exactly the failure this type exists to prevent.
type publicEvent struct {
	Type string `json:"type"`
	// WorkspaceID stays on the wire. A subscriber only receives events from
	// its own workspace, so this tells it nothing it did not already know —
	// and clients use it to route between workspace views after a context
	// switch. Classified deliberately rather than by omission.
	WorkspaceID string              `json:"workspace_id,omitempty"`
	AgentID     string              `json:"agent_id"`
	SessionID   string              `json:"session_id"`
	Payload     any                 `json:"payload"`
	Timestamp   time.Time           `json:"timestamp"`
	Parts       []message.TypedPart `json:"parts,omitempty"`
}

// publicFields is the classification. Every field of message.Event must appear
// here, and the test that enforces that is the point of the whole file.
//
// true  = crosses the wire
// false = internal only: available to the authorizer, never serialized
var publicFields = map[string]bool{
	"Type":        true,
	"WorkspaceID": true,
	"AgentID":     true,
	"SessionID":   true,
	"Payload":     true,
	"Timestamp":   true,
	"Parts":       true,
}

// project returns the wire form of an event.
func project(event message.Event) publicEvent {
	return publicEvent{
		Type:        event.Type,
		WorkspaceID: event.WorkspaceID,
		AgentID:     event.AgentID,
		SessionID:   event.SessionID,
		Payload:     event.Payload,
		Timestamp:   event.Timestamp,
		Parts:       event.Parts,
	}
}
