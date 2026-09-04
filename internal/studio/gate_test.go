package studio

import (
	"context"
	"strings"
	"testing"
)

func TestCompileGate_Deterministic(t *testing.T) {
	vars := []string{"articles", "score", "approved"}
	cases := []struct {
		phrase string
		want   string
	}{
		{"only if at least one article was found", "{{ gt (len .articles) 0 }}"},
		{"if there are no articles", "{{ eq (len .articles) 0 }}"},
		{"score > 0.5", "{{ gt .score 0.5 }}"},
		{"count >= 3", "{{ ge .count 3 }}"},
		{"if approved", "{{ .approved }}"},
		{"already a {{ gt (len .articles) 0 }} expr", "already a {{ gt (len .articles) 0 }} expr"},
	}
	for _, c := range cases {
		got, err := CompileGate(context.Background(), nil, c.phrase, vars)
		if err != nil {
			t.Errorf("%q: unexpected error %v", c.phrase, err)
			continue
		}
		if got != c.want {
			t.Errorf("%q: got %q want %q", c.phrase, got, c.want)
		}
		if verr := ValidatePredicate(got); verr != nil {
			t.Errorf("%q: produced invalid predicate %q: %v", c.phrase, got, verr)
		}
	}
}

// With no deterministic match and no LLM, CompileGate errors clearly rather than
// guessing.
func TestCompileGate_NoMatchNoLLM(t *testing.T) {
	_, err := CompileGate(context.Background(), nil, "when the vibes are good", nil)
	if err == nil {
		t.Error("expected an error when nothing matches and no LLM is supplied")
	}
}

// An invalid {{ }} expression is rejected.
func TestCompileGate_InvalidTemplate(t *testing.T) {
	_, err := CompileGate(context.Background(), nil, "{{ gt (len .x) }", nil)
	if err == nil {
		t.Error("expected invalid template to be rejected")
	}
}

// fakeGateLLM returns a canned predicate.
type fakeGateLLM struct{ out string }

func (f fakeGateLLM) Complete(_ context.Context, _ string) (string, error) { return f.out, nil }

func TestCompileGate_LLMFallback(t *testing.T) {
	llm := fakeGateLLM{out: "```\n{{ and (gt (len .items) 0) .ready }}\n```"}
	got, err := CompileGate(context.Background(), llm, "combine the two tallies and proceed when the ratio clears the bar", []string{"items", "ready"})
	if err != nil {
		t.Fatalf("LLM fallback failed: %v", err)
	}
	if !strings.HasPrefix(got, "{{") || !strings.Contains(got, "and") {
		t.Errorf("expected the model predicate, got %q", got)
	}
	if verr := ValidatePredicate(got); verr != nil {
		t.Errorf("LLM predicate should validate: %v", verr)
	}
}

// A model that returns garbage is rejected (validation catches it).
func TestCompileGate_LLMGarbageRejected(t *testing.T) {
	llm := fakeGateLLM{out: "totally not a template"}
	_, err := CompileGate(context.Background(), llm, "something unmatched entirely", []string{"x"})
	if err == nil {
		t.Error("a non-template model answer should be rejected")
	}
}
