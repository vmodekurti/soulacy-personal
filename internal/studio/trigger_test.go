package studio

import (
	"context"
	"testing"
)

// draftJSON is a minimal valid flow with a blank/unspecified trigger, used to
// exercise deterministic trigger inference (normalizeTrigger). The model is
// faked to echo this regardless of intent; the post-parse normalization is
// what must set the trigger from the intent's phrasing.
func triggerDraftJSON(triggerType, cron string) string {
	cfg := ""
	if cron != "" {
		cfg = `, "config": {"cron": "` + cron + `"}`
	}
	return `{
  "name": "T",
  "trigger": { "type": "` + triggerType + `"` + cfg + ` },
  "channels": ["telegram"],
  "flow": {
    "nodes": [
      { "id": "a", "kind": "agent", "agent": "responder", "input": "{{.input}}", "output": "out", "x": 0, "y": 0 }
    ],
    "edges": [ { "from": "a", "to": "end" } ],
    "entry": "a"
  }
}`
}

func TestNormalizeTrigger_ScheduleFromPhrasing(t *testing.T) {
	cases := []struct {
		name     string
		intent   string
		wantType string
		wantCron string
	}{
		{"every weekday at 8am", "Every weekday at 8am, send me the digest.", "schedule", "0 8 * * 1-5"},
		{"every morning", "Every morning summarize my inbox.", "schedule", "0 8 * * *"},
		{"daily at 9am", "Daily at 9am post the standup reminder.", "schedule", "0 9 * * *"},
		{"hourly", "Hourly, check the status page.", "schedule", "0 * * * *"},
		{"at 6pm", "At 6pm remind me to log off.", "schedule", "0 18 * * *"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Model leaves the trigger type blank → inference must fill it.
			llm := fakeLLM{out: triggerDraftJSON("", "")}
			res, err := Compile(context.Background(), llm, tc.intent, Catalog{}, nil)
			if err != nil {
				t.Fatalf("Compile: %v", err)
			}
			if res.Workflow.Trigger.Type != tc.wantType {
				t.Errorf("trigger type = %q, want %q", res.Workflow.Trigger.Type, tc.wantType)
			}
			gotCron, _ := res.Workflow.Trigger.Config["cron"].(string)
			if gotCron != tc.wantCron {
				t.Errorf("cron = %q, want %q", gotCron, tc.wantCron)
			}
		})
	}
}

func TestNormalizeTrigger_FillsCronWhenModelLeftBlank(t *testing.T) {
	// Model correctly set type=schedule but forgot the cron; inference fills
	// it from the intent without overriding the model's type choice.
	llm := fakeLLM{out: triggerDraftJSON("schedule", "")}
	res, err := Compile(context.Background(), llm, "Every weekday at 7am, do the thing.", Catalog{}, nil)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if res.Workflow.Trigger.Type != "schedule" {
		t.Fatalf("trigger type = %q, want schedule", res.Workflow.Trigger.Type)
	}
	if got, _ := res.Workflow.Trigger.Config["cron"].(string); got != "0 7 * * 1-5" {
		t.Errorf("cron = %q, want 0 7 * * 1-5", got)
	}
}

func TestNormalizeTrigger_DoesNotOverrideModelCron(t *testing.T) {
	// When the model already supplied a cron, inference must leave it alone.
	llm := fakeLLM{out: triggerDraftJSON("schedule", "30 6 * * 1")}
	res, err := Compile(context.Background(), llm, "Every morning at 8am.", Catalog{}, nil)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if got, _ := res.Workflow.Trigger.Config["cron"].(string); got != "30 6 * * 1" {
		t.Errorf("cron = %q, want the model's 30 6 * * 1 preserved", got)
	}
}

func TestNormalizeTrigger_ChannelAndWebhookFromPhrasing(t *testing.T) {
	cases := []struct {
		intent   string
		wantType string
	}{
		{"When someone messages me on Telegram, reply politely.", "channel"},
		{"On telegram, answer questions about the docs.", "channel"},
		{"Set up a webhook that posts incoming alerts to Slack.", "webhook"},
		{"When GitHub posts to our endpoint, summarize the PR.", "webhook"},
	}
	for _, tc := range cases {
		llm := fakeLLM{out: triggerDraftJSON("", "")}
		res, err := Compile(context.Background(), llm, tc.intent, Catalog{}, nil)
		if err != nil {
			t.Fatalf("Compile(%q): %v", tc.intent, err)
		}
		if res.Workflow.Trigger.Type != tc.wantType {
			t.Errorf("intent %q: trigger type = %q, want %q", tc.intent, res.Workflow.Trigger.Type, tc.wantType)
		}
	}
}
