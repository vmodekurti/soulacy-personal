// Package introspect implements the pre-installation safety introspection
// pipeline (Story E20). Before a staged package (plugin or skill) is
// approved, three checks run against the staging directory:
//
//  1. Static scan — Python sources are searched for dangerous calls
//     (eval/exec/subprocess/os.system/socket/ctypes), suspicious imports,
//     and path-traversal strings; SKILL.md / plugin.yaml are checked for
//     obvious prompt-injection markers.
//  2. LLM audit — an auditor agent (via the llm router) reads the package
//     documents for prompt injection and behaviour/manifest mismatches.
//     Degrades gracefully: no provider → "audit skipped: no LLM available",
//     never a silent block.
//  3. Sandboxed dry-run — declared sidecar startup hooks execute briefly
//     under the rlimit __exec-sandbox wrapper with HTTP egress pointed at a
//     dead proxy; exit status, runtime, and file writes are recorded.
//
// The unified SecurityReport is attached to the E13 install Preview (GUI
// approval dialog) and the E18 CLI consent prompt.
package introspect

// Severity grades one finding.
type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

// Verdict values for SecurityReport.Verdict.
const (
	VerdictPass    = "pass"    // nothing above info
	VerdictCaution = "caution" // warnings present — review before approving
	VerdictDanger  = "danger"  // critical findings — approval strongly discouraged
)

func sevRank(s Severity) int {
	switch s {
	case SeverityCritical:
		return 2
	case SeverityWarning:
		return 1
	default:
		return 0
	}
}

// Finding is one observation from one check.
type Finding struct {
	// Check identifies the producing stage: "static", "llm_audit", "dry_run".
	Check    string   `json:"check"`
	Severity Severity `json:"severity"`
	// File is the package-relative path the finding refers to (optional).
	File string `json:"file,omitempty"`
	// Line is the 1-based line number within File (optional).
	Line int `json:"line,omitempty"`
	// Message is the human-readable description shown in consent dialogs.
	Message string `json:"message"`
}

// SecurityReport is the unified pipeline output.
type SecurityReport struct {
	Findings []Finding `json:"findings"`
	// Severity is the maximum severity across findings (info when empty).
	Severity Severity `json:"severity"`
	// Verdict maps Severity to an operator-facing recommendation:
	// pass / caution / danger.
	Verdict string `json:"verdict"`
}

// BuildReport aggregates findings into a SecurityReport. Findings is always
// non-nil so the JSON renders as [] rather than null.
func BuildReport(findings []Finding) SecurityReport {
	if findings == nil {
		findings = []Finding{}
	}
	max := SeverityInfo
	for _, f := range findings {
		if sevRank(f.Severity) > sevRank(max) {
			max = f.Severity
		}
	}
	verdict := VerdictPass
	switch max {
	case SeverityWarning:
		verdict = VerdictCaution
	case SeverityCritical:
		verdict = VerdictDanger
	}
	return SecurityReport{Findings: findings, Severity: max, Verdict: verdict}
}
