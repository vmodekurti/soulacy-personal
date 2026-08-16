package runs

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/soulacy/soulacy/pkg/message"
)

// message.go — turning a run record back into the message a worker executes.
//
// This lives with the record rather than in the gateway because there are now
// two callers: the API that admits a run, and the recovery sweep that
// re-queues one whose worker died (MU-021 criterion 6). Two copies of "how a
// run becomes a message" would drift, and the way they would drift is subtle —
// a differing session ID or a missing run_id metadata key produces a message
// that executes fine and updates nothing.

// Channel is the pseudo-channel a durable run travels on. It has no adapter:
// a run's reply is stored on its record, which is the whole point of the run
// being durable rather than a held-open connection.
const Channel = "run"

// IDMetadataKey carries the run id on the inbound message.
const IDMetadataKey = "run_id"

// InboundMessage builds the message that executes a run.
func InboundMessage(run Run) message.Message {
	return message.Message{
		ID:          run.ID,
		WorkspaceID: run.WorkspaceID,
		SessionID:   SessionID(run),
		AgentID:     run.AgentID,
		Channel:     Channel,
		UserID:      run.Subject,
		Role:        message.RoleUser,
		Parts:       message.Text(PromptFromPayload(run.Payload)),
		Metadata:    map[string]string{IDMetadataKey: run.ID},
		CreatedAt:   time.Now().UTC(),
	}
}

// SessionID keeps a run's conversation separable. A caller that supplied a
// session joins it; one that did not gets a session of its own rather than
// sharing a default with every other run of the same agent.
func SessionID(run Run) string {
	if strings.TrimSpace(run.SessionID) != "" {
		return run.SessionID
	}
	return "run-" + run.ID
}

// PromptFromPayload extracts the prompt a run should execute.
//
// `{"prompt": "..."}` is the documented shape; anything else is passed through
// as JSON so an agent that expects structured input still receives exactly what
// the caller sent, rather than an empty message and a silent no-op.
func PromptFromPayload(payload json.RawMessage) string {
	if len(payload) == 0 {
		return ""
	}
	var shaped struct {
		Prompt string `json:"prompt"`
		Text   string `json:"text"`
	}
	if err := json.Unmarshal(payload, &shaped); err == nil {
		if strings.TrimSpace(shaped.Prompt) != "" {
			return shaped.Prompt
		}
		if strings.TrimSpace(shaped.Text) != "" {
			return shaped.Text
		}
	}
	return string(payload)
}
