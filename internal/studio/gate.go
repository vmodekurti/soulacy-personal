package studio

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"text/template"

	reasoning "github.com/soulacy/soulacy/internal/reasoning"
)

// gate.go — Phase B: editable connectors. A connector (edge) carries a GATE: the
// condition under which the flow proceeds along it. The engine evaluates the gate
// as a Go-template predicate (FlowEdge.If). CompileGate lets the user express that
// gate in plain language ("only if at least one article was found") and turns it
// into a validated predicate ("{{ gt (len .articles) 0 }}") — deterministically
// for common shapes, via the LLM otherwise. No template typing required.

// cmpOpFunc maps a comparison operator in the user's phrasing to the Go-template
// builtin that implements it.
var cmpOpFunc = map[string]string{
	">": "gt", ">=": "ge", "<": "lt", "<=": "le", "==": "eq", "=": "eq", "!=": "ne",
}

var cmpRe = regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_]*)\s*(>=|<=|==|!=|>|<|=)\s*([0-9]+(?:\.[0-9]+)?)`)

// CompileGate converts a plain-language connector gate into a flow predicate.
// `vars` are the flow variables available at this edge (used to bind nouns to the
// right var and to ground the LLM). It tries deterministic patterns first (no
// model); if none fit and an LLM is supplied, it asks the model; the result is
// always validated against the exact flow template funcset, so a returned
// predicate is guaranteed to parse at run time. An already-templated phrase
// ({{ … }}) is validated and returned as-is.
func CompileGate(ctx context.Context, llm LLM, phrase string, vars []string) (string, error) {
	p := strings.TrimSpace(phrase)
	if p == "" {
		return "", fmt.Errorf("studio: empty gate")
	}
	// Already a template expression — validate and pass through.
	if strings.Contains(p, "{{") {
		if err := ValidatePredicate(p); err != nil {
			return "", fmt.Errorf("studio: gate predicate is invalid: %w", err)
		}
		return p, nil
	}

	if pred := deterministicGate(p, vars); pred != "" {
		return pred, nil
	}

	if llm != nil {
		if pred, err := llmGate(ctx, llm, p, vars); err == nil && strings.Contains(pred, "{{") {
			// Require a real template action AND that it parses: a plain-text model
			// answer (no {{ }}) is a "valid" template but not a predicate.
			if verr := ValidatePredicate(pred); verr == nil {
				return pred, nil
			}
		}
	}
	return "", fmt.Errorf("studio: could not compile gate %q — rephrase (e.g. \"at least one article\", \"score > 0.5\") or write a {{ }} expression", phrase)
}

// deterministicGate handles the common gate shapes without a model. Returns ""
// when nothing matches.
func deterministicGate(phrase string, vars []string) string {
	lp := strings.ToLower(phrase)

	// Numeric comparison: "score > 0.5", "count >= 3".
	if m := cmpRe.FindStringSubmatch(phrase); m != nil {
		if fn, ok := cmpOpFunc[m[2]]; ok {
			return fmt.Sprintf("{{ %s .%s %s }}", fn, m[1], m[3])
		}
	}

	v := matchVar(lp, vars)
	if v == "" {
		return ""
	}

	// Emptiness / presence on a list-or-string var.
	negative := containsAny(lp, "no ", "none", "empty", "not any", "zero", "nothing", "without")
	positive := containsAny(lp, "at least one", "at least 1", "any ", "some ", "found", "has ", "have ", "more than 0", "exists", "present", "non-empty", "not empty")
	switch {
	case negative:
		return fmt.Sprintf("{{ eq (len .%s) 0 }}", v)
	case positive:
		return fmt.Sprintf("{{ gt (len .%s) 0 }}", v)
	}

	// Truthiness: "if approved", "when ready".
	if containsAny(lp, "is true", "true", "approved", "ready", "ok", "success", "succeeded", "enabled") {
		return fmt.Sprintf("{{ .%s }}", v)
	}
	if containsAny(lp, "is false", "false", "failed", "error", "rejected", "disabled") {
		return fmt.Sprintf("{{ not .%s }}", v)
	}
	return ""
}

// matchVar returns the first provided var that appears in the lowercased phrase,
// tolerating simple plural/singular drift ("articles" var vs "article" in the
// phrase, and vice versa), or "".
func matchVar(lowerPhrase string, vars []string) string {
	for _, v := range vars {
		tv := strings.ToLower(strings.TrimSpace(v))
		if tv == "" {
			continue
		}
		if strings.Contains(lowerPhrase, tv) ||
			strings.Contains(lowerPhrase, strings.TrimSuffix(tv, "s")) {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// llmGate asks the model for a predicate, constrained to the flow funcset and the
// available vars. It returns the raw predicate text (validated by the caller).
func llmGate(ctx context.Context, llm LLM, phrase string, vars []string) (string, error) {
	var sb strings.Builder
	sb.WriteString("Convert this plain-language workflow connector condition into a SINGLE Go text/template boolean expression.\n")
	sb.WriteString("Respond with ONLY the expression, e.g. {{ gt (len .items) 0 }} — no prose, no code fences.\n")
	sb.WriteString("Allowed functions: len, gt, lt, ge, le, eq, ne, and, or, not. Reference flow vars as .name.\n")
	if len(vars) > 0 {
		sb.WriteString("Available flow vars: ")
		sb.WriteString(strings.Join(vars, ", "))
		sb.WriteString("\n")
	}
	sb.WriteString("Condition: ")
	sb.WriteString(phrase)
	sb.WriteString("\n")
	out, err := llm.Complete(ctx, sb.String())
	if err != nil {
		return "", err
	}
	out = strings.TrimSpace(stripFences(out))
	// Narrow to the first {{ … }} if the model added stray text.
	if i := strings.Index(out, "{{"); i >= 0 {
		if j := strings.LastIndex(out, "}}"); j > i {
			out = out[i : j+2]
		}
	}
	return strings.TrimSpace(out), nil
}

// ValidatePredicate reports whether expr parses with the exact funcset the flow
// engine renders edge predicates with — keeping "valid in the editor" and
// "renders at run" in lockstep.
func ValidatePredicate(expr string) error {
	_, err := template.New("gate").Funcs(reasoning.FlowTemplateFuncs()).Parse(expr)
	return err
}
