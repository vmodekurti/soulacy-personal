package studio

import (
	"testing"

	"github.com/soulacy/soulacy/internal/reasoning"
)

// The concrete few-shot example embedded in the prompt MUST itself be a valid,
// compilable workflow — otherwise we are teaching the model a broken shape.
func TestCanonicalExample_ParsesAndCompiles(t *testing.T) {
	d, err := ParseDraft(canonicalExample)
	if err != nil {
		t.Fatalf("canonicalExample failed to parse: %v", err)
	}
	normalizeFlow(&d)
	reconcilePorts(&d)
	if _, err := reasoning.CompileFlow(d.spec()); err != nil {
		t.Fatalf("canonicalExample failed to compile: %v", err)
	}
	// It must demonstrate a concrete python node with real code.
	var hasPyCode bool
	for _, n := range d.Flow.Nodes {
		if n.Kind == "python" && len(n.Code) > 20 {
			hasPyCode = true
		}
	}
	if !hasPyCode {
		t.Fatal("canonicalExample should include a python node with real code")
	}
}
