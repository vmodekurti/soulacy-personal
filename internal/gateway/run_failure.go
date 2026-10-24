package gateway

import (
	"github.com/soulacy/soulacy/internal/runfailure"
	"github.com/soulacy/soulacy/pkg/message"
)

// Reconstruct from durable observations so existing failed jobs gain the same
// explanation as new runs. Completion checks alone do not establish a cause.
func diagnoseRunFailure(events []message.Event, status, summary, deliveryError string) *runfailure.Diagnosis {
	if status != "failed" && status != "degraded" {
		return nil
	}
	var recorder runfailure.Recorder
	for _, ev := range events {
		p := payloadMap(ev.Payload)
		if inner := payloadMap(p["tool_result"]); inner != nil {
			p = inner
		}
		switch ev.Type {
		case "tool.result":
			recorder.Observe(stringField(p, "name"), runfailure.ToolError(stringField(p, "content"), isToolError(p)))
		case "warn":
			if stringField(p, "stage") == "required-tool-recovery" {
				recorder.Observe(stringField(p, "required_tool"), stringField(p, "error"))
			}
		case "error":
			recorder.Observe(studioFirstNonEmpty(stringField(p, "stage"), "execution"), payloadErrorText(p))
		}
	}
	if deliveryError != "" {
		recorder.Observe("delivery", deliveryError)
	}
	return recorder.Diagnosis(summary)
}
