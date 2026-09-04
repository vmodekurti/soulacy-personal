package runtime

// outcome.go — evaluate an agent's business-outcome contract against a REAL run
// (P0-4), and let the verdict affect the run's status (P0-6).
//
// The gap this closes: assertions existed only inside Studio's build loop. Once
// an agent was saved, the single question asked of a production run was "did a
// node return an error." A workflow that searched zero sources, generated no
// audio, and delivered an empty message answered "no" and was recorded as a
// success. The operator learned otherwise from the silence on Telegram.
//
// Evaluation here is deliberately independent of Studio: the contract lives on
// agent.Definition (pkg/agent/outcome.go), the evaluator is pure, and the
// runtime consults it after the flow has finished. Studio and the engine agree
// on operator semantics because they share the vocabulary, not the code path.

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/soulacy/soulacy/internal/reasoning"
	"github.com/soulacy/soulacy/pkg/agent"
)

// outcomeCollectorKey carries a slot the flow run writes its contract verdict
// into, so the caller that BUILDS the reply (Engine.Handle) can mark it
// degraded. A context collector rather than a return-value change because
// WorkflowExecutor.Run's signature is shared by the step-based executor, which
// has no contract concept — mirrors WithFlowNodeObserver.
type outcomeCollectorKey struct{}

// WithOutcomeCollector returns a context whose flow run records its outcome
// report into *slot. Nil slot is a no-op.
func WithOutcomeCollector(ctx context.Context, slot *OutcomeReport) context.Context {
	if slot == nil {
		return ctx
	}
	return context.WithValue(ctx, outcomeCollectorKey{}, slot)
}

func outcomeCollectorFrom(ctx context.Context) *OutcomeReport {
	slot, _ := ctx.Value(outcomeCollectorKey{}).(*OutcomeReport)
	return slot
}

// Outcome classes, mirroring internal/studio.Outcome. Duplicated as plain
// strings rather than imported because internal/studio is a BUILD-time package
// and the runtime must not depend on it.
const (
	OutcomeComplete = "complete"
	OutcomePartial  = "partial"
	OutcomeEmpty    = "empty"
	OutcomeFailed   = "failed"
)

// OutcomeAssertionResult is one evaluated assertion from a production run.
type OutcomeAssertionResult struct {
	Target   string `json:"target"`
	Op       string `json:"op"`
	Value    string `json:"value,omitempty"`
	Describe string `json:"describe,omitempty"`
	Pass     bool   `json:"pass"`
	Outcome  string `json:"outcome"`
	Detail   string `json:"detail"`
}

// OutcomeReport is the verdict for one run against its contract.
type OutcomeReport struct {
	Outcome    string                   `json:"outcome"`
	Assertions []OutcomeAssertionResult `json:"assertions"`
	// Met is true only when every assertion passed. It is what a certification
	// gate and the scheduler's delivery decision consult.
	Met bool `json:"met"`
	// Summary is a one-line, user-facing explanation of an unmet contract.
	Summary string `json:"summary,omitempty"`
}

// EvaluateOutcome judges a finished run against its agent's contract.
//
// final is the flow's final output; trace supplies each node's output so an
// assertion can target a specific step (e.g. the add_source_pack node) rather
// than only the run's last value. A nil/empty contract returns a zero report
// with Met=true — no contract means nothing to fail.
func EvaluateOutcome(contract *agent.OutcomeContract, final json.RawMessage, trace []reasoning.FlowNodeRun) OutcomeReport {
	if !contract.HasAssertions() {
		return OutcomeReport{Outcome: OutcomeComplete, Met: true}
	}
	byNode := make(map[string]json.RawMessage, len(trace))
	for _, rec := range trace {
		// Later visits of a cyclic node overwrite earlier ones: the last value
		// is what the run actually ended up with.
		byNode[rec.NodeID] = rec.Output
	}

	report := OutcomeReport{Met: true}
	worst := OutcomeComplete
	passed := 0
	for _, a := range contract.Assertions {
		raw, found := final, true
		if a.Target != "" && a.Target != "result" {
			raw, found = byNode[a.Target]
		}
		res := evalOutcomeAssertion(a, raw, found)
		if res.Pass {
			passed++
		} else {
			report.Met = false
		}
		if outcomeSeverity(res.Outcome) > outcomeSeverity(worst) {
			worst = res.Outcome
		}
		report.Assertions = append(report.Assertions, res)
	}
	// Some met and some not, with nothing worse → partial.
	if worst == OutcomeFailed && passed > 0 {
		worst = OutcomePartial
	}
	report.Outcome = worst
	if !report.Met {
		report.Summary = summarizeUnmet(report.Assertions)
	}
	return report
}

func outcomeSeverity(o string) int {
	switch o {
	case OutcomeFailed:
		return 3
	case OutcomeEmpty:
		return 2
	case OutcomePartial:
		return 1
	default:
		return 0
	}
}

