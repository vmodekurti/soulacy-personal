package agentvalidate

import "testing"

func TestPromptImpliesDelivery(t *testing.T) {
	yes := []string{
		"When done, send the summary to Telegram.",
		"Deliver the report to the team channel each morning.",
		"Notify me on Slack with the results.",
		"Output: Telegram",
		"Post the brief to Discord when finished.",
	}
	for _, p := range yes {
		if !PromptImpliesDelivery(p) {
			t.Fatalf("expected delivery-implied for: %q", p)
		}
	}
	no := []string{
		"You are a helpful assistant. Answer the user's question.",
		"Summarize the input and return a concise paragraph.",
		"",
	}
	for _, p := range no {
		if PromptImpliesDelivery(p) {
			t.Fatalf("did NOT expect delivery-implied for: %q", p)
		}
	}
}
