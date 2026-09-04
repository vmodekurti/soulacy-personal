package studio

// certify.go — the production certification gate (P0-1).
//
// Certification is deliberately the LAST epic built, because it is not an
// independent feature: it is the composition of everything before it. A
// certificate that asserted "this agent is production-ready" without contracts
// (P0-2), discovered tool schemas (P0-3), outcome assertions (P0-4), a capable
// model (P0-5), and a proven repair path (P0-7) would be a rubber stamp — the
// most dangerous artefact in the system, because it converts an unchecked
// workflow into one an operator believes has been checked.
//
// So each requirement here delegates to the machinery that actually knows the
// answer, and the gate's own job is only to insist that every question was
// asked and answered before scheduled execution is allowed.
//
// The one rule worth stating outright: a DRY RUN can never satisfy
// certification. A mock runner proves the graph is wired; it proves nothing
// about whether the provider answers, the credentials work, or the message
// arrives. Requiring a real integration run is the difference between "this
// compiles" and "this worked".

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/soulacy/soulacy/pkg/agent"
)

// CertRequirement is one checked precondition.
type CertRequirement struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail"`
	// Fix is the concrete action that resolves this requirement. Every failed
	// requirement carries one — P0-1's "displays every failed requirement with a
	// direct repair action" is only meaningful if the action is specific.
	Fix string `json:"fix,omitempty"`
	// Action is a machine-readable hint the GUI turns into a button:
	// "open_preflight" | "open_providers" | "open_mcp" | "open_delivery" |
	// "add_assertions" | "run_live" | "choose_model" | "open_studio".
	Action string `json:"action,omitempty"`
}

// CertificationRecord is the audit artefact: what was certified, against what,
// and when. Without the versions this is just a timestamp — the point is to be
// able to answer "what exactly was proven, and does it still hold".
type CertificationRecord struct {
	AgentID      string `json:"agent_id"`
	AgentVersion string `json:"agent_version"`
	Model        string `json:"model"`
	Provider     string `json:"provider"`
	// ToolVersions maps each tool to the schema hash it was certified against,
	// so drift (P0-3) can revoke precisely the certificates it invalidates.
	ToolVersions map[string]string `json:"tool_versions,omitempty"`
	// RunID is the REAL integration run that satisfied certification.
	RunID string `json:"run_id,omitempty"`
	// Outcome is that run's business-outcome class.
	Outcome      string            `json:"outcome,omitempty"`
	Requirements []CertRequirement `json:"requirements"`
	Certified    bool              `json:"certified"`
	CertifiedAt  string            `json:"certified_at,omitempty"`
}

// CertificationInput is the live evidence the gate reasons over. Assembled by
// the gateway, which is the only layer that can see all of it at once.
type CertificationInput struct {
	Definition agent.Definition
	Catalog    Catalog
	Preflight  PreflightResult
	Contract   ContractResult

	// ConnectedMCP / ChannelsConfigured / SecretsSet mirror PreflightInput.
	ConnectedMCP       map[string]bool
	ChannelsConfigured map[string]bool
	SecretsSet         map[string]bool
	// RequiredSecrets are the credential names this agent needs. Supplied by
	// the caller rather than guessed here: only the gateway knows how a
	// provider/channel maps onto a vault key, and inventing that mapping would
	// produce false blockers — worse than no check.
	RequiredSecrets []string

	// LastRealRun describes the most recent REAL (non-dry) run, if any.
	LastRealRun *RealRunEvidence
	// RestartTested reports whether a scheduled agent survived the
	// restart-and-retry check.
	RestartTested bool
	// Drift is the result of comparing captured tool schemas against live ones.
	Drift []ToolDrift
}

// RealRunEvidence is proof that the agent ran for real and what came of it.
type RealRunEvidence struct {
	RunID string `json:"run_id"`
	// Dry marks a mock run. A dry run can never certify.
	Dry bool `json:"dry"`
	// Succeeded reports that the run completed without a node error.
	Succeeded bool `json:"succeeded"`
	// OutcomeMet reports that its business-outcome contract was satisfied —
	// which is a different and stronger claim than Succeeded.
	OutcomeMet bool   `json:"outcome_met"`
	Outcome    string `json:"outcome,omitempty"`
	At         string `json:"at,omitempty"`
}

