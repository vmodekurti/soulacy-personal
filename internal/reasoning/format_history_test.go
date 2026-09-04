package reasoning

import (
	"errors"
	"strings"
	"testing"
)

func TestFormatStepHistoryRendersActionArgumentsAsJSON(t *testing.T) {
	history := formatStepHistory([]Step{
		{
			Thought: "queue resource",
			Action: ToolCall{
				Tool: "queue_put",
				Arguments: map[string]any{
					"queue": "pending_resources",
					"item":  map[string]any{"url": "https://example.com"},
				},
			},
			Obs: Observation{Content: "queued", Source: "queue_put"},
		},
	})

	if strings.Contains(history, "map[") {
		t.Fatalf("history leaked Go map formatting: %s", history)
	}
	if !strings.Contains(history, `Action: queue_put({"item":{"url":"https://example.com"},"queue":"pending_resources"})`) {
		t.Fatalf("history did not render compact JSON action args: %s", history)
	}
}

func TestFormatStepHistoryFallsBackToObservationError(t *testing.T) {
	history := formatStepHistory([]Step{
		{
			Thought: "send message",
			Action:  ToolCall{Tool: "channel.send", Input: map[string]string{"channel": "slack"}},
			Obs:     Observation{Error: errors.New("missing destination")},
		},
	})

	if !strings.Contains(history, `Action: channel.send({"channel":"slack"})`) {
		t.Fatalf("history did not render input args as JSON: %s", history)
	}
	if !strings.Contains(history, "Observation: missing destination") {
		t.Fatalf("history did not include observation error: %s", history)
	}
}