// summarizeUnmet renders the failed assertions in the author's own words where
// a Describe was supplied, so an operator reading a notification is told what
// the agent was meant to do — not handed an operator name and a JSON path.
func summarizeUnmet(results []OutcomeAssertionResult) string {
	var parts []string
	for _, r := range results {
		if r.Pass {
			continue
		}
		if strings.TrimSpace(r.Describe) != "" {
			parts = append(parts, r.Describe+" — "+r.Detail)
		} else {
			parts = append(parts, r.Detail)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	if len(parts) == 1 {
		return parts[0]
	}
	return strings.Join(parts, "; ")
}

// evalOutcomeAssertion evaluates one assertion against a target's raw output.
func evalOutcomeAssertion(a agent.OutcomeAssertion, raw json.RawMessage, found bool) OutcomeAssertionResult {
	res := OutcomeAssertionResult{Target: a.Target, Op: a.Op, Value: a.Value, Describe: a.Describe}
	if !found {
		res.Outcome = OutcomeFailed
		res.Detail = fmt.Sprintf("step %q did not run", a.Target)
		return res
	}
	decoded, decodedOK := decodeOutcomeTarget(raw)

	switch a.Op {
	case "exists":
		res.Pass = decodedOK && outcomeCount(decoded) > 0
		if res.Pass {
			res.Detail = fmt.Sprintf("%q produced output", targetLabel(a.Target))
		} else {
			res.Outcome, res.Detail = OutcomeEmpty, fmt.Sprintf("%q produced no output", targetLabel(a.Target))
		}

	case "not_empty":
		n := 0
		if decodedOK {
			n = outcomeCount(decoded)
		}
		res.Pass = n > 0
		if res.Pass {
			res.Detail = fmt.Sprintf("%q produced %d item(s)", targetLabel(a.Target), n)
		} else {
			res.Outcome, res.Detail = OutcomeEmpty, fmt.Sprintf("%q produced an empty result", targetLabel(a.Target))
		}

	case "contains", "equals":
		got := outcomeString(raw)
		if a.Op == "contains" {
			res.Pass = strings.Contains(got, a.Value)
		} else {
			res.Pass = strings.TrimSpace(got) == strings.TrimSpace(a.Value)
		}
		if res.Pass {
			res.Detail = fmt.Sprintf("%q %s %q", targetLabel(a.Target), a.Op, a.Value)
		} else if strings.TrimSpace(got) == "" {
			res.Outcome, res.Detail = OutcomeEmpty, fmt.Sprintf("%q produced no output", targetLabel(a.Target))
		} else {
			res.Detail = fmt.Sprintf("%q does not %s %q", targetLabel(a.Target), a.Op, a.Value)
		}

	case "count_gte", "count_eq":
		want, err := strconv.Atoi(strings.TrimSpace(a.Value))
		if err != nil {
			res.Outcome, res.Detail = OutcomeFailed, fmt.Sprintf("assertion value %q is not a number", a.Value)
			return res
		}
		got := 0
		if decodedOK {
			got = outcomeCollectionCount(decoded)
		}
		if a.Op == "count_eq" {
			res.Pass = got == want
		} else {
			res.Pass = got >= want
		}
		switch {
		case res.Pass:
			res.Detail = fmt.Sprintf("%q has %d item(s)", targetLabel(a.Target), got)
		case got == 0:
			res.Outcome, res.Detail = OutcomeEmpty, fmt.Sprintf("%q produced 0 items, expected %d", targetLabel(a.Target), want)
		default:
			res.Outcome, res.Detail = OutcomePartial, fmt.Sprintf("%q produced %d item(s), expected %d", targetLabel(a.Target), got, want)
		}

	case "has_field":
		if decodedOK {
			if _, ok := outcomePath(decoded, a.Value); ok {
				res.Pass = true
				res.Detail = fmt.Sprintf("%q has %q", targetLabel(a.Target), a.Value)
			}
		}
		if !res.Pass {
			res.Outcome, res.Detail = OutcomeFailed, fmt.Sprintf("%q is missing %q", targetLabel(a.Target), a.Value)
		}

	case "field_equals":
		path, want, cut := strings.Cut(a.Value, "=")
		if !cut {
			res.Outcome, res.Detail = OutcomeFailed, fmt.Sprintf("field_equals value %q must be \"path=expected\"", a.Value)
			return res
		}
		got, ok := any(nil), false
		if decodedOK {
			got, ok = outcomePath(decoded, path)
		}
		if !ok {
			res.Outcome, res.Detail = OutcomeFailed, fmt.Sprintf("%q is missing %q", targetLabel(a.Target), path)
			return res
		}
		gotStr := strings.TrimSpace(fmt.Sprint(got))
		res.Pass = gotStr == strings.TrimSpace(want)
		if res.Pass {
			res.Detail = fmt.Sprintf("%s = %s", path, gotStr)
		} else {
			res.Outcome, res.Detail = OutcomeFailed, fmt.Sprintf("%s = %s, expected %s", path, gotStr, strings.TrimSpace(want))
		}

	case "delivered", "destination":
		m, ok := decoded.(map[string]any)
		if !decodedOK || !ok {
			res.Outcome, res.Detail = OutcomeFailed, fmt.Sprintf("%q is not a delivery result", targetLabel(a.Target))
			return res
		}
		okFlag, _ := m["ok"].(bool)
		if d, has := m["delivered"].(bool); has {
			okFlag = okFlag || d
		}
		channel := outcomeFirstString(m, "channel")
		to := outcomeFirstString(m, "to", "destination", "chat_id", "recipient")
		if a.Op == "delivered" {
			switch {
			case !okFlag:
				res.Outcome, res.Detail = OutcomeFailed, fmt.Sprintf("%q did not deliver", targetLabel(a.Target))
			case strings.TrimSpace(a.Value) != "" && !strings.EqualFold(a.Value, channel):
				res.Outcome, res.Detail = OutcomeFailed, fmt.Sprintf("delivered via %q, expected %q", channel, a.Value)
			default:
				res.Pass, res.Detail = true, fmt.Sprintf("delivered via %q to %q", channel, to)
			}
			break
		}
		switch {
		case to == "":
			res.Outcome, res.Detail = OutcomeFailed, fmt.Sprintf("%q records no destination", targetLabel(a.Target))
		case strings.TrimSpace(a.Value) != "" && strings.TrimSpace(a.Value) != to:
			// Sent successfully, to the wrong place.
			res.Outcome, res.Detail = OutcomeFailed, fmt.Sprintf("delivered to %q, expected %q", to, a.Value)
		default:
			res.Pass, res.Detail = true, fmt.Sprintf("destination is %q", to)
		}

	case "artifact":
		m, ok := decoded.(map[string]any)
		if !decodedOK || !ok {
			res.Outcome, res.Detail = OutcomeFailed, fmt.Sprintf("%q is not an artifact result", targetLabel(a.Target))
			return res
		}
		id := outcomeFirstString(m, "artifact_id", "audio_url", "artifact_url", "url", "file_id", "id")
		if id == "" {
			if arr, isArr := m["artifacts"].([]any); isArr && len(arr) > 0 {
				id = fmt.Sprintf("%d artifact(s)", len(arr))
			}
		}
		status := outcomeFirstString(m, "artifact_status", "status", "state")
		switch {
		case id == "":
			res.Outcome, res.Detail = OutcomeEmpty, fmt.Sprintf("%q produced no artifact", targetLabel(a.Target))
		case status != "" && !outcomeTerminalStatus(status):
			// Still working is PARTIAL: the run did its part, the provider
			// hasn't finished. Reporting it as success is the bug.
			res.Outcome, res.Detail = OutcomePartial, fmt.Sprintf("artifact %s is still %q", id, status)
		default:
			res.Pass, res.Detail = true, fmt.Sprintf("artifact %s is ready", id)
		}

	default:
		res.Outcome, res.Detail = OutcomeFailed, fmt.Sprintf("unknown assertion op %q", a.Op)
	}

	if res.Outcome == "" {
		if res.Pass {
			res.Outcome = OutcomeComplete
		} else {
			res.Outcome = OutcomeFailed
		}
	}
	return res
}

func targetLabel(t string) string {
	if strings.TrimSpace(t) == "" || t == "result" {
		return "the run result"
	}
	return t
}

func decodeOutcomeTarget(raw json.RawMessage) (any, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, false
	}
	if s, ok := v.(string); ok {
		t := strings.TrimSpace(s)
		if strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[") {
			var inner any
			if json.Unmarshal([]byte(t), &inner) == nil {
				return inner, true
			}
		}
	}
	return v, true
}

func outcomeString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return string(raw)
}