// NewCertificationInput assembles the gate's evidence from the four things a
// save/deploy path always holds: the definition it is about to persist, the
// grounded catalog, the preflight INPUT (which already carries the connected
// MCP servers, configured channels and stored secrets), and the two verdicts
// computed from them.
//
// It exists so the caller does not have to re-derive the environment maps by
// hand and get them subtly wrong — a certification built from a half-populated
// input reports false blockers, which trains operators to ignore the gate.
//
// Tool drift is computed here rather than asked for, because the two inputs it
// needs (the captured snapshot on the definition, and the live catalog) are both
// already present. The three facets that CANNOT be derived from a save-time
// snapshot — the real run, the restart test, and the required-credential list —
// stay caller-supplied: only the gateway knows how a provider or channel maps
// onto a vault key, and guessing would manufacture blockers that no operator can
// clear. Leaving them empty fails the corresponding requirement, which is the
// correct verdict for an agent that has genuinely never been run.
func NewCertificationInput(def agent.Definition, in PreflightInput, pre PreflightResult, contract ContractResult) CertificationInput {
	return CertificationInput{
		Definition:         def,
		Catalog:            in.Catalog,
		Preflight:          pre,
		Contract:           contract,
		ConnectedMCP:       in.ConnectedMCP,
		ChannelsConfigured: in.ChannelsConfigured,
		SecretsSet:         in.SecretsSet,
		Drift:              DetectToolDrift(def.ToolSchemas, in.Catalog),
	}
}

// WithRealRun records the proving run on the input. Returns the input so the
// assembly reads as one expression at the call site.
func (in CertificationInput) WithRealRun(ev *RealRunEvidence) CertificationInput {
	in.LastRealRun = ev
	return in
}

// WithRequiredSecrets records the credential names this agent needs. Supplied
// by the caller, never guessed here — see CertificationInput.RequiredSecrets.
func (in CertificationInput) WithRequiredSecrets(names ...string) CertificationInput {
	in.RequiredSecrets = append(append([]string(nil), in.RequiredSecrets...), names...)
	return in
}

// WithRestartTested records the restart-and-retry result (scheduled agents only).
func (in CertificationInput) WithRestartTested(ok bool) CertificationInput {
	in.RestartTested = ok
	return in
}

// Certify evaluates every requirement and produces the record. It never
// mutates anything: certification is a verdict, and applying it (enabling a
// schedule) is the caller's separate, explicit act.
func Certify(in CertificationInput, now time.Time) CertificationRecord {
	rec := CertificationRecord{
		AgentID:      in.Definition.ID,
		AgentVersion: strings.TrimSpace(in.Definition.Version),
		Model:        in.Definition.LLM.Model,
		Provider:     in.Definition.LLM.Provider,
	}
	if snap := in.Definition.ToolSchemas; snap.HasTools() {
		rec.ToolVersions = map[string]string{}
		for _, t := range snap.Tools {
			rec.ToolVersions[t.Tool] = t.Hash
		}
	}

	add := func(r CertRequirement) { rec.Requirements = append(rec.Requirements, r) }

	// ── 1. No contract or preflight blockers ────────────────────────────────
	blockers := len(in.Preflight.Blockers) + in.Contract.Blockers
	add(CertRequirement{
		ID: "no_blockers", Title: "Workflow validates cleanly",
		Passed: blockers == 0,
		Detail: fmt.Sprintf("%d blocking issue(s)", blockers),
		Fix:    "Resolve every blocking issue in the workflow — each one lists a specific fix.",
		Action: "open_preflight",
	})

	// ── 2. Credentials present ──────────────────────────────────────────────
	// PreflightInput.SecretsSet was collected and never read, so a missing
	// credential was completely unenforced. Checked here against a caller-
	// supplied required list, so the check is exact rather than guessed.
	missing := missingSecrets(in.RequiredSecrets, in.SecretsSet)
	add(CertRequirement{
		ID: "credentials", Title: "Required credentials are configured",
		Passed: len(missing) == 0,
		Detail: secretsDetail(missing),
		Fix:    "Add the missing credential(s) in Secrets, then re-test the provider.",
		Action: "open_providers",
	})

	// ── 3. MCP servers connected ────────────────────────────────────────────
	disconnected := disconnectedServers(in.Definition, in.ConnectedMCP)
	add(CertRequirement{
		ID: "mcp_connected", Title: "Every MCP server this agent uses is connected",
		Passed: len(disconnected) == 0,
		Detail: listOrNone(disconnected, "disconnected: "),
		Fix:    "Start or reconnect the server(s), or replace the blocks that call them.",
		Action: "open_mcp",
	})

	// ── 4. Delivery destination valid ───────────────────────────────────────
	badDest := invalidDestinations(in.Definition, in.ChannelsConfigured)
	add(CertRequirement{
		ID: "destinations", Title: "Delivery destinations are configured",
		Passed: len(badDest) == 0,
		Detail: listOrNone(badDest, "unconfigured: "),
		Fix:    "Configure the channel and destination, or remove the delivery step.",
		Action: "open_delivery",
	})

	// ── 5. Substantive outcome assertions ───────────────────────────────────
	strength := AssessAssertions(contractAssertions(in.Definition.Outcome))
	add(CertRequirement{
		ID: "assertions", Title: "Business outcomes are asserted",
		Passed: strength.OK,
		Detail: assertionDetail(strength),
		Fix:    strengthFix(strength),
		Action: "add_assertions",
	})

	// ── 6. Empty-result handling ────────────────────────────────────────────
	// The specific failure mode: a run that completes cleanly and delivers
	// nothing. An agent is only certified if SOMETHING would catch that.
	add(CertRequirement{
		ID: "empty_handling", Title: "An empty result would be caught",
		Passed: handlesEmptyResult(in.Definition.Outcome),
		Detail: "at least one assertion must fail when the run produces nothing",
		Fix:    "Add a count_gte or not_empty assertion on the step that collects the data.",
		Action: "add_assertions",
	})

	// ── 7. A real integration run ───────────────────────────────────────────
	add(realRunRequirement(in.LastRealRun))

	// ── 8. Restart-and-retry, for scheduled agents only ─────────────────────
	if isScheduled(in.Definition) {
		add(CertRequirement{
			ID: "restart_retry", Title: "Survives a restart mid-schedule",
			Passed: in.RestartTested,
			Detail: "a scheduled agent must run exactly once across a gateway restart",
			Fix:    "Run the restart-and-retry test for this agent.",
			Action: "run_live",
		})
	}

	// ── 9. Model capable of the chosen strategy ─────────────────────────────
	add(modelRequirement(in.Definition))

	// ── 10. No breaking tool-schema drift ───────────────────────────────────
	add(CertRequirement{
		ID: "no_drift", Title: "Tool contracts are unchanged since the workflow was built",
		Passed: !NeedsRecertification(in.Drift),
		Detail: driftSummary(in.Drift),
		Fix:    "Open the affected block(s) and update them to the tool's current contract, then re-certify.",
		Action: "open_studio",
	})

	rec.Certified = true
	for _, r := range rec.Requirements {
		if !r.Passed {
			rec.Certified = false
			break
		}
	}
	if in.LastRealRun != nil {
		rec.RunID = in.LastRealRun.RunID
		rec.Outcome = in.LastRealRun.Outcome
	}
	if rec.Certified {
		rec.CertifiedAt = now.UTC().Format(time.RFC3339)
	}
	return rec
}

