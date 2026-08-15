package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"

	reasoningpkg "github.com/soulacy/soulacy/internal/reasoning"
	"github.com/soulacy/soulacy/internal/studio"
)

// studio_repair.go — post-run "learn from Run Live and adjust" endpoints.
//
//	POST /studio/repair-live  — given a draft + the last run's per-node trace,
//	                            return reviewable repair proposals (deterministic
//	                            shape adapters first, LLM rewrite for novel shapes).
//	POST /studio/apply-repair — apply ONE approved proposal TRANSACTIONALLY:
//	                            isolate it on a copy, validate, replay the
//	                            original failing input in the SANDBOX, judge the
//	                            outcome contract, and promote only if all of that
//	                            passes — otherwise roll back and say so. Nothing
//	                            is auto-applied; the client drives approval.

type repairLiveRequest struct {
	Workflow  studio.Draft          `json:"workflow"`
	NodeTrace []repairTraceNodeJSON `json:"node_trace"`
}

type repairTraceNodeJSON struct {
	NodeID     string `json:"node_id"`
	Kind       string `json:"kind"`
	Input      string `json:"input"`
	Output     string `json:"output"`
	InputFull  string `json:"input_full"`
	OutputFull string `json:"output_full"`
	Error      string `json:"error"`
}

// pick returns the full field when present, else the truncated one — so repair
// diagnosis always uses the most complete data the client has.
func pick(full, short string) string {
	if strings.TrimSpace(full) != "" {
		return full
	}
	return short
}

// studioLiveRunsFromTrace enriches the runtime's exact per-node evidence with
// the draft output-variable names needed for shape diagnosis. Render failures
// carry the original input template; successful producers carry their real
// output bytes, so the repair model can reason about the actual contract break.
func studioLiveRunsFromTrace(draft studio.Draft, entries []reasoningpkg.FlowNodeRun) []studio.LiveNodeRun {
	outVar := map[string]string{}
	for _, n := range draft.Flow.Nodes {
		outVar[n.ID] = n.Output
	}
	runs := make([]studio.LiveNodeRun, 0, len(entries))
	for _, entry := range entries {
		runs = append(runs, studio.LiveNodeRun{
			NodeID:    entry.NodeID,
			Kind:      entry.Kind,
			Input:     entry.Input,
			Output:    entry.Output,
			Error:     entry.Error,
			OutputVar: outVar[entry.NodeID],
		})
	}
	return runs
}

// studioRepairDraftFromTrace performs bounded evidence-based repair on a
// temporary draft. Deterministic adapters run first; the LLM may then rewrite
// only one field on a failing node. Advisory-only proposals are preserved for
// the UI but never applied.
func studioRepairDraftFromTrace(ctx context.Context, model studio.LLM, draft studio.Draft, entries []reasoningpkg.FlowNodeRun) (studio.Draft, []studio.RepairProposal) {
	proposals := studio.ProposeLiveRepairs(ctx, model, draft, studioLiveRunsFromTrace(draft, entries))
	applied := make([]studio.RepairProposal, 0, len(proposals))
	for _, proposal := range proposals {
		if strings.TrimSpace(proposal.New) == "" {
			continue
		}
		if studio.ApplyProposal(&draft, proposal) {
			applied = append(applied, proposal)
		}
	}
	return draft, applied
}

// handleStudioRepairLive turns a live trace into repair proposals. It maps each
// traced node back to its draft Output var so the shape diagnosis can find the
// producer of a mismatched value. A missing LLM still yields deterministic
// proposals; only novel shapes need the model.
func (s *Server) handleStudioRepairLive(c *fiber.Ctx) error {
	var req repairLiveRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid body: "+err.Error())
	}
	runs := make([]studio.LiveNodeRun, 0, len(req.NodeTrace))
	outVar := map[string]string{}
	for _, n := range req.Workflow.Flow.Nodes {
		outVar[n.ID] = n.Output
	}
	for _, t := range req.NodeTrace {
		runs = append(runs, studio.LiveNodeRun{
			NodeID:    t.NodeID,
			Kind:      t.Kind,
			Input:     pick(t.InputFull, t.Input),
			Output:    toRawJSON(pick(t.OutputFull, t.Output)),
			Error:     t.Error,
			OutputVar: outVar[t.NodeID],
		})
	}

	proposals := studio.ProposeLiveRepairs(c.Context(), s.studioLLM(c), req.Workflow, runs)
	if proposals == nil {
		proposals = []studio.RepairProposal{}
	}
	return c.JSON(fiber.Map{"proposals": proposals})
}

