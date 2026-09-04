package studio

// outcome.go — P0-4 "Business-Outcome Assertions".
//
// The original assertion vocabulary was `contains | equals | exists` matched
// against a stringified blob. That can express "the run produced some text" but
// not "three sources were added", "the audio artifact finished", or "the link
// reached the configured Telegram chat" — so an agent could pass every
// assertion it had while delivering nothing. Worse, the assertion GENERATOR
// falls back to `exists` whenever it can't predict a substring, which made the
// weakest possible assertion also the most common one.
//
// This file adds two things:
//
//  1. Operators that address STRUCTURE rather than substrings — counts, field
//     presence at a JSON path, delivery status, destination, artifact state.
//     They resolve against decoded JSON, so "3 items" is a number and not the
//     accident of a substring appearing in a serialized blob.
//
//  2. An outcome CLASSIFICATION. "Did it error" and "did it achieve the goal"
//     are different questions, and a single pass/fail boolean cannot express
//     the case that matters most in production: every node ran, nothing errored,
//     and the result is empty. That is Empty, not Complete — and it must not be
//     reported as success.

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Outcome classifies what a run actually achieved, independent of whether its
// nodes errored. Ordered by severity so the worst outcome across a set wins.
type Outcome string

const (
	// OutcomeComplete — every assertion passed.
	OutcomeComplete Outcome = "complete"
	// OutcomePartial — some assertions passed and some failed. The run did real
	// work but did not fully meet the stated goal (e.g. 2 of 3 sources added).
	OutcomePartial Outcome = "partial"
	// OutcomeEmpty — the run executed cleanly but produced nothing: the target
	// existed and reported zero items, or an empty/null output. This is the
	// outcome "no runtime error means success" hides, and the reason the
	// distinction exists at all.
	OutcomeEmpty Outcome = "empty"
	// OutcomeFailed — a required assertion failed outright, or the target of an
	// assertion never executed.
	OutcomeFailed Outcome = "failed"
)

