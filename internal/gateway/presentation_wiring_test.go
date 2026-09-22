package gateway

import "testing"

// #199 — a ledger row with output carries a presentation; one without does not.
func TestPresentOutput(t *testing.T) {
	if presentOutput("   ") != nil {
		t.Fatal("empty output must have no presentation")
	}
	p := presentOutput("# Backups verified\n\n- [x] soulspace\n- [x] postgres\n")
	if p == nil || p.Headline != "Backups verified" || len(p.Blocks) == 0 {
		t.Fatalf("presentation = %+v", p)
	}
	kinds := map[string]bool{}
	for _, b := range p.Blocks {
		kinds[b.Kind] = true
	}
	if !kinds["checklist"] {
		t.Fatalf("expected a checklist block, got %v", kinds)
	}
}
