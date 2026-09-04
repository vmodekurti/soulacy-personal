// plan.go — Studio's "plan" step (Story M2). Before a Studio draft is saved
// and exposed to channels, Plan classifies the agent it would become and
// decides whether saving would create a PRIVILEGED-tier channel exposure
// that needs the operator's explicit consent.
//
// The logic lives here (not in the gateway) so it is unit-testable without
// an HTTP server: POST /api/v1/studio/plan is a thin wrapper over Plan, and
// POST /api/v1/studio/save reuses the same decision to enforce consent.
package studio

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/soulacy/soulacy/internal/studio/codeclass"
	"github.com/soulacy/soulacy/internal/tier"
)

// ConsentItem describes one concrete reason consent is being requested.
//
//   - kind "channel": binding a Privileged-tier workflow to a non-web channel
//     exposes the workflow's shell/write/install surface to that channel's users.
//   - kind "code": a Custom Python node runs BEYOND the default guardrails
//     (needs system/network, or uses dynamic execution). Per the per-case
//     consent policy (docs/STUDIO_PYTHON_TOOLS.md §13) each such node is its own
//     decision, bound to a content Hash so editing the code voids the consent.
type ConsentItem struct {
	Kind   string `json:"kind"`   // "channel" | "code"
	Name   string `json:"name"`   // channel id, or the node id for kind=code
	Reason string `json:"reason"` // human-readable justification

	// Capabilities (kind=code) lists what the node needs: subset of
	// {"system","network"}. Empty with Dynamic=true means review-only.
	Capabilities []string `json:"capabilities,omitempty"`
	// Dynamic (kind=code) flags eval/exec/__import__ the classifier can't see
	// through — raises the warning level.
	Dynamic bool `json:"dynamic,omitempty"`
	// Hash (kind=code) is the first 12 hex chars of sha256(code). A grant is
	// bound to this; a code edit changes the hash and voids prior consent.
	Hash string `json:"hash,omitempty"`
}

// PlanResult is the POST /api/v1/studio/plan response and the shared
// decision the save path enforces. Tier is the lowercase tier token
// ("read_only" is normalised to "readonly" for the wire contract).
type PlanResult struct {
	Tier            string        `json:"tier"`
	Reasons         []string      `json:"reasons"`
	RequiresConsent bool          `json:"requiresConsent"`
	ConsentItems    []ConsentItem `json:"consentItems"`
}

// Plan converts a draft into the agent.Definition it would be saved as,
// classifies its capability tier, and decides whether saving needs consent.
//
// requiresConsent is true iff BOTH:
//   - the workflow classifies as Privileged tier, AND
//   - the draft binds at least one channel.
//
// A Privileged workflow with no channel can't expose anything to channel
// users, so it needs no consent; an Active/ReadOnly workflow is allowed on
// channels by default (mirroring internal/app/channels.go's bindingDecision).
//
// When requiresConsent is true, one ConsentItem is emitted per bound channel,
// each carrying the tier reasons that justified the Privileged classification.
//
// The conversion is consent-agnostic (acceptPrivilegedExposure=false): Plan
// reports the situation, it does not apply consent.
func Plan(draft Draft) (PlanResult, error) {
	def, err := ToAgentDefinition(draft, false)
	if err != nil {
		return PlanResult{}, err
	}

	// No lookup: Studio classifies the draft in isolation. Named peer agents
	// referenced by the flow can't be resolved here, but the draft's own tool
	// surface (projected onto def.Builtins in ToAgentDefinition) is what
	// drives the Privileged decision for a freshly-authored workflow.
	exp := tier.Explain(&def, nil)

	res := PlanResult{
		Tier:    tierLabel(exp.Tier),
		Reasons: exp.Reasons,
	}
	if res.Reasons == nil {
		res.Reasons = []string{}
	}
	res.ConsentItems = []ConsentItem{}

	if exp.Tier == tier.Privileged && len(def.Channels) > 0 {
		res.RequiresConsent = true
		reason := joinReasons(exp.Reasons)
		for _, ch := range def.Channels {
			res.ConsentItems = append(res.ConsentItems, ConsentItem{
				Kind:   "channel",
				Name:   ch,
				Reason: reason,
			})
		}
	}

	// Per-case code consent (§13): every Custom Python node that runs beyond the
	// ReadOnly guardrails is its own consent decision, independent of channels —
	// running code/commands on the host needs the user's explicit OK each time,
	// bound to the exact code via a content hash.
	for _, item := range codeConsentItems(draft.Flow) {
		res.RequiresConsent = true
		res.ConsentItems = append(res.ConsentItems, item)
	}

	return res, nil
}

// codeConsentItems returns one ConsentItem per Custom Python node whose code is
// beyond the guardrails (needs system/network, or uses dynamic execution).
func codeConsentItems(flow Flow) []ConsentItem {
	var out []ConsentItem
	for _, n := range flow.Nodes {
		// Any node carrying inline code is a Custom Python node (kind may not be
		// normalized yet on a raw draft); classify by the code itself.
		if strings.TrimSpace(n.Code) == "" {
			continue
		}
		cls := codeclass.Classify(n.Code)
		if !cls.Beyond() {
			continue
		}
		out = append(out, ConsentItem{
			Kind:         "code",
			Name:         n.ID,
			Reason:       codeConsentReason(cls),
			Capabilities: cls.Requires,
			Dynamic:      cls.Dynamic,
			Hash:         hashCode(n.Code),
		})
	}
	return out
}

func codeConsentReason(c codeclass.Result) string {
	var parts []string
	for _, cap := range c.Requires {
		switch cap {
		case codeclass.CapSystem:
			parts = append(parts, "runs commands / writes files on the host")
		case codeclass.CapNetwork:
			parts = append(parts, "makes network requests")
		}
	}
	if c.Dynamic {
		parts = append(parts, "uses dynamic execution the scanner cannot inspect")
	}
	if len(parts) == 0 {
		return "runs code beyond the default guardrails"
	}
	return "this step " + strings.Join(parts, "; ")
}

// hashCode returns the first 12 hex chars of sha256(code) — the binding a code
// consent grant is tied to. Editing the code changes the hash, voiding consent.
func hashCode(code string) string {
	sum := sha256.Sum256([]byte(code))
	return hex.EncodeToString(sum[:])[:12]
}

// tierLabel maps a tier.Tier onto the wire vocabulary the frontend contract
// pins: readonly | active | privileged (note: tier.Tier.String() returns
// "read_only", which we collapse to "readonly" for the API).
func tierLabel(t tier.Tier) string {
	switch t {
	case tier.ReadOnly:
		return "readonly"
	case tier.Active:
		return "active"
	case tier.Privileged:
		return "privileged"
	default:
		return "unknown"
	}
}

// joinReasons renders the tier reasons into a single human-readable string
// for a ConsentItem. An empty reason set (shouldn't happen for Privileged,
// but stay defensive) yields a generic fallback.
func joinReasons(reasons []string) string {
	switch len(reasons) {
	case 0:
		return "workflow has privileged capabilities"
	case 1:
		return reasons[0]
	default:
		out := reasons[0]
		for _, r := range reasons[1:] {
			out += "; " + r
		}
		return out
	}
}
