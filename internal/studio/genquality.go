package studio

import (
	"strings"
	"text/template"

	"github.com/soulacy/soulacy/internal/reasoning"
)

// genquality.go measures how robust the workflow-generation pipeline is: given a
// RAW generated draft (as a builder model would emit it), does the deterministic
// normalization + validation layer turn it into a valid, renderable flow? This
// is the offline, LLM-free harness behind the generation eval corpus — it lets
// us quantify the "absorb model variance" coverage and guard it in CI, instead
// of discovering each hallucination class in the field.

// GenCheck is the verdict for one raw draft after the deterministic pipeline.
type GenCheck struct {
	Valid  bool     `json:"valid"`
	Errors []string `json:"errors,omitempty"`
}

// NormalizeAndCheck runs the SAME deterministic passes the generation pipeline
// applies (parse → normalize kinds → reconcile ports → content-output) and then
// validates: the graph must compile, and every node input template must parse
// against the flow funcset (catching "function X not defined" like fromJson).
// It performs NO LLM calls, so it is deterministic and CI-safe.
func NormalizeAndCheck(rawDraftJSON string) GenCheck {
	d, err := ParseDraft(rawDraftJSON)
	if err != nil {
		return GenCheck{Valid: false, Errors: []string{"parse: " + err.Error()}}
	}

	// Deterministic repair layer (mirror of Compile's post-parse chain).
	normalizeFlow(&d)
	reconcilePorts(&d)
	ensureContentOutput(&d)

	var errs []string

	// Structural + wiring validity (CompileFlow + rules).
	vr := Validate(d)
	for _, e := range vr.Errors {
		errs = append(errs, e.Message)
	}

	// Template validity: each node input must parse with the flow funcset. Go's
	// text/template validates function names at Parse time, so an unregistered
	// helper (the fromJson class) is caught here deterministically.
	funcs := reasoning.FlowTemplateFuncs()
	for _, n := range d.Flow.Nodes {
		in := strings.TrimSpace(n.Input)
		if in == "" {
			continue
		}
		if _, perr := template.New("").Funcs(funcs).Parse(in); perr != nil {
			errs = append(errs, "node "+n.ID+": input template: "+perr.Error())
		}
	}

	return GenCheck{Valid: len(errs) == 0, Errors: errs}
}
