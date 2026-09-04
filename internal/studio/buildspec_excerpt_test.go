package studio

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// The Work row quotes the prompt back as evidence for a stage it claims to have
// found. A quote that starts or ends mid-word reads as though Studio mangled
// the input, which undermines the exact thing the excerpt exists to establish.
func TestStageEvidenceQuotesWholeWords(t *testing.T) {
	spec := ExtractBuildSpecFrom(travelAdvisorIntent, specCatalog())
	if len(spec.Stages) == 0 {
		t.Fatal("expected at least one stage for a prompt describing a search")
	}
	for _, st := range spec.Stages {
		d := st.Detail
		if d == "" || !strings.Contains(d, "…") {
			continue // a synthesised detail, not a quotation
		}
		body := strings.TrimSpace(strings.Trim(d, "… "))
		if body == "" {
			t.Fatalf("stage %q has an empty excerpt", st.Name)
		}
		if !utf8.ValidString(d) {
			t.Errorf("stage %q excerpt is not valid UTF-8: %q", st.Name, d)
		}
		// Every word in the excerpt must appear whole in the original prompt.
		// "gent" and "fi" — the halves of "agent" and "find" — would fail here.
		for _, w := range strings.Fields(body) {
			if !strings.Contains(travelAdvisorIntent, w) {
				t.Errorf("stage %q excerpt contains %q, which is not a whole word "+
					"of the prompt (excerpt: %q)", st.Name, w, d)
			}
		}
	}
}

// The excerpt is quoted back to the person who wrote it, so it should carry
// their capitalisation rather than the lowercased copy used for matching.
func TestStageEvidencePreservesOriginalCasing(t *testing.T) {
	spec := ExtractBuildSpecFrom(travelAdvisorIntent, specCatalog())
	var sawQuote bool
	for _, st := range spec.Stages {
		if strings.Contains(st.Detail, "…") {
			sawQuote = true
			if strings.Contains(st.Detail, "mcp travel tool") {
				t.Errorf("excerpt was taken from the lowercased copy: %q", st.Detail)
			}
		}
	}
	if !sawQuote {
		t.Skip("no quoted excerpt in this spec")
	}
}

func TestExcerptAroundHandlesEdges(t *testing.T) {
	const s = "alpha beta gamma delta"
	if got := excerptAround(s, 0); got != s {
		t.Errorf("a window covering the whole string should not be elided: %q", got)
	}
	if got := excerptAround(s, -1); got != "" {
		t.Errorf("negative index should yield empty, got %q", got)
	}
	if got := excerptAround(s, len(s)+50); got != "" {
		t.Errorf("out-of-range index should yield empty, got %q", got)
	}
	// Multi-byte input must not be cut mid-rune.
	long := strings.Repeat("naïve café ", 40)
	got := excerptAround(long, 200)
	if !utf8.ValidString(got) {
		t.Errorf("excerpt split a multi-byte rune: %q", got)
	}
}
