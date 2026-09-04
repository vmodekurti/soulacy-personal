package studio

// repairtxn.go — transactional runtime repair (P0-7).
//
// The existing repair path proposes a change and re-validates it STRUCTURALLY:
// does the template parse, does the graph compile. That answers "is the patch
// well-formed", not "does the patch fix the failure" — so a proposal that
// parsed cleanly and changed nothing useful was indistinguishable from one that
// worked. The user found out on the next real run.
//
// This adds the missing half:
//
//	isolate  — the proposal is applied to a COPY, never to the live draft
//	replay   — the ORIGINAL failing input is re-run against that copy
//	promote  — the copy is adopted only if validation AND the outcome
//	           assertions both pass on the replay
//
// Plus the classification the requirement asks for. The old Classify lumped
// auth, network, and permissions into one undifferentiated `tool_failure`, and
// declared an `empty_result` class it never actually produced. Both matter for
// P0-7's first bullet: a credential failure and a rate limit want completely
// different responses, and neither may EVER be "repaired" by weakening a
// security control.

import (
	"fmt"
	"strings"
	"text/template"
	"time"

	reasoning "github.com/soulacy/soulacy/internal/reasoning"
	"github.com/soulacy/soulacy/pkg/agent"
	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

// Extended repair classes (P0-7). The original four live in liverepair.go;
// these split the overloaded tool_failure bucket and add the business case.
const (
	// RepairAuth — credentials rejected. NEVER auto-repairable: the only "fix"
	// available to a code change would be to weaken a security control.
	RepairAuth RepairClass = "auth"
	// RepairPermission — authenticated but not allowed. Same rule as auth.
	RepairPermission RepairClass = "permission"
	// RepairNetwork — transient transport failure. Retryable, not repairable:
	// nothing about the workflow is wrong.
	RepairNetwork RepairClass = "network"
	// RepairRateLimit — throttled. Retryable with backoff.
	RepairRateLimit RepairClass = "rate_limit"
	// RepairAssertion — every node ran, but a business-outcome assertion failed.
	// The workflow is structurally fine and semantically wrong, which is the
	// class the old classifier had no way to express at all.
	RepairAssertion RepairClass = "assertion"
)

// securityClasses are never repaired by mutating the workflow. Recorded as a
// set rather than an if-chain so the rule is stated once and cannot drift.
var securityClasses = map[RepairClass]bool{
	RepairAuth:       true,
	RepairPermission: true,
}

// ClassifyFailure extends Classify with the distinctions P0-7 requires. It
// consults the ORIGINAL classifier first so existing behaviour is preserved,
// then refines the tool_failure bucket.
func ClassifyFailure(run LiveNodeRun) RepairClass {
	base := Classify(run)
	if base != RepairToolFailure {
		return base
	}
	text := strings.ToLower(strings.TrimSpace(run.Error))
	if text == "" {
		text = strings.ToLower(outputErrorText(run.Output))
	}
	switch {
	case containsAny(text, "429", "rate limit", "too many requests", "quota exceeded"):
		return RepairRateLimit
	case containsAny(text, "401", "unauthorized", "invalid api key", "authentication", "no api key", "credentials"):
		return RepairAuth
	case containsAny(text, "403", "forbidden", "permission", "not permitted", "access denied", "consent"):
		return RepairPermission
	case containsAny(text, "no such host", "connection refused", "timeout", "deadline exceeded", "network", "eof", "reset by peer"):
		return RepairNetwork
	}
	return RepairToolFailure
}

// ClassifyEmptyResult reports the empty-collection case the original Classify
// declared but never produced: the node SUCCEEDED and returned nothing usable.
// It is checked separately because it is not an error at all — which is exactly
// why it slipped through.
func ClassifyEmptyResult(run LiveNodeRun) bool {
	if strings.TrimSpace(run.Error) != "" {
		return false
	}
	decoded, ok := decodeTarget(run.Output)
	if !ok {
		return true
	}
	return collectionCount(decoded) == 0
}

// collectionCount counts what an author means by "how many results" — the
// obvious collection field inside an object, rather than the object's key
// count. {"results":[]} holds ZERO results, not one key.
func collectionCount(v any) int {
	if m, ok := v.(map[string]any); ok {
		for _, key := range []string{"results", "items", "sources", "artifacts", "data", "records"} {
			if inner, present := m[key]; present {
				return countOf(inner)
			}
		}
	}
	return countOf(v)
}

// IsSecurityClass reports whether a class must never be "repaired" by changing
// the workflow. P0-7's last bullet, enforced as a lookup rather than a habit.
func IsSecurityClass(c RepairClass) bool { return securityClasses[c] }

// IsRetryable reports whether the right response is to try again unchanged.
// A retryable failure is not a defect, and proposing a code change for one
// teaches the repair loop to "fix" workflows that were never broken.
func IsRetryable(c RepairClass) bool {
	return c == RepairNetwork || c == RepairRateLimit
}

// RepairAdvice is the decision for one classified failure: what kind of
// response is appropriate, and what to tell the operator.
type RepairAdvice struct {
	Class      RepairClass `json:"class"`
	Repairable bool        `json:"repairable"`
	Retryable  bool        `json:"retryable"`
	Security   bool        `json:"security"`
	Summary    string      `json:"summary"`
	Action     string      `json:"action"`
}

// AdviseRepair maps a class onto the response it warrants.
func AdviseRepair(c RepairClass) RepairAdvice {
	a := RepairAdvice{Class: c}
	switch c {
	case RepairAuth:
		a.Security = true
		a.Summary = "A provider rejected the credentials for this step."
		a.Action = "Fix the key in Providers or Secrets. Soulacy will not attempt an automatic repair — every available code change here would weaken a security control rather than fix the cause."
	case RepairPermission:
		a.Security = true
		a.Summary = "The request was authenticated but not permitted."
		a.Action = "Grant the missing scope or capability at its source. This is never auto-repaired: relaxing a permission check is not a repair."
	case RepairNetwork:
		a.Retryable = true
		a.Summary = "A transient network failure stopped this step."
		a.Action = "Retry the step. Nothing about the workflow is wrong, so no change is proposed."
	case RepairRateLimit:
		a.Retryable = true
		a.Summary = "The provider throttled this request."
		a.Action = "Retry with backoff, or lower the fan-out's max-parallel so fewer calls land at once."
	case RepairAssertion:
		a.Repairable = true
		a.Summary = "Every step ran, but the run did not achieve what it was set up to achieve."
		a.Action = "Repair targets the step that produced too little, not the step that errored — because none did."
	case RepairShapeDrift, RepairTemplateError, RepairEmptyResult:
		a.Repairable = true
		a.Summary = "The data did not have the shape this step expected."
		a.Action = "Soulacy can propose a concrete reshape, verify it by replaying the failing input, and only then offer to keep it."
	default:
		a.Summary = "This step failed for a reason Soulacy cannot classify."
		a.Action = "Open the node's input and output to decide whether to repair or retry."
	}
	return a
}

// ── Transactional application ────────────────────────────────────────────────

// RepairAttempt is the full, auditable record of one transactional repair:
// what was proposed, whether the replay proved it, and how to undo it. Every
// field the requirement asks for — diff, rationale, evidence, rollback — is
// here, so a promotion is never a change the operator cannot inspect or revert.
type RepairAttempt struct {
	NodeID    string      `json:"node_id"`
	Class     RepairClass `json:"class"`
	Rationale string      `json:"rationale"`
	// Diff is the human-readable before/after for the changed field.
	Diff RepairDiff `json:"diff"`
	// Evidence is what justified the proposal (observed keys, the failing error).
	Evidence []string `json:"evidence,omitempty"`
	// Validated reports whether the isolated version compiles.
	Validated bool `json:"validated"`
	// Replayed reports whether the original failing input was re-run.
	Replayed bool `json:"replayed"`
	// ReplayPassed reports whether that replay met the outcome contract.
	ReplayPassed bool `json:"replay_passed"`
	// Promoted is true only when validation AND replay both passed.
	Promoted bool `json:"promoted"`
	// Unproven marks the one case that is neither promoted nor rejected: the
	// patch VALIDATED but there was no replay available to judge it with. The
	// returned candidate carries the patch, and the caller decides whether a
	// user-approved-but-unproven change is worth adopting. Keeping this distinct
	// from Promoted matters because the two failure modes are opposites —
	// "we proved this is wrong" must roll back, "we had nothing to prove it
	// with" must not silently discard the user's approved fix.
	Unproven bool   `json:"unproven,omitempty"`
	Reason   string `json:"reason,omitempty"`
	// Rollback is the value to restore to undo the change.
	Rollback RepairRollback `json:"rollback"`
	At       string         `json:"at,omitempty"`
}

// RepairDiff is the before/after of one field on one node.
type RepairDiff struct {
	Field string `json:"field"`
	Old   string `json:"old"`
	New   string `json:"new"`
}

// RepairRollback carries everything needed to restore the prior state.
type RepairRollback struct {
	NodeID string `json:"node_id"`
	Field  string `json:"field"`
	Value  string `json:"value"`
}

// ReplayFunc runs a candidate draft against the original failing input and
// reports the resulting node outputs. Injected so this package stays testable
// without a live engine — the gateway supplies the real implementation.
type ReplayFunc func(candidate Draft, input string) (outputs map[string]string, err error)

// ApplyRepairTransactionally is the whole P0-7 loop for one proposal.
//
// The draft passed in is NEVER mutated: the proposal is applied to a deep copy,
// which is validated, replayed, and judged. A repair that "compiles but doesn't
// fix it" cannot reach the live workflow, which is the failure mode this exists
// to close.
//
// The caller adopts the returned candidate when Promoted (proven) or Unproven
// (validated, but no replay was available to judge it with) — and must NOT
// adopt it otherwise, which is the actively-rejected case. Those two negatives
// are deliberately distinguishable: a repair disproved by a replay has to roll
// back, whereas one that merely could not be tested must still reach the user
// who approved it, clearly labelled.
func ApplyRepairTransactionally(
	draft Draft,
	proposal RepairProposal,
	failingInput string,
	contract *agent.OutcomeContract,
	replay ReplayFunc,
	now time.Time,
) (candidate Draft, attempt RepairAttempt) {
	attempt = RepairAttempt{
		NodeID:    proposal.NodeID,
		Class:     proposal.Class,
		Rationale: proposal.Rationale,
		Diff:      RepairDiff{Field: proposal.Field, Old: proposal.Old, New: proposal.New},
		Rollback:  RepairRollback{NodeID: proposal.NodeID, Field: proposal.Field, Value: proposal.Old},
		At:        now.UTC().Format(time.RFC3339),
	}
	for _, k := range proposal.ObservedKeys {
		attempt.Evidence = append(attempt.Evidence, "observed key: "+k)
	}

	// A security-class failure is refused before anything is applied. This is
	// the one branch that must never depend on a downstream check passing.
	if IsSecurityClass(proposal.Class) {
		attempt.Reason = "refused: " + string(proposal.Class) + " failures are never repaired by changing the workflow"
		return draft, attempt
	}

	// 1. ISOLATE — work on a copy so a rejected repair leaves nothing behind.
	candidate = cloneDraft(draft)
	if !applyProposalToDraft(&candidate, proposal) {
		attempt.Reason = "the proposal did not match any node in the draft"
		return draft, attempt
	}

	// 2. VALIDATE — structural check, as before.
	if err := validateCandidateDraft(candidate); err != nil {
		attempt.Reason = "the repaired version does not validate: " + err.Error()
		return draft, attempt
	}
	attempt.Validated = true

	// 3. REPLAY — the step that did not exist. Re-run the ORIGINAL failing
	//    input against the repaired version; a patch that cannot survive the
	//    input that broke it has not been shown to fix anything.
	if replay == nil {
		// Return the CANDIDATE, not the original. Promotion still requires proof,
		// but withholding the patched draft here meant a caller with no evidence
		// to replay against silently received an unchanged workflow — the user
		// approved a fix and nothing happened. Hand back the validated candidate
		// flagged Unproven and let the caller decide.
		attempt.Unproven = true
		attempt.Reason = "validated, but not replayed (no replay available) — applied unproven"
		return candidate, attempt
	}
	outputs, err := replay(candidate, failingInput)
	if err != nil {
		attempt.Reason = "the replay failed: " + err.Error()
		return draft, attempt
	}
	attempt.Replayed = true

	// 4. JUDGE — did the replay actually achieve the outcome? A run that
	//    completes without error but still delivers nothing has not been fixed.
	if node, ok := outputs[proposal.NodeID]; ok {
		if strings.TrimSpace(node) == "" {
			attempt.Reason = "the repaired step ran but produced no output"
			return draft, attempt
		}
	}
	if contract.HasAssertions() {
		results := evaluateContractOnOutputs(contract, outputs)
		if failed := firstFailure(results); failed != "" {
			attempt.Reason = "the replay ran but did not meet the outcome contract: " + failed
			return draft, attempt
		}
	}
	attempt.ReplayPassed = true

	// 5. PROMOTE — only now.
	attempt.Promoted = true
	attempt.Reason = "validated and proven by replaying the failing input"
	return candidate, attempt
}

// validateCandidateDraft compiles the candidate's graph — the same structural
// guarantee the old path gave, without a JSON round-trip. Templates, port
// contracts, cycle bounds and consent rules are all enforced by CompileFlow.
func validateCandidateDraft(d Draft) error {
	if _, err := reasoning.CompileFlow(d.spec()); err != nil {
		return err
	}
	// CompileFlow validates STRUCTURE (ids, ports, kinds, cycle bounds) but does
	// not parse node input templates — a repair whose whole purpose is to
	// rewrite a template would otherwise sail through the very check meant to
	// catch it. Parse with the renderer's own function set so "valid here" and
	// "renders at run time" cannot disagree.
	funcs := reasoning.FlowTemplateFuncs()
	for _, n := range d.Flow.Nodes {
		for field, tmpl := range map[string]string{"input": n.Input, "for_each": n.ForEach} {
			if strings.TrimSpace(tmpl) == "" {
				continue
			}
			if _, err := template.New(n.ID).Funcs(funcs).Parse(tmpl); err != nil {
				return fmt.Errorf("node %q %s template: %w", n.ID, field, err)
			}
		}
	}
	for i, e := range d.Flow.Edges {
		if strings.TrimSpace(e.If) == "" {
			continue
		}
		if _, err := template.New("edge").Funcs(funcs).Parse(e.If); err != nil {
			return fmt.Errorf("edge %d predicate: %w", i, err)
		}
	}
	return nil
}

// evaluateContractOnOutputs judges a contract against replayed node outputs.
func evaluateContractOnOutputs(contract *agent.OutcomeContract, outputs map[string]string) []AssertionResult {
	trace := make([]TraceEntry, 0, len(outputs))
	for node, out := range outputs {
		trace = append(trace, TraceEntry{NodeID: node, Output: []byte(out)})
	}
	assertions := make([]Assertion, 0, len(contract.Assertions))
	for _, a := range contract.Assertions {
		assertions = append(assertions, Assertion{Target: a.Target, Op: a.Op, Value: a.Value})
	}
	var final []byte
	if out, ok := outputs["result"]; ok {
		final = []byte(out)
	}
	return EvaluateAssertions(assertions, trace, final)
}

func firstFailure(results []AssertionResult) string {
	for _, r := range results {
		if !r.Pass {
			return r.Detail
		}
	}
	return ""
}

// cloneDraft deep-copies the parts a repair can touch, so the candidate and the
// original never share backing arrays.
func cloneDraft(d Draft) Draft {
	cp := d
	cp.Flow.Nodes = append([]sdkr.FlowNode(nil), d.Flow.Nodes...)
	cp.Flow.Edges = append([]sdkr.FlowEdge(nil), d.Flow.Edges...)
	return cp
}

// applyProposalToDraft writes a proposal's New value onto its node. Returns
// false when the node no longer exists (a stale proposal against an edited
// draft), which must not silently succeed.
func applyProposalToDraft(d *Draft, p RepairProposal) bool {
	for i := range d.Flow.Nodes {
		if d.Flow.Nodes[i].ID != p.NodeID {
			continue
		}
		switch p.Field {
		case "input":
			d.Flow.Nodes[i].Input = p.New
		case "code":
			d.Flow.Nodes[i].Code = p.New
		default:
			return false
		}
		return true
	}
	return false
}

// String renders an attempt for a log line or an operator-facing summary.
func (a RepairAttempt) String() string {
	verdict := "not promoted"
	if a.Promoted {
		verdict = "promoted"
	}
	return fmt.Sprintf("repair %s on %q: %s (%s)", a.Class, a.NodeID, verdict, a.Reason)
}
