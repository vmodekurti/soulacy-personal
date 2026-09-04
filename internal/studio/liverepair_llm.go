package studio

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// liverepair_llm.go is the model-backed half of live repair: when a shape drift
// has no confident deterministic fix (novel API shape, tangled python parsing),
// hand the model the failing node plus a REDACTED sample of the real output and
// ask it to rewrite exactly one field. The model never sees the whole trace, and
// large/sensitive payloads are truncated (redactSample) before they leave.

// ProposeLiveRepairs is the orchestrator: for every failed node in the trace it
// classifies the failure, tries a deterministic adapter first, and falls back to
// an LLM rewrite for shape/template drift. tool_failure and empty_result are
// surfaced advisory-only (no code rewrite). Nothing is applied — proposals are
// returned for approval. A nil llm still yields deterministic proposals.
func ProposeLiveRepairs(ctx context.Context, llm LLM, draft Draft, runs []LiveNodeRun) []RepairProposal {
	var out []RepairProposal
	for _, r := range runs {
		class := Classify(r)
		if class == RepairNone {
			continue
		}
		if class == RepairShapeDrift {
			diag := Diagnose(runs, r)
			if p, ok := ProposeAdapter(r, diag); ok {
				out = append(out, p)
				continue
			}
			if llm != nil {
				if p, ok := llmRepair(ctx, llm, draft, r, diag, class); ok {
					out = append(out, p)
					continue
				}
			}
			// No fix synthesizable: still report the observed shape so the user
			// can hand-adjust.
			out = append(out, advisory(r, class, diag))
			continue
		}
		if class == RepairTemplateError && llm != nil {
			if p, ok := llmRepair(ctx, llm, draft, r, ShapeDiagnosis{}, class); ok {
				out = append(out, p)
				continue
			}
		}
		out = append(out, advisory(r, class, ShapeDiagnosis{}))
	}
	return out
}

func advisory(r LiveNodeRun, class RepairClass, d ShapeDiagnosis) RepairProposal {
	rationale := "This looks like a real tool/API failure (auth, rate limit, or network), not a formatting mismatch — check credentials and the endpoint."
	switch class {
	case RepairShapeDrift:
		rationale = "The data shape differs from what the node expects, but no automatic remap was confident. "
		if len(d.ObservedKeys) > 0 {
			rationale += "The upstream output actually contains: " + strings.Join(d.ObservedKeys, ", ") + "."
		}
	case RepairEmptyResult:
		rationale = "The upstream produced an empty result; add an empty-case branch or a fallback message."
	case RepairTemplateError:
		rationale = "The input template has a syntax or unknown-function error; fix the template expression."
	}
	return RepairProposal{
		NodeID: r.NodeID, Field: r.Field(), Class: class,
		Old: currentField(r), Rationale: rationale, Auto: false, ObservedKeys: d.ObservedKeys,
	}
}

func currentField(r LiveNodeRun) string {
	return r.Input
}

func llmRepair(ctx context.Context, llm LLM, draft Draft, r LiveNodeRun, d ShapeDiagnosis, class RepairClass) (RepairProposal, bool) {
	prompt := BuildLiveRepairPrompt(draft, r, d, class)
	raw, err := llm.Complete(ctx, prompt)
	if err != nil {
		return RepairProposal{}, false
	}
	field, value, ok := parseRepairReply(raw)
	if !ok || strings.TrimSpace(value) == "" {
		return RepairProposal{}, false
	}
	// The model may only repair the field this node actually owns.
	if want := r.Field(); want != "" && field != want {
		field = want
	}
	old := ""
	if field == "input" {
		old = r.Input
	} else if field == "code" {
		old = nodeCode(draft, r.NodeID)
	}
	if value == old {
		return RepairProposal{}, false
	}
	rationale := "Rewritten to match the real output shape observed in the last run."
	if field == "code" {
		rationale = "Rewrote the script to parse the real input it actually received in the last run (defensively)."
	}
	return RepairProposal{
		NodeID: r.NodeID, Field: field, Class: class,
		Old: old, New: value, Auto: false, ObservedKeys: d.ObservedKeys,
		Rationale: rationale,
	}, true
}

