package studio

// outcomespec.go — the Studio-side outcome contract and its derivation from a
// draft (P0-4).
//
// Two problems are solved here.
//
// First, the contract has to CROSS the Studio/runtime boundary. Studio's
// Assertion type is a build-time thing; the runtime reads agent.OutcomeContract.
// ToAgentContract is the one translation point, so the two vocabularies cannot
// drift apart silently.
//
// Second, generation quality. SynthesizeTests asks a model for assertions and
// its own prompt tells it to fall back to `{"op":"exists"}` whenever it can't
// predict a substring — which made the WEAKEST possible assertion the most
// common one, and `exists` passes for any non-empty output. DeriveAssertions
// gives that a floor: it reads the graph and emits substantive assertions from
// what the workflow demonstrably does — a delivery step implies a `delivered`
// assertion, a search/collect step implies `count_gte 1`, an artifact poll
// implies `artifact`. Deterministic, no model call, and it means an agent is
// never left with a contract that only proves bytes were produced.

import (
	"encoding/json"
	"strings"

	"github.com/soulacy/soulacy/pkg/agent"
	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

// OutcomeSpec is the draft-side outcome contract. It mirrors
// agent.OutcomeContract but carries Studio's Assertion type so the assertion
// editor, the test runner, and the save path all speak one shape.
type OutcomeSpec struct {
	Goal       string      `json:"goal,omitempty"`
	Enforce    string      `json:"enforce,omitempty"`
	Assertions []Assertion `json:"assertions,omitempty"`

	// ── Test fixtures (ST-10) ────────────────────────────────────────────────
	// These travel WITH the workflow so a test suite is not lost on reload. They
	// were previously component-local state in the Studio bench, which meant the
	// mocks and sample input someone built up to reproduce a bug vanished the
	// moment they navigated away — and a reviewer opening the workflow had no way
	// to re-run what its author had tested.
	//
	// Deliberately NOT part of ToAgentContract: fixtures are build-time
	// scaffolding, so they must never reach the runtime contract or the deployed
	// agent's SOUL.yaml behaviour.

	// Mocks are per-node canned outputs keyed by node id, as raw JSON.
	Mocks map[string]json.RawMessage `json:"mocks,omitempty"`
	// SampleInput is the trigger payload the bench runs with.
	SampleInput string `json:"sample_input,omitempty"`
	// Variables and Environment are the named values a test run seeds beyond the
	// trigger — the ST-10 "variables and environment values" inputs.
	Variables   map[string]string `json:"variables,omitempty"`
	Environment map[string]string `json:"environment,omitempty"`
	// StartNode optionally begins the run at a specific node instead of the entry,
	// so a long pipeline can be iterated on from the step that is actually broken.
	StartNode string `json:"start_node,omitempty"`
}

// ToAgentContract converts a draft's contract into the persisted form. Returns
// nil when there is nothing to persist, so an agent without a contract keeps a
// clean SOUL.yaml rather than gaining an empty `outcome: {}` block.
func (s *OutcomeSpec) ToAgentContract() *agent.OutcomeContract {
	if s == nil || len(s.Assertions) == 0 {
		return nil
	}
	out := &agent.OutcomeContract{
		Goal:    strings.TrimSpace(s.Goal),
		Enforce: strings.TrimSpace(s.Enforce),
	}
	for _, a := range s.Assertions {
		if strings.TrimSpace(a.Op) == "" {
			continue
		}
		target := strings.TrimSpace(a.Target)
		if target == "" {
			target = "result"
		}
		out.Assertions = append(out.Assertions, agent.OutcomeAssertion{
			Target:   target,
			Op:       strings.TrimSpace(a.Op),
			Value:    strings.TrimSpace(a.Value),
			Describe: describeAssertion(a),
		})
	}
	if len(out.Assertions) == 0 {
		return nil
	}
	return out
}

// FromAgentContract converts a persisted contract back into draft form, so
// re-opening a saved agent in Studio shows the assertions it already has
// instead of an empty editor that would silently drop them on the next save.
func FromAgentContract(c *agent.OutcomeContract) *OutcomeSpec {
	if !c.HasAssertions() {
		return nil
	}
	spec := &OutcomeSpec{Goal: c.Goal, Enforce: c.Enforce}
	for _, a := range c.Assertions {
		spec.Assertions = append(spec.Assertions, Assertion{Target: a.Target, Op: a.Op, Value: a.Value})
	}
	return spec
}

// describeAssertion renders an assertion as the plain-language claim it encodes,
// so a production failure notification reads "three sources were added — 0
// items" rather than "count_gte(add_source_pack, 3) failed".
func describeAssertion(a Assertion) string {
	target := strings.TrimSpace(a.Target)
	if target == "" || target == "result" {
		target = "the run"
	}
	switch a.Op {
	case OpCountGTE:
		return "“" + target + "” produced at least " + a.Value + " item(s)"
	case OpCountEQ:
		return "“" + target + "” produced exactly " + a.Value + " item(s)"
	case OpNotEmpty:
		return "“" + target + "” produced a non-empty result"
	case OpHasField:
		return "“" + target + "” included " + a.Value
	case OpFieldEquals:
		return "“" + target + "” reported " + a.Value
	case OpDelivered:
		if a.Value != "" {
			return "the result was delivered via " + a.Value
		}
		return "the result was delivered"
	case OpDestination:
		return "the result reached " + a.Value
	case OpArtifact:
		return "“" + target + "” finished producing its artifact"
	case OpContains:
		return "“" + target + "” mentioned " + a.Value
	case OpEquals:
		return "“" + target + "” equalled " + a.Value
	default:
		return "“" + target + "” produced output"
	}
}

// node-role detection. These read the graph's own vocabulary — tool names and
// node ids — rather than asking a model what the workflow does. Conservative on
// purpose: a missed role costs one absent assertion, a wrong role costs a
// false failure on every production run.

func nodeMentions(n sdkr.FlowNode, markers ...string) bool {
	hay := strings.ToLower(n.ID + " " + n.Tool + " " + n.Agent + " " + n.Description + " " + n.Intent)
	for _, m := range markers {
		if strings.Contains(hay, m) {
			return true
		}
	}
	return false
}

// isOutcomeDeliveryNode reports a step whose job is to send the result
// somewhere. Broader than compiler.go's isDeliveryNode (which asks the narrower
// question "is this a pure receipt-producing tool that must not be the output
// node"): a peer agent or a python block that posts a webhook also delivers,
// and an assertion should cover it.
func isOutcomeDeliveryNode(n sdkr.FlowNode) bool {
	tool := strings.ToLower(strings.TrimSpace(n.Tool))
	if tool == "channel.send" || strings.HasSuffix(tool, ".send") {
		return true
	}
	return nodeMentions(n, "deliver", "notify", "publish")
}

// isCollectionNode reports a step that gathers items downstream work depends on
// — the step whose empty result is the failure nobody notices.
func isCollectionNode(n sdkr.FlowNode) bool {
	return nodeMentions(n, "search", "fetch", "collect", "gather", "list", "query", "find", "curate", "source")
}

// isArtifactNode reports a step that produces or waits on a generated artifact.
func isArtifactNode(n sdkr.FlowNode) bool {
	return nodeMentions(n, "audio", "artifact", "render", "generate_audio", "poll", "video", "podcast")
}

// DeriveAssertions builds a substantive contract from the draft's graph alone.
//
// It is the floor under LLM generation, not a replacement for it: a model can
// express "the brief mentions each source's publication date", which no static
// reading of a graph could infer. But a model that returns nothing useful must
// not leave the agent with a contract that only proves bytes were produced —
// so these are added when the model's own assertions are weak.
//
// At most one assertion per role, and only for roles the graph actually shows.
func DeriveAssertions(flow Flow) []Assertion {
	var out []Assertion
	var deliverID, collectID, artifactID string
	for _, n := range flow.Nodes {
		if isStructuralNodeKind(n.Kind) {
			continue
		}
		// Later nodes win for delivery (the last send is the one that matters);
		// earlier win for collection (the first gather feeds everything after).
		if isOutcomeDeliveryNode(n) {
			deliverID = n.ID
		}
		if collectID == "" && isCollectionNode(n) {
			collectID = n.ID
		}
		if artifactID == "" && isArtifactNode(n) {
			artifactID = n.ID
		}
	}
	if collectID != "" {
		out = append(out, Assertion{Target: collectID, Op: OpCountGTE, Value: "1"})
	}
	if artifactID != "" {
		out = append(out, Assertion{Target: artifactID, Op: OpArtifact})
	}
	if deliverID != "" {
		out = append(out, Assertion{Target: deliverID, Op: OpDelivered})
	}
	// A graph with none of those roles still gets one real claim: the run must
	// produce something, which `not_empty` checks and `exists` does not (an
	// empty list satisfies exists).
	if len(out) == 0 {
		out = append(out, Assertion{Target: "result", Op: OpNotEmpty})
	}
	return out
}

func isStructuralNodeKind(kind string) bool {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "branch", "trigger", "exit":
		return true
	}
	return false
}

// StrengthenAssertions returns the assertions to persist for a draft: the
// author's/model's own, plus derived ones when what exists makes no substantive
// claim. Existing assertions are never removed or rewritten — a human's
// explicit check always survives.
func StrengthenAssertions(existing []Assertion, flow Flow) []Assertion {
	if AssessAssertions(existing).OK {
		return existing
	}
	have := map[string]bool{}
	for _, a := range existing {
		have[a.Target+"|"+a.Op] = true
	}
	out := append([]Assertion(nil), existing...)
	for _, a := range DeriveAssertions(flow) {
		if !have[a.Target+"|"+a.Op] {
			out = append(out, a)
		}
	}
	return out
}
