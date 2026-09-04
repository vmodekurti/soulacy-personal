package runtime

import (
	"fmt"
	"strings"
	"time"
)

// FlowRunDiagnosis is a human-readable diagnosis of one retained workflow run.
// It is intentionally heuristic and deterministic: Studio can show useful
// guidance immediately without needing another LLM call just to understand a
// common platform failure.
type FlowRunDiagnosis struct {
	AgentID     string    `json:"agentId"`
	RunID       string    `json:"runId"`
	Trigger     string    `json:"trigger,omitempty"`
	Status      string    `json:"status"` // success, failed, empty
	Summary     string    `json:"summary"`
	FailedNode  string    `json:"failedNode,omitempty"`
	FailedKind  string    `json:"failedKind,omitempty"`
	Error       string    `json:"error,omitempty"`
	RootCause   string    `json:"rootCause,omitempty"`
	NextAction  string    `json:"nextAction,omitempty"`
	Suggestions []string  `json:"suggestions,omitempty"`
	Evidence    []string  `json:"evidence,omitempty"`
	Retryable   bool      `json:"retryable"`
	Steps       int       `json:"steps"`
	StartedAt   time.Time `json:"startedAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// DiagnoseFlowTrace classifies the first failing node in a retained flow trace.
// The rules are platform-level on purpose: they cover provider, channel, tool,
// JSON, timeout, permissions, and memory failures across generated workflows and
// ReAct-style tool runs that emit flow records.
func DiagnoseFlowTrace(tr FlowRunTrace) FlowRunDiagnosis {
	d := FlowRunDiagnosis{
		AgentID:   tr.AgentID,
		RunID:     tr.RunID,
		Trigger:   tr.Trigger,
		Status:    "success",
		Summary:   "Run completed without a recorded step error.",
		Steps:     len(tr.Entries),
		StartedAt: tr.StartedAt,
		UpdatedAt: tr.UpdatedAt,
		Retryable: false,
	}
	if len(tr.Entries) == 0 {
		d.Status = "empty"
		d.Summary = "No workflow steps were captured for this run."
		d.RootCause = "The run failed before the workflow emitted a trace, is still running, or did not use the traced runtime path."
		d.NextAction = "Open Activity for the same time window and check provider, channel, or gateway errors."
		d.Suggestions = []string{
			"Retry once after confirming the gateway is live.",
			"Check whether the selected agent uses a workflow or ReAct strategy that records tool steps.",
			"Review provider and channel configuration if Activity shows an error before the first tool call.",
		}
		d.Retryable = true
		return d
	}

	var failedIdx = -1
	var failedErr string
	for i, e := range tr.Entries {
		if err := flowNodeErrorText(e.Error, e.Output); err != "" {
			failedIdx = i
			failedErr = err
			break
		}
	}
	if failedIdx < 0 {
		d.Evidence = []string{lastStepEvidence(tr)}
		return d
	}

	step := tr.Entries[failedIdx]
	d.Status = "failed"
	d.Summary = "Run failed at node " + step.NodeID + "."
	d.FailedNode = step.NodeID
	d.FailedKind = step.Kind
	d.Error = failedErr
	d.Steps = failedIdx + 1
	d.Evidence = flowDiagnosisEvidence(tr, failedIdx, failedErr)

	root, next, retry, suggestions := classifyFlowError(failedErr)
	d.RootCause = root
	d.NextAction = next
	d.Retryable = retry
	d.Suggestions = suggestions
	return d
}

func flowNodeErrorText(err string, output []byte) string {
	if strings.TrimSpace(err) != "" {
		return strings.TrimSpace(err)
	}
	s := strings.TrimSpace(string(output))
	if s == "" {
		return ""
	}
	low := strings.ToLower(s)
	if strings.Contains(low, `"status":"error"`) ||
		strings.Contains(low, `"status":"failed"`) ||
		strings.Contains(low, `"ok":false`) {
		if msg := extractJSONishField(s, "error"); msg != "" {
			return msg
		}
		if msg := extractJSONishField(s, "message"); msg != "" {
			return msg
		}
		return s
	}
	return ""
}

func extractJSONishField(s, key string) string {
	pat := `"` + key + `":`
	i := strings.Index(s, pat)
	if i < 0 {
		return ""
	}
	rest := strings.TrimSpace(s[i+len(pat):])
	if !strings.HasPrefix(rest, `"`) {
		return ""
	}
	rest = rest[1:]
	var b strings.Builder
	esc := false
	for _, r := range rest {
		if esc {
			b.WriteRune(r)
			esc = false
			continue
		}
		if r == '\\' {
			esc = true
			continue
		}
		if r == '"' {
			break
		}
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

func flowDiagnosisEvidence(tr FlowRunTrace, idx int, err string) []string {
	step := tr.Entries[idx]
	ev := []string{
		"Failed node: " + step.NodeID,
		"Node kind: " + valueOr(step.Kind, "unknown"),
	}
	if step.DurationMS > 0 {
		ev = append(ev, "Duration: "+formatMillis(step.DurationMS))
	}
	if idx > 0 {
		prev := tr.Entries[idx-1]
		ev = append(ev, "Previous node: "+prev.NodeID)
	}
	if err != "" {
		ev = append(ev, "Error: "+truncateForDiagnosis(err, 260))
	}
	return ev
}

func lastStepEvidence(tr FlowRunTrace) string {
	if len(tr.Entries) == 0 {
		return "No steps captured."
	}
	last := tr.Entries[len(tr.Entries)-1]
	return "Last recorded node: " + last.NodeID
}

func classifyFlowError(err string) (root string, next string, retry bool, suggestions []string) {
	low := strings.ToLower(err)
	switch {
	case strings.Contains(low, "not valid json") || strings.Contains(low, "invalid character") || strings.Contains(low, "json"):
		return "A tool received arguments that were not valid JSON.",
			"Open the failing node and make its arguments a JSON object, or route typed outputs into declared input ports.",
			false,
			[]string{
				"Use a Python or LLM extraction node to produce structured JSON before the tool call.",
				"Prefer typed ports over hand-written templates for values passed between steps.",
				"Run Studio's integrity check after changing the node.",
			}
	case strings.Contains(low, " is required") || strings.Contains(low, "required") || strings.Contains(low, "missing"):
		return "A required field was missing from the tool or channel call.",
			"Fill the required field in the node inspector or configure a default destination for the channel.",
			false,
			[]string{
				"Check required parameters such as channel, to, text, queue, kb, model, or provider.",
				"For channel.send, make sure the channel has a default outbound destination or the node supplies one.",
				"Save and rerun after the required field is visible in the node arguments.",
			}
	case strings.Contains(low, "no such tool") || strings.Contains(low, "tool not found") || strings.Contains(low, "not installed"):
		return "The workflow references a tool, MCP server, or skill that is not available to the agent.",
			"Install or enable the missing capability, or replace the node with an available tool.",
			false,
			[]string{
				"Open the agent and confirm the tool appears in its allowed tools list.",
				"Check MCP server health if the missing tool name starts with mcp__.",
				"Regenerate the workflow after the capability is installed so Studio uses the real catalog.",
			}
	case strings.Contains(low, "requires the 'system' capability") || strings.Contains(low, "allow_system_agents") || strings.Contains(low, "authorization"):
		return "The workflow attempted a privileged operation without server authorization.",
			"Use a scoped built-in tool instead, or explicitly grant the system capability for this agent.",
			false,
			[]string{
				"Prefer knowledge-store or queue tools over shell/file writes for normal agents.",
				"If filesystem access is intended, configure an allowed workspace folder instead of broad system access.",
				"Treat this as a design decision, not a retryable runtime error.",
			}
	case strings.Contains(low, "401") || strings.Contains(low, "unauthorized") || strings.Contains(low, "credentials") || strings.Contains(low, "api key"):
		return "The provider or integration rejected the request because credentials were missing or invalid.",
			"Open Providers or Secrets Doctor and test the selected provider/channel before rerunning.",
			true,
			[]string{
				"Re-save the provider key and restart the gateway if the provider loads credentials at startup.",
				"Confirm the agent is using the provider/model you intended.",
				"Check whether a provider-specific key is required for this model.",
			}
	case strings.Contains(low, "400") || strings.Contains(low, "bad request") || strings.Contains(low, "top_p") || strings.Contains(low, "temperature") || strings.Contains(low, "tool_choice") || strings.Contains(low, "tool use"):
		return "The selected provider/model rejected one of the request parameters or tool-calling options.",
			"Use provider-compatible generation settings, or switch to a model that supports the requested tool mode.",
			false,
			[]string{
				"Remove conflicting tuning fields such as top_p plus temperature when a provider disallows the combination.",
				"Disable unsupported tool-choice options for this model.",
				"Use the provider test button after changing model settings.",
			}
	case strings.Contains(low, "timeout") || strings.Contains(low, "deadline") || strings.Contains(low, "context canceled"):
		return "The step exceeded its timeout or the run was cancelled while waiting on a slow operation.",
			"Increase the step timeout or reduce the payload before rerunning.",
			true,
			[]string{
				"Use chunking for long documents and large web pages.",
				"Move slow browser or scraping work to a remote executor when possible.",
				"Check provider latency before increasing max turns.",
			}
	case strings.Contains(low, "memory") || strings.Contains(low, "out of memory") || strings.Contains(low, "killed"):
		return "The run likely exhausted memory while processing a large payload.",
			"Chunk or queue the artifact instead of loading the full content into one step.",
			false,
			[]string{
				"Use streaming ingestion for documents and URLs.",
				"Keep attachments in the resource store and pass references between steps.",
				"Set smaller chunk sizes for embedding and summarization.",
			}
	default:
		return "The run failed inside a tool, provider, or workflow step.",
			"Open the failing node details and use the error plus input/output to decide whether to repair the node or retry.",
			true,
			[]string{
				"Check whether the input reaching the failing node matches what the tool expects.",
				"Look at the previous node's output for missing or malformed fields.",
				"Use Self-correct workflow if the same failure repeats after a simple retry.",
			}
	}
}

func valueOr(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}

func formatMillis(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	return (time.Duration(ms) * time.Millisecond).String()
}

func truncateForDiagnosis(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

// FlowRunDiagnosis returns a diagnosis for a retained run. If runID is empty,
// the latest run for the agent is diagnosed.
func (e *Engine) FlowRunDiagnosis(agentID, runID string) (FlowRunDiagnosis, bool) {
	var tr FlowRunTrace
	var ok bool
	if runID != "" {
		tr, ok = e.FlowTraceFor(agentID, runID)
	} else {
		tr, ok = e.LatestFlowTrace(agentID)
	}
	if !ok {
		return FlowRunDiagnosis{}, false
	}
	return DiagnoseFlowTrace(tr), true
}
