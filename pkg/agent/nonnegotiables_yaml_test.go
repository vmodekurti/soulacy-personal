package agent_test

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/soulacy/soulacy/pkg/agent"
)

func TestNonNegotiablesBareListIsMustNot(t *testing.T) {
	src := `
id: list-nn
name: List NN
non_negotiables:
  - never move money
  - flag a new subscription the week it appears
`
	var def agent.Definition
	if err := yaml.Unmarshal([]byte(src), &def); err != nil {
		t.Fatal(err)
	}
	if def.NonNegotiables == nil {
		t.Fatal("non_negotiables was nil")
	}
	if len(def.NonNegotiables.Must) != 0 {
		t.Errorf("must: got %v, want empty (bare list is must_not)", def.NonNegotiables.Must)
	}
	got := def.NonNegotiables.MustNot
	if len(got) != 2 || got[0] != "never move money" || got[1] != "flag a new subscription the week it appears" {
		t.Errorf("must_not: got %v", got)
	}
}

func TestNonNegotiablesMapFormUnchanged(t *testing.T) {
	src := `
id: map-nn
non_negotiables:
  must:
    - cite every numeric claim with [n]
  must_not:
    - give legal advice
  output_constraints:
    format: markdown
    max_length: 800
`
	var def agent.Definition
	if err := yaml.Unmarshal([]byte(src), &def); err != nil {
		t.Fatal(err)
	}
	nn := def.NonNegotiables
	if nn == nil {
		t.Fatal("non_negotiables was nil")
	}
	if len(nn.Must) != 1 || nn.Must[0] != "cite every numeric claim with [n]" {
		t.Errorf("must: got %v", nn.Must)
	}
	if len(nn.MustNot) != 1 || nn.MustNot[0] != "give legal advice" {
		t.Errorf("must_not: got %v", nn.MustNot)
	}
	if nn.OutputConstraints == nil || nn.OutputConstraints.Format != "markdown" || nn.OutputConstraints.MaxLength != 800 {
		t.Errorf("output_constraints: got %+v", nn.OutputConstraints)
	}
}

func TestNonNegotiablesScalarRejected(t *testing.T) {
	src := `
id: scalar-nn
non_negotiables: never move money
`
	var def agent.Definition
	err := yaml.Unmarshal([]byte(src), &def)
	if err == nil {
		t.Fatal("expected error for scalar non_negotiables")
	}
	if !strings.Contains(err.Error(), "must:/must_not:") {
		t.Errorf("error should hint the map form, got %v", err)
	}
}