// outcomeRank orders outcomes worst-first for aggregation.
func outcomeRank(o Outcome) int {
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

// Assertion operators. The original three are unchanged; everything below them
// is new and structural.
const (
	OpContains = "contains"
	OpEquals   = "equals"
	OpExists   = "exists"

	// OpNotEmpty passes when the target resolves to a non-empty collection,
	// object, or string. Distinct from OpExists, which passes on any output at
	// all — including an empty list, which is exactly the case that matters.
	OpNotEmpty = "not_empty"
	// OpCountGTE / OpCountEQ compare the number of items at the target (array
	// length, object key count, or 1 for a present scalar) against Value.
	OpCountGTE = "count_gte"
	OpCountEQ  = "count_eq"
	// OpHasField passes when the dotted path in Value resolves to a present,
	// non-null value on the target.
	OpHasField = "has_field"
	// OpFieldEquals passes when the dotted path in Value, given as
	// "path=expected", resolves to that value.
	OpFieldEquals = "field_equals"
	// OpDelivered passes when the target looks like a successful delivery
	// receipt (channel.send returns {ok,channel,to}). Value, when set, pins the
	// channel that must have accepted it.
	OpDelivered = "delivered"
	// OpDestination passes when the target's delivery destination equals Value —
	// the check that catches "it sent successfully, to the wrong chat".
	OpDestination = "destination"
	// OpArtifact passes when the target reports a finished artifact: an
	// artifact/audio/file id or url is present AND any status field is terminal
	// (not pending/processing/queued). Value optionally pins the artifact type.
	OpArtifact = "artifact"
)

// substantiveOps are the operators that express a BUSINESS outcome. An outcome
// contract built only from the others cannot distinguish "delivered the brief"
// from "produced some bytes", which is what the "cannot be certified with only
// a run-completed assertion" rule is about.
var substantiveOps = map[string]bool{
	OpNotEmpty:    true,
	OpCountGTE:    true,
	OpCountEQ:     true,
	OpHasField:    true,
	OpFieldEquals: true,
	OpDelivered:   true,
	OpDestination: true,
	OpArtifact:    true,
	// contains/equals against a specific expected value are substantive too: a
	// concrete expectation is a real claim about the result.
	OpContains: true,
	OpEquals:   true,
}

// IsSubstantiveAssertion reports whether an assertion makes a real claim about
// the outcome. `exists` never does — it passes for any non-empty output — and
// neither does a contains/equals with an empty expected value.
func IsSubstantiveAssertion(a Assertion) bool {
	if !substantiveOps[a.Op] {
		return false
	}
	if (a.Op == OpContains || a.Op == OpEquals) && strings.TrimSpace(a.Value) == "" {
		return false
	}
	return true
}

// AssertionStrength summarises whether a set of assertions is strong enough to
// certify against, and says what is missing when it isn't.
type AssertionStrength struct {
	Total       int      `json:"total"`
	Substantive int      `json:"substantive"`
	OK          bool     `json:"ok"`
	Reasons     []string `json:"reasons,omitempty"`
	Fix         string   `json:"fix,omitempty"`
}

// AssessAssertions judges an outcome contract's strength. This is the check a
// certification gate consumes: it is deliberately separate from evaluation so
// it can run at author time, before any run exists.
func AssessAssertions(assertions []Assertion) AssertionStrength {
	s := AssertionStrength{Total: len(assertions)}
	for _, a := range assertions {
		if IsSubstantiveAssertion(a) {
			s.Substantive++
		}
	}
	switch {
	case s.Total == 0:
		s.Reasons = append(s.Reasons, "this agent declares no outcome assertions, so a run that delivers nothing counts as success")
		s.Fix = "add at least one assertion describing what a successful run must produce — e.g. count_gte on the items collected, or delivered on the send step"
	case s.Substantive == 0:
		s.Reasons = append(s.Reasons, "every assertion only checks that output exists; none checks what the run was supposed to achieve")
		s.Fix = "replace or supplement the exists assertions with a substantive check (count_gte, has_field, delivered, destination, artifact)"
	default:
		s.OK = true
	}
	return s
}

// ── structural resolution ───────────────────────────────────────────────────

// decodeTarget decodes an output into a Go value, transparently unwrapping the
// common case of a JSON document that arrived as a JSON string.
func decodeTarget(raw json.RawMessage) (any, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, false
	}
	if s, ok := v.(string); ok {
		trimmed := strings.TrimSpace(s)
		if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
			var inner any
			if json.Unmarshal([]byte(trimmed), &inner) == nil {
				return inner, true
			}
		}
	}
	return v, true
}

// lookupPath walks a dotted path into a decoded value. Numeric segments index
// into arrays, so "results.0.url" works. ok=false when a segment can't be
// walked or the leaf is null.
func lookupPath(v any, path string) (any, bool) {
	cur := v
	for _, seg := range strings.Split(strings.TrimSpace(path), ".") {
		if seg == "" {
			continue
		}
		switch typed := cur.(type) {
		case map[string]any:
			next, present := typed[seg]
			if !present {
				return nil, false
			}
			cur = next
		case []any:
			idx, err := strconv.Atoi(seg)
			if err != nil || idx < 0 || idx >= len(typed) {
				return nil, false
			}
			cur = typed[idx]
		default:
			return nil, false
		}
	}
	if cur == nil {
		return nil, false
	}
	return cur, true
}

// countOf returns how many items a value represents: array length, object key
// count, or 1 for a present scalar. An empty string counts as 0 so "produced
// nothing" and "produced an empty string" agree.
func countOf(v any) int {
	switch typed := v.(type) {
	case nil:
		return 0
	case []any:
		return len(typed)
	case map[string]any:
		return len(typed)
	case string:
		if strings.TrimSpace(typed) == "" {
			return 0
		}
		return 1
	default:
		return 1
	}
}