type applyRepairRequest struct {
	Workflow studio.Draft          `json:"workflow"`
	Proposal studio.RepairProposal `json:"proposal"`
	// FailingInput is the trigger text of the run that failed. Replayed verbatim:
	// a patch that cannot survive the input that broke it has not been shown to
	// fix anything.
	FailingInput string `json:"failing_input,omitempty"`
	// NodeTrace is the failing run's per-node evidence — the SAME body
	// /studio/repair-live already receives. It is what makes the replay mean
	// something: each traced node's real output is fed back into the sandbox as a
	// mock, so the repaired template is re-rendered against the shape the
	// provider actually returned rather than against a synthetic stub.
	NodeTrace []repairTraceNodeJSON `json:"node_trace,omitempty"`
	// Preview asks for the verdict and the candidate WITHOUT the durable
	// consequences. The judgement is identical; only the learning is suppressed.
	// Without this the UI's "show me what this would change" button silently
	// taught the generator from a repair the user had not accepted yet — a
	// side effect from merely looking.
	Preview bool `json:"preview,omitempty"`
}

// applyRepairResponse is the auditable verdict for one transactional repair.
//
// Workflow is the draft the client should ADOPT: the repaired candidate when the
// replay proved it, and the UNCHANGED original when it did not. That is the
// whole point — before this, a proposal that parsed cleanly and fixed nothing
// was indistinguishable from one that worked, and the user found out on the next
// real run.
type applyRepairResponse struct {
	Workflow studio.Draft `json:"workflow"`
	// Applied mirrors Attempt.Promoted at the top level, because "did my change
	// stick?" is the first question the client asks.
	Applied bool `json:"applied"`
	// RolledBack is true when a change was built, judged, and discarded. It is
	// distinct from !Applied: a proposal that never matched a node was never
	// applied and never rolled back either.
	RolledBack bool `json:"rolled_back"`
	// Valid / Errors are the structural re-check of the returned draft, kept for
	// clients that only ever consumed those.
	Valid  bool     `json:"valid"`
	Errors []string `json:"errors"`
	// Attempt is the full record: diff, rationale, evidence, validated/replayed/
	// replay_passed, the reason, and the rollback value needed to undo it.
	Attempt studio.RepairAttempt `json:"attempt"`
	// Failure is the CLASS of the original failure plus what to do about it, so
	// the client shows "the provider rejected these credentials" instead of
	// re-reading the provider's prose error to the user.
	Failure *studio.RepairAdvice `json:"failure,omitempty"`
	// Verification says how the repair was judged. Sandboxed is always true here:
	// the replay runs on the mocked walk, never against real tools.
	Verification applyRepairVerification `json:"verification"`
}

// applyRepairVerification describes HOW the verdict was reached, in the terms an
// operator cares about: was it actually run, was it run for real, did it pass.
type applyRepairVerification struct {
	Sandboxed bool `json:"sandboxed"`
	Replayed  bool `json:"replayed"`
	Passed    bool `json:"passed"`
	// EvidenceSeeded distinguishes the two strengths of proof. True: the replay
	// was fed the failing run's real per-node outputs, so the fix was re-rendered
	// against the shape that actually broke it. False: it ran against synthetic
	// stubs — still a real execution, but it cannot prove the fix handles the
	// provider's actual response. The client should say which one it got rather
	// than presenting both as "verified".
	EvidenceSeeded bool `json:"evidence_seeded"`
	// Steps is the sandbox trace, so "it passed" is inspectable.
	Steps []string `json:"steps,omitempty"`
	// Note explains an absent replay rather than leaving the client to guess.
	Note string `json:"note,omitempty"`
}

