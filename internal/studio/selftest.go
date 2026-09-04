// selftest.go — self-test synthesis (Architect pillar #4).
//
// "It works" should be proven, not assumed. Given the intent and the generated
// draft, SynthesizeTests asks the builder model for a few representative inputs
// plus the success criteria that must hold after a run. The build loop then
// exercises those through its Verifier, so a green build means the agent
// actually produced the right kind of output — and a failing assertion becomes a
// concrete problem the repair loop can act on.
//
// Best-effort and self-contained: any model/parse failure yields no tests (the
// loop falls back to a single "just don't error" run), never a hard error.
package studio

import (
	"context"
	"encoding/json"
	"strings"
)

// SynthesizeTests generates a small set of self-tests for a draft. It returns at
// most 3 cases. nil is returned (not an error) when the model is unavailable or
// its output can't be parsed.
func SynthesizeTests(ctx context.Context, llm LLM, intent string, draft Draft, cat Catalog) []TestCase {
	if llm == nil {
		return nil
	}
	raw, err := llm.Complete(ctx, buildSelfTestPrompt(intent, draft))
	if err != nil {
		return nil
	}
	return parseSelfTests(raw)
}

// buildSelfTestPrompt asks for representative inputs + assertions as strict JSON.
func buildSelfTestPrompt(intent string, draft Draft) string {
	var sb strings.Builder
	sb.WriteString("You are writing acceptance tests for an automation an end user described.\n")
	sb.WriteString("Intent: ")
	sb.WriteString(strings.TrimSpace(intent))
	sb.WriteString("\n\n")
	if b, err := json.Marshal(draftTestSummary(draft)); err == nil {
		sb.WriteString("The automation being tested (summary):\n")
		sb.Write(b)
		sb.WriteString("\n\n")
	}
	sb.WriteString("Produce 1 to 3 tests. Each test has:\n")
	sb.WriteString("- \"input\": a realistic trigger input string (what arrives when the automation runs). Use \"\" for a scheduled/no-input automation.\n")
	sb.WriteString("- \"assertions\": 1-2 checks on the FINAL output. Each: {\"target\":\"result\",\"op\":\"exists|contains\",\"value\":\"...\"}. Use \"exists\" to require non-empty output; \"contains\" with a short expected substring when you can predict one. Keep values short and robust — do not assert on volatile specifics (dates, counts).\n\n")
	sb.WriteString("Return ONLY this JSON, no prose, no code fences:\n")
	sb.WriteString("{\"tests\":[{\"input\":\"\",\"assertions\":[{\"target\":\"result\",\"op\":\"exists\",\"value\":\"\"}]}]}\n")
	return sb.String()
}

// draftTestSummary is the compact, side-effect-free view of a draft handed to
// the test-writer model: name, intent, trigger, and the step/tool/agent names.
func draftTestSummary(d Draft) map[string]any {
	steps := make([]map[string]string, 0, len(d.Flow.Nodes))
	for _, n := range d.Flow.Nodes {
		steps = append(steps, map[string]string{"id": n.ID, "tool": n.Tool, "agent": n.Agent})
	}
	return map[string]any{
		"name":     d.Name,
		"strategy": d.Strategy,
		"trigger":  d.Trigger.Type,
		"tools":    d.Tools,
		"steps":    steps,
	}
}

// parseSelfTests extracts the {"tests":[...]} payload, tolerant of fences/prose,
// and clamps to at most 3 valid cases (an assertion needs a target+op).
func parseSelfTests(raw string) []TestCase {
	s := stripFences(strings.TrimSpace(raw))
	start := strings.IndexByte(s, '{')
	end := strings.LastIndexByte(s, '}')
	if start < 0 || end < 0 || end < start {
		return nil
	}
	var payload struct {
		Tests []TestCase `json:"tests"`
	}
	if err := json.Unmarshal([]byte(s[start:end+1]), &payload); err != nil {
		return nil
	}
	var out []TestCase
	for _, tc := range payload.Tests {
		clean := TestCase{Input: tc.Input}
		for _, a := range tc.Assertions {
			if strings.TrimSpace(a.Target) == "" || strings.TrimSpace(a.Op) == "" {
				continue
			}
			clean.Assertions = append(clean.Assertions, a)
		}
		out = append(out, clean)
		if len(out) == 3 {
			break
		}
	}
	return out
}
