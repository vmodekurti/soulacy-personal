// readiness.go — ONE readiness verdict for a draft (ST-07/ST-08).
//
// The failure mode this replaces: readiness was stitched together in the
// browser from three separate calls — /studio/compile, /studio/security_review
// and /studio/plan — and the "ok" badge was computed from whichever of them
// happened to answer. Two consequences, both bad:
//
//   - A non-GUI caller (CLI, deploy script, another service) that hit only one
//     endpoint got a partial picture it had no way to know was partial.
//   - When the security-review request failed, the client dropped that section
//     and still rendered ok=true. A silent omission that reads as a pass is
//     worse than an error, because it looks like an answer.
//
// Readiness composes the four sources server-side and reports every section it
// could NOT evaluate as Unknown, which forces OK=false. "We didn't check" and
// "we checked and it's fine" must never be the same output.
package studio

import (
	"fmt"
	"sort"
	"strings"

	"github.com/soulacy/soulacy/pkg/agent"
)

// Readiness section ids. Stable tokens — a client keys its UI off these.
const (
	ReadinessSectionPreflight = "preflight"
	ReadinessSectionContract  = "contract"
	ReadinessSectionSecurity  = "security"
	ReadinessSectionConsent   = "consent"
)

// Readiness section/item statuses.
const (
	ReadinessReady   = "ready"
	ReadinessWarning = "warning"
	ReadinessBlocked = "blocked"
	ReadinessUnknown = "unknown"
)

// ReadinessInput is everything the composed verdict needs. Only Draft is
// mandatory; every other field is zero-value safe and simply narrows what can
// be judged (an unsupplied input yields silence, never a false blocker).
type ReadinessInput struct {
	Draft   Draft
	Catalog Catalog
	// Preflight is the live-state input (connected MCP, channels, secrets,
	// providers, models). Shared verbatim with Preflight/AssessContract so the
	// three sections cannot disagree about the environment.
	Preflight PreflightInput
	// Definition is the persisted agent when the draft has been saved before.
	// It unlocks the security/persona/capability checks that need fields Draft
	// does not carry. nil for a brand-new draft — those checks then skip.
	Definition *agent.Definition
	// IntentGateDefault is the workspace-scoped intent-gate fallback, threaded
	// so the security summary matches the effective runtime mode.
	IntentGateDefault string
	// ConsentAccepted records that the operator has already granted the
	// privileged-exposure consent this draft requires. Without it, a draft that
	// needs consent is correctly NOT ready: the save path will refuse it.
	ConsentAccepted bool
	// Unavailable marks sections the CALLER could not gather state for, keyed by
	// section id with a human reason. Each named section is reported Unknown and
	// forces OK=false. This is the explicit channel for partial failure — the
	// thing the client-side stitching did implicitly and invisibly.
	Unavailable map[string]string
}

// ReadinessItem is one finding, carrying the section it came from plus the same
// machine-readable action vocabulary as certify.go so the client can render a
// "Fix this" button rather than a sentence.
type ReadinessItem struct {
	Section      string            `json:"section"`
	Severity     string            `json:"severity"` // block | warn | pass
	Kind         string            `json:"kind,omitempty"`
	NodeID       string            `json:"nodeId,omitempty"`
	Message      string            `json:"message"`
	Fix          string            `json:"fix,omitempty"`
	Action       string            `json:"action,omitempty"`
	ActionParams map[string]string `json:"actionParams,omitempty"`
	// ActionLabel is the button text, resolved from the shared vocabulary in
	// fixactions.go (or the finding's own override). It travels WITH the item
	// so every panel renders the same action the same way — three panels each
	// keeping their own label switch is how "Configure provider" in one place
	// became "Fix this" in another.
	ActionLabel string `json:"actionLabel,omitempty"`
}

// ReadinessSection is one evaluated (or unevaluated) source.
type ReadinessSection struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"` // ready | warning | blocked | unknown
	// Reason explains an unknown status: why this section could not be judged.
	Reason   string `json:"reason,omitempty"`
	Blockers int    `json:"blockers"`
	Warnings int    `json:"warnings"`
	Passes   int    `json:"passes"`
}