// BuildLiveRepairPrompt asks the model to rewrite one node's field against the
// real, redacted output sample. It demands a strict JSON reply so parsing is
// deterministic.
func BuildLiveRepairPrompt(draft Draft, r LiveNodeRun, d ShapeDiagnosis, class RepairClass) string {
	var sb strings.Builder
	sb.WriteString("You are repairing ONE node in a running workflow. A live execution produced a wrong result because the node's assumptions did not match the REAL data it received from an upstream tool/API.\n\n")
	sb.WriteString("Node id: " + r.NodeID + "\nNode kind: " + r.Kind + "\n")
	sb.WriteString("Failure class: " + string(class) + "\n")
	sb.WriteString("Error (may be reported in the node's OUTPUT rather than as a crash):\n" + EffectiveError(r) + "\n\n")
	if r.Field() == "code" {
		// Python node: show the CURRENT code and the REAL input it received, so the
		// model can rewrite parsing to match reality (e.g. an upstream tool that
		// returns an HTTP response with headers before the JSON body).
		if code := nodeCode(draft, r.NodeID); code != "" {
			sb.WriteString("Current python code (def run(inputs)):\n" + code + "\n\n")
		}
		if in := strings.TrimSpace(r.Input); in != "" {
			sb.WriteString("The ACTUAL input this code received last run (parse THIS shape — it may be a string, may have headers/prefix before JSON, may be wrapped):\n")
			sb.WriteString(string(redactSample(json.RawMessage(in), 6, 400)) + "\n\n")
		}
		sb.WriteString("Rewrite `def run(inputs):` to read the input DEFENSIVELY: locate/parse the JSON even if it's embedded in a larger string (e.g. slice from the first '{' or split on a blank line), tolerate missing keys, and never raise — return a sensible fallback on bad input.\n")
	} else {
		sb.WriteString("Current input template (Go text/template over flow vars):\n" + r.Input + "\n\n")
	}
	if len(d.ObservedKeys) > 0 {
		sb.WriteString("The upstream output ACTUALLY has these top-level keys: " + strings.Join(d.ObservedKeys, ", ") + "\n")
	}
	if d.ArrayKey != "" {
		sb.WriteString("A list/array is present under key: " + d.ArrayKey + "\n")
	}
	if d.StringWrapped {
		sb.WriteString("NOTE: the upstream value is a JSON STRING that wraps JSON; parse it (fromJson in templates, json.loads in python) before field access.\n")
	}
	if r.Field() != "code" && len(r.Output) > 0 {
		sb.WriteString("\nRedacted sample of the REAL upstream/node output:\n")
		sb.WriteString(string(redactSample(r.Output, 6, 240)) + "\n")
	}
	sb.WriteString("\nRewrite ONLY this node's ")
	if r.Field() == "code" {
		sb.WriteString("python code")
	} else {
		sb.WriteString("input template")
	}
	sb.WriteString(" so it works against the real shape. Be defensive (tolerate missing keys, empty lists). Do NOT change any other node.\n")
	sb.WriteString("Reply with STRICT JSON only, no prose:\n{\"field\": \"" + fieldOrDefault(r) + "\", \"value\": \"<the rewritten " + fieldOrDefault(r) + ">\"}\n")
	return sb.String()
}

func fieldOrDefault(r LiveNodeRun) string {
	if f := r.Field(); f != "" {
		return f
	}
	return "input"
}

// parseRepairReply extracts {"field","value"} from the model reply, tolerating a
// code fence.
func parseRepairReply(raw string) (field, value string, ok bool) {
	s := stripFences(strings.TrimSpace(raw))
	var m struct {
		Field string `json:"field"`
		Value string `json:"value"`
	}
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return "", "", false
	}
	f := strings.TrimSpace(m.Field)
	if f != "input" && f != "code" {
		f = "input"
	}
	return f, m.Value, true
}

// redactSample truncates a JSON value for safe, compact inclusion in a prompt:
// arrays keep the first maxItems elements, strings are capped at maxStr runes,
// and nested structures recurse. Preserves SHAPE (keys, types) while dropping
// bulk/sensitive content.
func redactSample(raw json.RawMessage, maxItems, maxStr int) json.RawMessage {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return capString(string(raw), maxStr)
	}
	red := redactValue(v, maxItems, maxStr)
	b, err := json.Marshal(red)
	if err != nil {
		return raw
	}
	return b
}

func redactValue(v any, maxItems, maxStr int) any {
	switch t := v.(type) {
	case string:
		if len([]rune(t)) > maxStr {
			return string([]rune(t)[:maxStr]) + "…"
		}
		return t
	case []any:
		n := len(t)
		lim := n
		if lim > maxItems {
			lim = maxItems
		}
		out := make([]any, 0, lim+1)
		for i := 0; i < lim; i++ {
			out = append(out, redactValue(t[i], maxItems, maxStr))
		}
		if n > lim {
			out = append(out, fmt.Sprintf("…(+%d more of %d)", n-lim, n))
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = redactValue(val, maxItems, maxStr)
		}
		return out
	default:
		return v
	}
}

func capString(s string, maxStr int) json.RawMessage {
	if len([]rune(s)) > maxStr {
		s = string([]rune(s)[:maxStr]) + "…"
	}
	b, _ := json.Marshal(s)
	return b
}