// handleStudioApplyRepair applies one approved proposal TRANSACTIONALLY:
// isolate → validate → replay the original failing input in the sandbox →
// re-evaluate the outcome contract → promote, or roll back.
//
// The replay deliberately runs on the MOCKED walk (studio.TestRun, the same
// engine studio.VerifierFor(studio.SideEffectsMocked) selects). Verifying a
// repair is the one moment where a draft that is by definition not yet known to
// be correct would otherwise be executed against real tools — repeatedly, once
// per proposal. Proving a fix must never cost the user a real message, a real
// ticket, or a real charge, so real execution is not reachable from this path at
// all: there is no flag here that promotes it.
func (s *Server) handleStudioApplyRepair(c *fiber.Ctx) error {
	var req applyRepairRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid body: "+err.Error())
	}

	// Classify the ORIGINAL failure from the trace, so the client gets a real
	// error class (auth / permission / rate_limit / network / assertion / shape
	// drift) instead of a provider's prose. This also tells it when retrying
	// unchanged is the right answer and no repair should be applied at all.
	var advice *studio.RepairAdvice
	if run, ok := studioFailingRun(req.Workflow, req.NodeTrace, req.Proposal.NodeID); ok {
		a := studio.AdviseRepair(studio.ClassifyFailure(run))
		advice = &a
	}

	replay, note := s.studioSandboxReplay(c.Context(), req.NodeTrace)
	candidate, attempt := studio.ApplyRepairTransactionally(
		req.Workflow,
		req.Proposal,
		req.FailingInput,
		req.Workflow.Outcome.ToAgentContract(),
		replay,
		time.Now(),
	)
	if !attempt.Promoted && !attempt.Validated && attempt.Reason == "the proposal did not match any node in the draft" {
		return fiber.NewError(fiber.StatusBadRequest, "proposal did not match any node/field")
	}

	raw, _ := json.Marshal(candidate)
	check := studio.NormalizeAndCheck(string(raw))
	// Adopt on proof OR on "nothing could disprove it". Only a repair the replay
	// actively rejected is withheld.
	adopted := (attempt.Promoted || attempt.Unproven) && check.Valid

	// The two things learned here need DIFFERENT standards of evidence.
	//
	// A lesson records what the provider actually returned ("the list is under
	// items, not results") — observed facts from the failing run, true whether or
	// not this particular patch was ever replayed. Gating it on promotion threw
	// away real evidence and silently killed the learning loop for every client
	// that does not post a trace.
	//
	// A corpus case is the stronger claim that THIS repaired workflow is a good
	// example to generate from. That one asserts the patch works, so it requires
	// the replay to have actually proved it.
	if adopted && !req.Preview {
		s.recordLessonFromRepair(s.studio(c), req.Workflow, req.Proposal)
	}
	if attempt.Promoted && check.Valid && !req.Preview {
		s.recordCorpusCase(candidate, req.Proposal.NodeID)
	}

	res := applyRepairResponse{
		Workflow:   candidate,
		Applied:    adopted,
		RolledBack: !adopted && attempt.Validated,
		Valid:      check.Valid,
		Errors:     check.Errors,
		Attempt:    attempt,
		Failure:    advice,
		Verification: applyRepairVerification{
			Sandboxed: true,
			Replayed:  attempt.Replayed,
			Passed:    attempt.ReplayPassed,
			// An empty note means the replay ran against the failing run's real
			// per-node outputs; the note is only set when there was no evidence
			// and the repair was therefore applied unproven.
			EvidenceSeeded: note == "",
			Steps:          studioReplaySteps(candidate, attempt),
			Note:           note,
		},
	}
	if res.Errors == nil {
		res.Errors = []string{}
	}
	return c.JSON(res)
}

// studioReplaySteps renders the sandbox walk as readable lines. Only meaningful
// once a replay happened; an unproven repair has nothing to show.
func studioReplaySteps(d studio.Draft, attempt studio.RepairAttempt) []string {
	if !attempt.Replayed {
		return nil
	}
	steps := make([]string, 0, len(d.Flow.Nodes))
	for _, n := range d.Flow.Nodes {
		steps = append(steps, "replayed step "+n.ID+" ("+n.Kind+")")
	}
	return steps
}