// ReadinessReport is the single verdict. OK is true only when every section was
// evaluated AND none of them blocks — an unknown section can never pass.
type ReadinessReport struct {
	OK       bool               `json:"ok"`
	Sections []ReadinessSection `json:"sections"`
	Blockers []ReadinessItem    `json:"blockers,omitempty"`
	Warnings []ReadinessItem    `json:"warnings,omitempty"`
	// Ready holds the checks that ran and passed, so a client can render the
	// full Ready/Warning/Blocker triage instead of only what is broken.
	Ready []ReadinessItem `json:"ready,omitempty"`
	// Unknown lists the ids of sections that could not be evaluated. Present as
	// its own field so a caller cannot miss them by only reading Blockers.
	Unknown []string `json:"unknown,omitempty"`
	Summary string   `json:"summary"`

	// The underlying reports, for clients that want the detail. Nil when the
	// corresponding section is Unknown — a nil report is honest about not
	// having run; an empty one would look like a clean result.
	Preflight *PreflightResult `json:"preflight,omitempty"`
	Contract  *ContractResult  `json:"contract,omitempty"`
	Security  *SecurityReview  `json:"security,omitempty"`
	Consent   *PlanResult      `json:"consent,omitempty"`
}

// Readiness composes preflight + contract assessment + security review +
// consent/plan into one verdict. Pure and deterministic: no I/O, no clock.
//
// Intended endpoint: POST /api/v1/studio/readiness — one call replacing the
// client-side stitching of /studio/compile + /studio/security_review +
// /studio/plan.
func Readiness(in ReadinessInput) ReadinessReport {
	var rep ReadinessReport
	add := func(sec ReadinessSection, items []ReadinessItem) {
		for _, it := range items {
			switch it.Severity {
			case "block":
				sec.Blockers++
				rep.Blockers = append(rep.Blockers, it)
			case "pass":
				sec.Passes++
				rep.Ready = append(rep.Ready, it)
			default:
				sec.Warnings++
				rep.Warnings = append(rep.Warnings, it)
			}
		}
		if sec.Status == "" {
			sec.Status = statusFor(sec.Blockers, sec.Warnings)
		}
		rep.Sections = append(rep.Sections, sec)
	}
	unknown := func(id, title, reason string) {
		rep.Unknown = append(rep.Unknown, id)
		rep.Sections = append(rep.Sections, ReadinessSection{
			ID: id, Title: title, Status: ReadinessUnknown,
			Reason: nonEmptyOr(reason, "this section could not be evaluated"),
		})
	}

	// The environment is shared by preflight and the contract, so both sections
	// judge the same world. A caller that supplies only ReadinessInput.Catalog
	// gets it threaded into the preflight input rather than silently ignored.
	pin := in.Preflight
	if pin.Catalog.Tools == nil && pin.Catalog.MCP == nil && pin.Catalog.Agents == nil {
		pin.Catalog = in.Catalog
	}
	cat := in.Catalog
	if cat.Tools == nil && cat.MCP == nil && cat.Agents == nil {
		cat = pin.Catalog
	}

	// ── preflight ───────────────────────────────────────────────────────────
	if reason, off := sectionUnavailable(in, ReadinessSectionPreflight); off {
		unknown(ReadinessSectionPreflight, "Runtime readiness", reason)
	} else {
		pf := Preflight(in.Draft, pin)
		rep.Preflight = &pf
		add(ReadinessSection{ID: ReadinessSectionPreflight, Title: "Runtime readiness"},
			preflightItems(pf))
	}

	// ── generation contract ─────────────────────────────────────────────────
	if reason, off := sectionUnavailable(in, ReadinessSectionContract); off {
		unknown(ReadinessSectionContract, "Generation contract", reason)
	} else {
		var opts []ContractOption
		if in.Definition != nil {
			opts = append(opts, WithAgentDefinition(in.Definition))
		}
		cr := AssessContract(in.Draft, cat, pin, opts...)
		rep.Contract = &cr
		items := contractItems(cr)
		if rep.Preflight != nil {
			// AssessContract runs preflight itself and republishes every issue it
			// finds as a "runtime.*" check, so that the contract is useful on its
			// own. Here it is not on its own: the preflight section above already
			// reported those exact issues. Counting both made every runtime finding
			// appear twice — the Save dialog said "Blockers (2)" from the contract
			// while the readiness summary beside it said "4 blockers, 4 warnings"
			// for the same two problems. Seen live on a generated workflow.
			//
			// Drop the contract's copies rather than preflight's: preflight is the
			// section that owns these, and its items carry the preflight-specific
			// fix actions.
			items = withoutPreflightEchoes(items)
		}
		add(ReadinessSection{ID: ReadinessSectionContract, Title: "Generation contract"},
			items)
	}

	// ── security review ─────────────────────────────────────────────────────
	if reason, off := sectionUnavailable(in, ReadinessSectionSecurity); off {
		unknown(ReadinessSectionSecurity, "Security review", reason)
	} else {
		sr := SecurityPreflight(in.Draft, in.Definition, in.IntentGateDefault)
		rep.Security = &sr
		add(ReadinessSection{ID: ReadinessSectionSecurity, Title: "Security review"},
			securityItems(sr))
	}

	// ── consent / plan ──────────────────────────────────────────────────────
	if reason, off := sectionUnavailable(in, ReadinessSectionConsent); off {
		unknown(ReadinessSectionConsent, "Consent", reason)
	} else if plan, err := Plan(in.Draft); err != nil {
		// Plan failing means the draft could not even be converted into the
		// agent it would become. That is not "no consent needed" — it is "we do
		// not know", and it must not read as a pass.
		unknown(ReadinessSectionConsent, "Consent", "the draft could not be classified: "+err.Error())
	} else {
		rep.Consent = &plan
		add(ReadinessSection{ID: ReadinessSectionConsent, Title: "Consent"},
			consentItems(plan, in.ConsentAccepted))
	}

	rep.OK = len(rep.Blockers) == 0 && len(rep.Unknown) == 0
	rep.Summary = readinessSummary(rep)
	return rep
}

