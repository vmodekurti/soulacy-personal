package introspect

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/soulacy/soulacy/pkg/plugin"
)

// Pipeline runs the three E20 checks against a staged package directory.
// Zero value is usable: a nil Auditor degrades to "audit skipped" and a nil
// DryRunConfig skips the dry-run with an info finding.
type Pipeline struct {
	// Auditor performs the LLM audit. nil = no LLM available.
	Auditor Auditor
	// DryRun, when non-nil, enables the sandboxed startup-hook dry-run.
	DryRun *DryRunConfig
}

// Run executes static scan → LLM audit → dry-run and aggregates the
// SecurityReport. manifest may be nil (skills without plugin.yaml); the
// dry-run then has no hooks to execute. Run never fails — every degradation
// becomes a finding so the operator sees exactly what was and wasn't checked.
func (p *Pipeline) Run(ctx context.Context, dir string, manifest *plugin.Manifest) SecurityReport {
	findings := StaticScan(dir)

	// LLM audit with graceful degradation (never silently absent).
	if p.Auditor == nil {
		findings = append(findings, Finding{
			Check: "llm_audit", Severity: SeverityInfo,
			Message: "audit skipped: no LLM available",
		})
	} else if docs := collectDocs(dir); len(docs) == 0 {
		findings = append(findings, Finding{
			Check: "llm_audit", Severity: SeverityInfo,
			Message: "audit skipped: package has no documents (SKILL.md / plugin.yaml / README.md)",
		})
	} else if auditFindings, err := p.Auditor.Audit(ctx, docs); err != nil {
		findings = append(findings, Finding{
			Check: "llm_audit", Severity: SeverityInfo,
			Message: "audit skipped: " + err.Error(),
		})
	} else {
		findings = append(findings, auditFindings...)
	}

	// Sandboxed dry-run.
	if p.DryRun == nil {
		findings = append(findings, Finding{
			Check: "dry_run", Severity: SeverityInfo,
			Message: "dry-run skipped: not configured on this host",
		})
	} else {
		findings = append(findings, DryRun(ctx, dir, manifest, *p.DryRun)...)
	}

	return BuildReport(findings)
}

// collectDocs gathers the documents the LLM audit reads.
func collectDocs(dir string) map[string]string {
	docs := map[string]string{}
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		base := strings.ToLower(filepath.Base(path))
		if !docFiles[base] {
			return nil
		}
		body, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		rel, rerr := filepath.Rel(dir, path)
		if rerr != nil {
			rel = path
		}
		docs[rel] = string(body)
		return nil
	})
	return docs
}