func realRunRequirement(ev *RealRunEvidence) CertRequirement {
	r := CertRequirement{
		ID: "real_run", Title: "Proven by a real run, not a dry run",
		Fix:    "Run this agent live once against real tools and confirm it achieves its stated outcome.",
		Action: "run_live",
	}
	switch {
	case ev == nil:
		r.Detail = "no real run recorded"
	case ev.Dry:
		// The distinction the whole requirement rests on.
		r.Detail = "only a dry run was recorded — a mock run proves the graph is wired, not that the provider answers, the credentials work, or the message arrives"
	case !ev.Succeeded:
		r.Detail = "the last real run failed"
	case !ev.OutcomeMet:
		r.Detail = "the last real run completed but did not meet its outcome contract (" + ev.Outcome + ")"
	default:
		r.Passed = true
		r.Detail = "run " + ev.RunID + " met its outcome contract"
	}
	return r
}

func modelRequirement(def agent.Definition) CertRequirement {
	strategy := strings.TrimSpace(def.Reasoning.Strategy)
	if def.Workflow != nil && strategy == "" {
		// A fixed workflow asks the least of a model; there is no strategy bar.
		return CertRequirement{
			ID: "model_capable", Title: "Model suits the execution strategy",
			Passed: true, Detail: "fixed workflow — no reasoning-capability requirement",
		}
	}
	warning := StrategyWarning(def.LLM.Provider, def.LLM.Model, strategy)
	return CertRequirement{
		ID: "model_capable", Title: "Model suits the execution strategy",
		Passed: warning == "",
		Detail: nonEmptyOr(warning, "the selected model supports "+orDefault(strategy, "this strategy")),
		Fix:    "Choose a model that supports this strategy, or switch the agent to a fixed Workflow.",
		Action: "choose_model",
	}
}

// ── helpers ─────────────────────────────────────────────────────────────────

func contractAssertions(c *agent.OutcomeContract) []Assertion {
	if !c.HasAssertions() {
		return nil
	}
	out := make([]Assertion, 0, len(c.Assertions))
	for _, a := range c.Assertions {
		out = append(out, Assertion{Target: a.Target, Op: a.Op, Value: a.Value})
	}
	return out
}

