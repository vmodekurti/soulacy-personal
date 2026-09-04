package studio

import (
	"strings"
	"testing"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

func TestFixTemplateTypos_CollapsesDoubleDot(t *testing.T) {
	d := Draft{Flow: Flow{Nodes: []sdkr.FlowNode{
		{ID: "n", Kind: "tool", Tool: "mk", Input: `{"title":"AI News - {{ ..date_info }}","url":"http://a..b"}`},
	}}}
	if fixTemplateTypos(&d) != 1 {
		t.Fatal("expected one fix")
	}
	got := d.Flow.Nodes[0].Input
	if !strings.Contains(got, "{{ .date_info }}") {
		t.Errorf("double dot not collapsed inside template: %q", got)
	}
	if !strings.Contains(got, "http://a..b") {
		t.Errorf("non-template text must be untouched: %q", got)
	}
	// Now preflight should pass the template check.
	r := Preflight(d, PreflightInput{})
	if blockKinds(r)["template"] != 0 {
		t.Errorf("template should be valid after typo fix: %+v", r.Blockers)
	}
}