// firstString returns the first non-empty string among the named keys.
func firstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := m[k].(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// terminalArtifactStatus reports whether a status string means "finished"
// rather than "still working". Unknown statuses count as terminal: a provider
// inventing a new success word must not make the assertion hang on failure.
func terminalArtifactStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "pending", "processing", "queued", "running", "in_progress", "in progress", "started", "unknown":
		return false
	default:
		return true
	}
}

// evalStructuralAssertion evaluates the operators added by P0-4. handled=false
// means the op is not one of these, and the caller should fall through to the
// original contains/equals/exists path.
func evalStructuralAssertion(a Assertion, raw json.RawMessage) (pass bool, outcome Outcome, detail string, handled bool) {
	if !substantiveOps[a.Op] || a.Op == OpContains || a.Op == OpEquals {
		return false, "", "", false
	}
	decoded, ok := decodeTarget(raw)
	if !ok {
		return false, OutcomeEmpty, fmt.Sprintf("target %q produced no decodable output", a.Target), true
	}

	switch a.Op {
	case OpNotEmpty:
		n := countOf(decoded)
		if n > 0 {
			return true, OutcomeComplete, fmt.Sprintf("%q produced %d item(s)", a.Target, n), true
		}
		return false, OutcomeEmpty, fmt.Sprintf("%q produced an empty result", a.Target), true

	case OpCountGTE, OpCountEQ:
		want, err := strconv.Atoi(strings.TrimSpace(a.Value))
		if err != nil {
			return false, OutcomeFailed, fmt.Sprintf("assertion value %q is not a number", a.Value), true
		}
		// A path prefix is allowed: "results>=3" style is expressed as
		// Target=node, Value=3, with the collection located by convention.
		got := countOf(decoded)
		if m, isObj := decoded.(map[string]any); isObj {
			// Prefer an obvious collection field over the object's key count,
			// which is almost never what the author means.
			for _, key := range []string{"results", "items", "sources", "artifacts", "data", "records"} {
				if inner, present := m[key]; present {
					got = countOf(inner)
					break
				}
			}
		}
		if a.Op == OpCountEQ {
			pass = got == want
		} else {
			pass = got >= want
		}
		switch {
		case pass:
			return true, OutcomeComplete, fmt.Sprintf("%q has %d item(s)", a.Target, got), true
		case got == 0:
			return false, OutcomeEmpty, fmt.Sprintf("%q produced 0 items, expected %s %d", a.Target, opWord(a.Op), want), true
		default:
			return false, OutcomePartial, fmt.Sprintf("%q produced %d item(s), expected %s %d", a.Target, got, opWord(a.Op), want), true
		}

	case OpHasField:
		if _, found := lookupPath(decoded, a.Value); found {
			return true, OutcomeComplete, fmt.Sprintf("%q has field %q", a.Target, a.Value), true
		}
		return false, OutcomeFailed, fmt.Sprintf("%q is missing field %q", a.Target, a.Value), true

	case OpFieldEquals:
		path, want, cut := strings.Cut(a.Value, "=")
		if !cut {
			return false, OutcomeFailed, fmt.Sprintf("field_equals value %q must be \"path=expected\"", a.Value), true
		}
		got, found := lookupPath(decoded, path)
		if !found {
			return false, OutcomeFailed, fmt.Sprintf("%q is missing field %q", a.Target, path), true
		}
		gotStr := strings.TrimSpace(fmt.Sprint(got))
		if gotStr == strings.TrimSpace(want) {
			return true, OutcomeComplete, fmt.Sprintf("%s = %s", path, gotStr), true
		}
		return false, OutcomeFailed, fmt.Sprintf("%s = %s, expected %s", path, gotStr, strings.TrimSpace(want)), true

	case OpDelivered, OpDestination:
		m, isObj := decoded.(map[string]any)
		if !isObj {
			return false, OutcomeFailed, fmt.Sprintf("%q is not a delivery receipt", a.Target), true
		}
		okFlag, _ := m["ok"].(bool)
		delivered, hasDelivered := m["delivered"].(bool)
		if hasDelivered {
			okFlag = okFlag || delivered
		}
		channel := firstString(m, "channel")
		to := firstString(m, "to", "destination", "chat_id", "recipient")
		if a.Op == OpDelivered {
			if !okFlag {
				return false, OutcomeFailed, fmt.Sprintf("%q did not report a successful delivery", a.Target), true
			}
			if want := strings.TrimSpace(a.Value); want != "" && !strings.EqualFold(want, channel) {
				return false, OutcomeFailed, fmt.Sprintf("delivered via %q, expected %q", channel, want), true
			}
			return true, OutcomeComplete, fmt.Sprintf("delivered via %q to %q", channel, to), true
		}
		if to == "" {
			return false, OutcomeFailed, fmt.Sprintf("%q records no destination", a.Target), true
		}
		if want := strings.TrimSpace(a.Value); want != "" && want != to {
			// The failure this catches: sent successfully, to the wrong place.
			return false, OutcomeFailed, fmt.Sprintf("delivered to %q, expected %q", to, want), true
		}
		return true, OutcomeComplete, fmt.Sprintf("destination is %q", to), true

	case OpArtifact:
		m, isObj := decoded.(map[string]any)
		if !isObj {
			return false, OutcomeFailed, fmt.Sprintf("%q is not an artifact result", a.Target), true
		}
		id := firstString(m, "artifact_id", "audio_url", "artifact_url", "url", "file_id", "id")
		status := firstString(m, "artifact_status", "status", "state")
		if id == "" {
			// An artifacts array is the other common shape.
			if arr, ok := m["artifacts"].([]any); ok && len(arr) > 0 {
				id = fmt.Sprintf("%d artifact(s)", len(arr))
			}
		}
		if id == "" {
			return false, OutcomeEmpty, fmt.Sprintf("%q reports no artifact", a.Target), true
		}
		if status != "" && !terminalArtifactStatus(status) {
			// Still working is PARTIAL, not failed: the run did its job and the
			// provider hasn't finished. Treating it as success is the bug.
			return false, OutcomePartial, fmt.Sprintf("artifact %s is still %q", id, status), true
		}
		if want := strings.TrimSpace(a.Value); want != "" {
			if got := firstString(m, "artifact_type", "type"); got != "" && !strings.EqualFold(got, want) {
				return false, OutcomeFailed, fmt.Sprintf("artifact type %q, expected %q", got, want), true
			}
		}
		return true, OutcomeComplete, fmt.Sprintf("artifact %s is ready (status %q)", id, statusOr(status)), true
	}
	return false, "", "", false
}

func opWord(op string) string {
	if op == OpCountEQ {
		return "exactly"
	}
	return "at least"
}

func statusOr(s string) string {
	if strings.TrimSpace(s) == "" {
		return "complete"
	}
	return s
}

// ClassifyOutcome aggregates assertion results into a single run outcome. The
// worst outcome wins, because a run that delivered nothing is not rescued by
// three other assertions passing.
func ClassifyOutcome(results []AssertionResult) Outcome {
	if len(results) == 0 {
		// No contract to judge against. Not "complete" — there is nothing that
		// says this run achieved anything.
		return OutcomeEmpty
	}
	worst := OutcomeComplete
	passed := 0
	for _, r := range results {
		if r.Pass {
			passed++
		}
		o := r.Outcome
		if o == "" {
			if r.Pass {
				o = OutcomeComplete
			} else {
				o = OutcomeFailed
			}
		}
		if outcomeRank(o) > outcomeRank(worst) {
			worst = o
		}
	}
	// Some passed and some didn't, with no worse signal → partial.
	if worst == OutcomeFailed && passed > 0 {
		return OutcomePartial
	}
	return worst
}