// handlesEmptyResult reports whether any assertion would FAIL on an empty run.
// `exists` would not — it passes for any non-empty output, including an empty
// list — which is precisely why it cannot satisfy this requirement.
func handlesEmptyResult(c *agent.OutcomeContract) bool {
	if !c.HasAssertions() {
		return false
	}
	for _, a := range c.Assertions {
		switch a.Op {
		case OpNotEmpty, OpCountGTE, OpCountEQ, OpDelivered, OpDestination, OpArtifact, OpHasField, OpFieldEquals:
			return true
		}
	}
	return false
}

func missingSecrets(required []string, set map[string]bool) []string {
	var missing []string
	for _, name := range required {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if !set[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	return missing
}

func secretsDetail(missing []string) string {
	if len(missing) == 0 {
		return "all required credentials are present"
	}
	return "missing: " + strings.Join(missing, ", ")
}

func disconnectedServers(def agent.Definition, connected map[string]bool) []string {
	seen := map[string]bool{}
	var out []string
	if def.Workflow != nil {
		for _, n := range def.Workflow.Nodes {
			srv := mcpServerOf(n.Tool)
			if srv == "" || seen[srv] {
				continue
			}
			seen[srv] = true
			if !connected[srv] {
				out = append(out, srv)
			}
		}
	}
	if def.MCPTools != nil {
		for _, t := range *def.MCPTools {
			srv := mcpServerOf(t)
			if srv == "" || seen[srv] {
				continue
			}
			seen[srv] = true
			if !connected[srv] {
				out = append(out, srv)
			}
		}
	}
	sort.Strings(out)
	return out
}

func invalidDestinations(def agent.Definition, configured map[string]bool) []string {
	var out []string
	seen := map[string]bool{}
	for _, ch := range def.Channels {
		ch = strings.TrimSpace(ch)
		if ch == "" || seen[ch] {
			continue
		}
		seen[ch] = true
		if !configured[ch] {
			out = append(out, ch)
		}
	}
	// A scheduled agent that delivers must name a destination, not just a channel.
	if def.Schedule != nil && def.Schedule.Output != nil {
		o := def.Schedule.Output
		if strings.TrimSpace(o.Channel) != "" && strings.TrimSpace(o.To) == "" {
			out = append(out, o.Channel+" (no destination)")
		}
	}
	sort.Strings(out)
	return out
}

func isScheduled(def agent.Definition) bool {
	return def.Trigger == agent.TriggerCron || def.Schedule != nil
}

func assertionDetail(s AssertionStrength) string {
	if s.OK {
		return fmt.Sprintf("%d assertion(s), %d substantive", s.Total, s.Substantive)
	}
	if len(s.Reasons) > 0 {
		return s.Reasons[0]
	}
	return "no substantive assertions"
}

func strengthFix(s AssertionStrength) string {
	if s.Fix != "" {
		return s.Fix
	}
	return "Add an assertion describing what a successful run must produce."
}

func driftSummary(drift []ToolDrift) string {
	if len(drift) == 0 {
		return "no drift detected"
	}
	var parts []string
	for _, d := range drift {
		label := d.Tool
		if len(d.Nodes) > 0 {
			label += " (" + strings.Join(d.Nodes, ", ") + ")"
		}
		parts = append(parts, label)
	}
	return strings.Join(parts, "; ")
}

func listOrNone(items []string, prefix string) string {
	if len(items) == 0 {
		return "none"
	}
	return prefix + strings.Join(items, ", ")
}

func nonEmptyOr(v, fallback string) string {
	if strings.TrimSpace(v) != "" {
		return v
	}
	return fallback
}

func orDefault(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}

// FailedRequirements returns only the unmet requirements, for a UI that shows
// what remains rather than a wall of green ticks.
func (r CertificationRecord) FailedRequirements() []CertRequirement {
	var out []CertRequirement
	for _, req := range r.Requirements {
		if !req.Passed {
			out = append(out, req)
		}
	}
	return out
}

// BlocksScheduling reports whether scheduled execution must stay disabled.
// P0-1: "scheduled execution cannot be enabled before certification".
func (r CertificationRecord) BlocksScheduling() bool { return !r.Certified }

// Summary is a one-line verdict for a log or a status row.
func (r CertificationRecord) Summary() string {
	if r.Certified {
		return fmt.Sprintf("certified at %s (run %s)", r.CertifiedAt, orDefault(r.RunID, "n/a"))
	}
	failed := r.FailedRequirements()
	names := make([]string, 0, len(failed))
	for _, f := range failed {
		names = append(names, f.ID)
	}
	return fmt.Sprintf("not certified — %d requirement(s) unmet: %s", len(failed), strings.Join(names, ", "))
}