// studioFailingRun picks the node run to classify: the one the proposal targets
// when it errored, otherwise the first errored node. Falls back to the proposal's
// own node so an "it ran and produced nothing" failure is still classifiable.
func studioFailingRun(draft studio.Draft, trace []repairTraceNodeJSON, nodeID string) (studio.LiveNodeRun, bool) {
	if len(trace) == 0 {
		return studio.LiveNodeRun{}, false
	}
	outVar := map[string]string{}
	for _, n := range draft.Flow.Nodes {
		outVar[n.ID] = n.Output
	}
	build := func(t repairTraceNodeJSON) studio.LiveNodeRun {
		return studio.LiveNodeRun{
			NodeID:    t.NodeID,
			Kind:      t.Kind,
			Input:     pick(t.InputFull, t.Input),
			Output:    toRawJSON(pick(t.OutputFull, t.Output)),
			Error:     t.Error,
			OutputVar: outVar[t.NodeID],
		}
	}
	var firstError *repairTraceNodeJSON
	for i := range trace {
		if trace[i].NodeID == nodeID && strings.TrimSpace(trace[i].Error) != "" {
			return build(trace[i]), true
		}
		if firstError == nil && strings.TrimSpace(trace[i].Error) != "" {
			firstError = &trace[i]
		}
	}
	if firstError != nil {
		return build(*firstError), true
	}
	for i := range trace {
		if trace[i].NodeID == nodeID {
			return build(trace[i]), true
		}
	}
	return studio.LiveNodeRun{}, false
}

// studioSandboxReplay builds the ReplayFunc that proves a repair, seeded with
// the failing run's real per-node outputs.
//
// The seeding is what makes the check worth running: without it the sandbox
// feeds every node a synthetic stub, so a template rewritten to match the shape
// the provider ACTUALLY returned would fail the very replay meant to confirm it.
// With it, the repaired node re-renders against the same bytes that broke it.
//
// Returns a nil ReplayFunc when there is no evidence to replay against. That is
// deliberate: ApplyRepairTransactionally then refuses to promote and says so
// ("validated, but not replayed"), rather than adopting an unproven change or —
// worse — declaring an unprovable one verified.
func (s *Server) studioSandboxReplay(ctx context.Context, trace []repairTraceNodeJSON) (studio.ReplayFunc, string) {
	mocks := map[string]json.RawMessage{}
	for _, t := range trace {
		if strings.TrimSpace(t.NodeID) == "" {
			continue
		}
		if raw := toRawJSON(pick(t.OutputFull, t.Output)); len(raw) > 0 {
			mocks[t.NodeID] = raw
		}
	}
	// No evidence means no MEANINGFUL replay. Running the walk against synthetic
	// stubs would look like proof while producing false negatives: a shape-drift
	// fix reads a field the stub does not have, the walk errors, and a correct
	// repair gets rolled back for a reason that has nothing to do with it.
	//
	// So the replay is skipped rather than faked, and the caller falls back to
	// applying the user-approved change UNVERIFIED and saying so. Refusing to
	// apply it at all — the behaviour this replaces — meant every client that did
	// not post a trace silently got no repair.
	if len(mocks) == 0 {
		return nil, "no node_trace from the failing run was supplied, so this repair was applied on its structural check alone and has not been proven against the data that broke it — post the run's node_trace to have it replayed"
	}
	return func(candidate studio.Draft, input string) (map[string]string, error) {
		// SideEffectsMocked is not a parameter here — it is the only mode. The
		// mocked walk is the same one studio.VerifierFor(studio.SideEffectsMocked)
		// returns; no real runner is reachable from this call.
		res, err := studio.TestRun(ctx, candidate, input, &studio.TestOptions{
			Mocks: mocks,
			Mode:  "dry",
		})
		if err != nil {
			return nil, err
		}
		outputs := make(map[string]string, len(res.Trace)+1)
		for _, e := range res.Trace {
			if strings.TrimSpace(e.Error) != "" {
				return nil, errors.New("step " + e.NodeID + " failed on replay: " + e.Error)
			}
			outputs[e.NodeID] = string(e.Output)
		}
		if len(res.Result) > 0 {
			outputs["result"] = string(res.Result)
		}
		return outputs, nil
	}, ""
}

// toRawJSON turns a traced output string into JSON: pass valid JSON through,
// otherwise wrap plain text as a JSON string so downstream shape checks are
// consistent (a non-JSON payload reads as a string value).
func toRawJSON(s string) json.RawMessage {
	t := strings.TrimSpace(s)
	if t == "" {
		return nil
	}
	if json.Valid([]byte(t)) {
		return json.RawMessage(t)
	}
	b, _ := json.Marshal(s)
	return b
}