// sectionUnavailable reports whether the caller declared a section
// un-evaluatable, and why.
func sectionUnavailable(in ReadinessInput, id string) (string, bool) {
	if in.Unavailable == nil {
		return "", false
	}
	reason, ok := in.Unavailable[id]
	return reason, ok
}

func statusFor(blockers, warnings int) string {
	switch {
	case blockers > 0:
		return ReadinessBlocked
	case warnings > 0:
		return ReadinessWarning
	default:
		return ReadinessReady
	}
}

// finishItem settles an item's action and button text. Node-scoped findings
// with no better destination become "reveal the step" rather than "open the
// editor" — the editor is already open, so pointing at it says nothing.
func finishItem(it ReadinessItem, override string) ReadinessItem {
	if it.Action == FixOpenStudio && strings.TrimSpace(it.NodeID) != "" {
		it.Action = FixRevealNode
	}
	if !IsFixAction(it.Action) {
		// Never ship an id the client cannot handle: a button that does nothing
		// is worse than prose. Drop it and let the Fix text carry the finding.
		it.Action = ""
	}
	it.ActionLabel = resolveFixLabel(it.Action, override)
	return it
}

func preflightItems(pf PreflightResult) []ReadinessItem {
	var out []ReadinessItem
	conv := func(issues []PreflightIssue) {
		for _, i := range issues {
			out = append(out, finishItem(ReadinessItem{
				Section: ReadinessSectionPreflight, Severity: i.Severity, Kind: i.Kind,
				NodeID: i.NodeID, Message: i.Message, Fix: i.Fix,
				Action: nonEmptyOr(i.Action, actionForKind(i.Kind)), ActionParams: i.ActionParams,
			}, ""))
		}
	}
	conv(pf.Blockers)
	conv(pf.Warnings)
	conv(pf.Passes)
	return out
}

// contractItems converts contract checks, mapping "pass"/"warn"/"block" onto
// the readiness severities. The contract already re-runs preflight internally,
// so its runtime.* checks are duplicates by construction — they are kept
// because the contract's SCORE is computed from them and a client showing the
// contract section expects them to be there.
func contractItems(cr ContractResult) []ReadinessItem {
	out := make([]ReadinessItem, 0, len(cr.Checks))
	for _, c := range cr.Checks {
		sev := c.Status
		if sev == "pass" || sev == "block" || sev == "warn" {
			// already in the shared vocabulary
		} else {
			sev = "warn"
		}
		out = append(out, finishItem(ReadinessItem{
			Section: ReadinessSectionContract, Severity: sev, Kind: c.ID,
			NodeID: c.NodeID, Message: c.Message, Fix: c.Fix,
			// The check's own action wins when it has one; the id-derived
			// mapping is only the fallback.
			Action:       nonEmptyOr(c.Action, actionForContractCheck(c.ID)),
			ActionParams: c.ActionParams,
		}, c.ActionLabel))
	}
	return out
}

// actionForContractCheck maps a contract check id ("runtime.mcp",
// "graph.integrity", …) onto the certify.go action vocabulary by its leading
// segment, so contract blockers are as actionable as preflight ones.
func actionForContractCheck(id string) string {
	seg := id
	if i := strings.LastIndex(id, "."); i >= 0 {
		seg = id[i+1:]
	}
	if a := actionForKind(seg); a != "open_studio" {
		return a
	}
	return "open_studio"
}