func outcomeCount(v any) int {
	switch t := v.(type) {
	case nil:
		return 0
	case []any:
		return len(t)
	case map[string]any:
		return len(t)
	case string:
		if strings.TrimSpace(t) == "" {
			return 0
		}
		return 1
	default:
		return 1
	}
}

// outcomeCollectionCount prefers an obvious collection field inside an object
// over the object's key count, which is almost never what an author means by
// "at least 3".
func outcomeCollectionCount(v any) int {
	if m, ok := v.(map[string]any); ok {
		for _, key := range []string{"results", "items", "sources", "artifacts", "data", "records"} {
			if inner, present := m[key]; present {
				return outcomeCount(inner)
			}
		}
	}
	return outcomeCount(v)
}

func outcomePath(v any, path string) (any, bool) {
	cur := v
	for _, seg := range strings.Split(strings.TrimSpace(path), ".") {
		if seg == "" {
			continue
		}
		switch t := cur.(type) {
		case map[string]any:
			next, ok := t[seg]
			if !ok {
				return nil, false
			}
			cur = next
		case []any:
			i, err := strconv.Atoi(seg)
			if err != nil || i < 0 || i >= len(t) {
				return nil, false
			}
			cur = t[i]
		default:
			return nil, false
		}
	}
	if cur == nil {
		return nil, false
	}
	return cur, true
}

func outcomeFirstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := m[k].(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// outcomeTerminalStatus reports whether a status means "finished". Unknown
// statuses count as terminal so a provider inventing a new success word cannot
// make the assertion hang on failure.
func outcomeTerminalStatus(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "pending", "processing", "queued", "running", "in_progress", "in progress", "started", "unknown":
		return false
	default:
		return true
	}
}
