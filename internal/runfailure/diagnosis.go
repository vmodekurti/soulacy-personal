// Package runfailure describes observed failures without asking an LLM to
// invent a cause. Shared by live replies and reconstructed historical runs.
package runfailure

import (
	"encoding/json"
	"strings"

	"github.com/soulacy/soulacy/internal/trust"
)

type Diagnosis struct {
	Summary    string   `json:"summary"`
	Step       string   `json:"step,omitempty"`
	Detail     string   `json:"detail,omitempty"`
	NextAction string   `json:"nextAction"`
	Checks     []string `json:"checks,omitempty"`
}

// Recorder retains unresolved failures in observation order. A later success
// for a tool clears its error so a recovered error is not presented as a cause.
// Callers provide synchronization when observers can run concurrently.
type Recorder struct{ failures []*Diagnosis }

func (r *Recorder) Observe(step, detail string) {
	kept := r.failures[:0]
	for _, d := range r.failures {
		if d.Step != step {
			kept = append(kept, d)
		}
	}
	r.failures = kept
	if strings.TrimSpace(detail) != "" {
		r.failures = append(r.failures, Observed(step, detail))
	}
}

func (r *Recorder) Diagnosis(summary string) *Diagnosis {
	d := Incomplete(summary)
	if len(r.failures) > 0 {
		copy := *r.failures[len(r.failures)-1]
		d = &copy
	}
	if strings.Contains(summary, "mission acceptance checks failed:") {
		summary = strings.TrimPrefix(summary, "mission acceptance checks failed:")
	}
	if strings.Contains(summary, "required tool") || strings.Contains(summary, "final output did not match") {
		for _, check := range strings.Split(summary, ";") {
			if strings.TrimSpace(check) != "" {
				d.Checks = append(d.Checks, strings.TrimSpace(check))
			}
		}
	}
	return d
}

func ToolError(content string, failed bool) string {
	if envelope, ok := trust.Extract(content); ok {
		content = envelope.Body
	}
	content = strings.TrimSpace(content)
	// A successful status lookup may report a failed background job. Lists of
	// old jobs are deliberately excluded: they are not failures of this run.
	var result map[string]any
	if json.Unmarshal([]byte(content), &result) == nil {
		status, _ := result["status"].(string)
		if status == "" {
			status, _ = result["job_status"].(string)
		}
		if failed || status == "failed" || status == "error" {
			for _, key := range []string{"error", "error_message", "failure_reason", "detail"} {
				if detail, ok := result[key].(string); ok && strings.TrimSpace(detail) != "" {
					return detail
				}
			}
		}
		if !failed {
			return ""
		}
	}
	if failed {
		return content
	}
	return ""
}

func Observed(step, detail string) *Diagnosis {
	d := &Diagnosis{
		Summary: "The run encountered an error in " + step + ".",
		Step:    step, Detail: detail,
		NextAction: "Check the reported error for this step, correct its configuration or inputs, then run the job again.",
	}
	lower := strings.ToLower(detail)
	switch {
	case strings.Contains(lower, "schema-valid arguments"), strings.Contains(lower, "invalid json"), strings.Contains(lower, "missing required argument"):
		d.Summary = "The model could not provide valid inputs for " + step + "."
		d.NextAction = "Check that this step belongs in the job, then retry. If it repeats, use a model that reliably calls tools."
	case strings.Contains(lower, "model") && (strings.Contains(lower, "not found") || strings.Contains(lower, "does not exist")):
		d.Summary = "A configured model was not found."
		d.NextAction = "Select an installed, available model in the service used by this step, then retry."
		if strings.Contains(step, "open_notebook") {
			d.NextAction = "In Open Notebook, update the podcast profile to use an available model, then run the job again."
		}
	case strings.Contains(lower, "401"), strings.Contains(lower, "unauthorized"), strings.Contains(lower, "session expired"):
		d.Summary = "The service rejected authentication for " + step + "."
		d.NextAction = "Reconnect the affected account or refresh its saved login, then retry."
	case strings.Contains(lower, "not permitted"), strings.Contains(lower, "insufficient permissions"):
		d.NextAction = "Review the job’s allowed tools and permissions for this step before retrying."
	case strings.Contains(lower, "timeout"), strings.Contains(lower, "timed out"), strings.Contains(lower, "connection refused"):
		d.NextAction = "Check that the service used by this step is running and reachable, then retry."
	}
	return d
}

func Incomplete(summary string) *Diagnosis {
	d := &Diagnosis{
		Summary:    "The run stopped before completing its required steps.",
		Detail:     summary,
		NextAction: "Review the missing steps and the job’s instructions, then retry.",
	}
	if strings.Contains(summary, "required tool was not observed") {
		d.Summary = "The run ended without calling one or more required tools."
		d.NextAction = "Check that the required tools match the job’s intended workflow and are available to it, then retry."
	} else if strings.Contains(summary, "final output did not match") {
		d.Summary = "The run ended without the required final result."
	}
	return d
}