func securityItems(sr SecurityReview) []ReadinessItem {
	var out []ReadinessItem
	conv := func(fs []SecurityFinding) {
		for _, f := range fs {
			// A finding that names its own fix knows better than the
			// category mapping does — the category can only ever say "go to
			// this screen", never "Studio can do this for you".
			out = append(out, finishItem(ReadinessItem{
				Section: ReadinessSectionSecurity, Severity: f.Severity, Kind: f.Category,
				Message: f.Message, Fix: f.Fix,
				Action: nonEmptyOr(f.Action, actionForSecurityCategory(f.Category)),
			}, f.ActionLabel))
		}
	}
	conv(sr.Blockers)
	conv(sr.Warnings)
	if len(sr.Blockers) == 0 && len(sr.Warnings) == 0 {
		out = append(out, ReadinessItem{
			Section: ReadinessSectionSecurity, Severity: "pass", Kind: "security",
			Message: "The security review found no blocking capability or exposure issues.",
		})
	}
	return out
}

func actionForSecurityCategory(cat string) string {
	switch strings.ToLower(strings.TrimSpace(cat)) {
	case "channel":
		return "open_delivery"
	default:
		return "open_studio"
	}
}

// consentItems turns the plan verdict into readiness items. An UNGRANTED
// consent requirement is a blocker, not a warning: the save path refuses to
// persist the agent without it, so reporting it as a soft warning would let
// "ready" mean something the system will not actually let you do.
func consentItems(plan PlanResult, accepted bool) []ReadinessItem {
	if !plan.RequiresConsent {
		return []ReadinessItem{{
			Section: ReadinessSectionConsent, Severity: "pass", Kind: "consent",
			Message: "This agent is " + plan.Tier + " tier and needs no privileged-exposure consent.",
		}}
	}
	sev := "block"
	if accepted {
		sev = "pass"
	}
	out := make([]ReadinessItem, 0, len(plan.ConsentItems))
	for _, ci := range plan.ConsentItems {
		msg := "Requires consent (" + ci.Kind + " \"" + ci.Name + "\"): " + ci.Reason
		if accepted {
			msg = "Consent granted for " + ci.Kind + " \"" + ci.Name + "\": " + ci.Reason
		}
		action := FixOpenStudio
		label := ""
		if ci.Kind == "channel" {
			action = FixOpenDelivery
			label = "Review the binding"
		}
		// Through finishItem like every other source. Building the item by hand
		// here is how consent findings ended up with an action and no button
		// text — the UI rendered a button with nothing written on it.
		out = append(out, finishItem(ReadinessItem{
			Section: ReadinessSectionConsent, Severity: sev, Kind: "consent:" + ci.Kind,
			Message: msg,
			Fix:     "Review the exposure and accept it explicitly when saving, or remove the privileged capability / channel binding.",
			Action:  action, ActionParams: map[string]string{"name": ci.Name, "kind": ci.Kind},
		}, label))
	}
	if len(out) == 0 {
		// RequiresConsent with no items shouldn't happen; stay explicit rather
		// than silently reporting a clean consent section.
		out = append(out, ReadinessItem{
			Section: ReadinessSectionConsent, Severity: sev, Kind: "consent",
			Message: "This agent requires privileged-exposure consent.",
			Action:  "open_studio",
		})
	}
	return out
}

func readinessSummary(rep ReadinessReport) string {
	if rep.OK {
		return fmt.Sprintf("ready — %d check(s) passed, %d warning(s)", len(rep.Ready), len(rep.Warnings))
	}
	var parts []string
	if n := len(rep.Blockers); n > 0 {
		parts = append(parts, fmt.Sprintf("%d blocker(s)", n))
	}
	if n := len(rep.Unknown); n > 0 {
		ids := append([]string(nil), rep.Unknown...)
		sort.Strings(ids)
		parts = append(parts, fmt.Sprintf("%d section(s) could not be evaluated: %s", n, strings.Join(ids, ", ")))
	}
	return "not ready — " + strings.Join(parts, "; ")
}

// withoutPreflightEchoes removes the contract checks that are re-reports of
// preflight issues.
//
// AssessContract folds preflight in under ids prefixed "runtime." (see the
// add("runtime."+…) calls in contract.go). That is right when the contract is
// assessed alone. Inside Readiness, where preflight has its own section, those
// checks are the same findings a second time.
//
// Matching on the id prefix rather than on message text: the prefix is the
// contract's own declaration of where the check came from, whereas message
// text is prose that either side may reword.
func withoutPreflightEchoes(items []ReadinessItem) []ReadinessItem {
	out := items[:0:0]
	for _, it := range items {
		if strings.HasPrefix(it.Kind, "runtime.") {
			continue
		}
		out = append(out, it)
	}
	return out
}
