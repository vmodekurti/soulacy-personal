// skillinstall.go — Story E18: remote skill & package installation.
//
// `sy skill install <arg>` keeps its local-directory behaviour; when arg is
// NOT a local directory it is treated as a package slug and resolved through
// the configured registry providers (E19): resolve latest version+checksum →
// fetch into a staging dir (sha256-verified for archives) → E20 safety
// introspection → permissions consent prompt → move into ~/.soulacy/skills/
// → hot-load via the gateway's POST /skills/rescan.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/introspect"
	"github.com/soulacy/soulacy/internal/pkgregistry"
	"github.com/soulacy/soulacy/internal/plugininstall"
	"github.com/soulacy/soulacy/pkg/plugin"
	"go.uber.org/zap"
)

// remoteInstallOpts parameterises remoteSkillInstall for testability.
type remoteInstallOpts struct {
	// SkillsDir is the install root (normally ~/.soulacy/skills).
	SkillsDir string
	// AssumeYes skips the consent prompt — EXCEPT when the safety verdict
	// is "danger", which always requires an interactive yes.
	AssumeYes bool
	// AllowUnverified opts in to installing packages whose authenticity
	// cannot be cryptographically verified: registries with no signing_key,
	// unsigned packages from a signing registry, or raw git sources. Without
	// it (the default) such installs are BLOCKED with SEC-7's friction error.
	// It never relaxes the safety introspection or danger-verdict gates.
	AllowUnverified bool
	// Confirm asks the operator a yes/no question. nil = always deny
	// (non-interactive without --yes).
	Confirm func(prompt string) bool
	// Rescan triggers the gateway hot-load after install. nil = skip.
	Rescan func() error
	// Out receives progress output.
	Out io.Writer
	// Pipeline runs the E20 checks. nil = a default pipeline (static scan +
	// bounded dry-run; LLM audit reports "skipped" — the CLI has no router).
	Pipeline *introspect.Pipeline
	// Log records the loud WARN emitted when an unverified install proceeds
	// under AllowUnverified. nil = a no-op logger.
	Log *zap.Logger
}

// registriesFromConfig builds the E19 engine from the `registries:` config
// block. With no registries configured, the native defaults apply: the
// public skills.sh directory + a bare git provider — both
// `sy skill install anthropics/skills/skill-creator` and
// `sy skill install github.com/user/skill` work with zero configuration.
func registriesFromConfig(entries []config.RegistryConfig, log *zap.Logger) *pkgregistry.Engine {
	if len(entries) == 0 {
		entries = pkgregistry.DefaultRegistries()
	}
	eng, errs := pkgregistry.FromConfig(entries, log)
	for _, e := range errs {
		fmt.Fprintf(os.Stderr, "warning: %v\n", e)
	}
	return eng
}

// remoteSkillInstall is the E18 flow. Returns an error for every abort path
// so the CLI exits non-zero; the staging dir never survives a failure.
func remoteSkillInstall(ctx context.Context, eng *pkgregistry.Engine, slug string, opts remoteInstallOpts) error {
	out := opts.Out
	if out == nil {
		out = os.Stdout
	}

	// 1. Resolve through the registries (priority order, fallback).
	pkg, err := eng.Resolve(ctx, slug)
	if err != nil {
		return fmt.Errorf("%q is not a local directory and no configured registry resolves it: %w", slug, err)
	}
	fmt.Fprintf(out, "Resolved %s@%s via registry %q\n", pkg.Slug, pkg.Version, pkg.Provider)
	if pkg.Description != "" {
		fmt.Fprintf(out, "  %s\n", firstLine(pkg.Description))
	}
	if pkg.Checksum != "" {
		fmt.Fprintf(out, "  archive sha256: %s\n", pkg.Checksum)
	}
	// Signature provenance (verification itself is enforced inside Fetch —
	// a registry with signing_key REFUSES unsigned/tampered packages).
	switch {
	case pkg.Signature != "" && eng.VerifiesSignatures(pkg.Provider):
		fmt.Fprintf(out, "  signature: ed25519, verified against registry %q's signing_key during fetch\n", pkg.Provider)
	case pkg.Signature != "":
		fmt.Fprintf(out, "  signature: present but UNVERIFIED — set signing_key on registry %q to enforce verification\n", pkg.Provider)
	default:
		fmt.Fprintf(out, "  signature: none (unsigned package)\n")
	}

	// SEC-7: install friction for cryptographically UNVERIFIABLE packages.
	// A package is "verified" only when it carries a signature AND the
	// resolving registry enforces signatures with a configured signing_key.
	// Everything else — an unsigned package, a registry with no signing_key,
	// or a raw git URL — cannot be authenticated, so we refuse to install it
	// unless the operator explicitly opts in with --allow-unverified.
	verified := pkg.Signature != "" && eng.VerifiesSignatures(pkg.Provider)
	if !verified {
		if !opts.AllowUnverified {
			return fmt.Errorf(
				"refusing to install %q: its authenticity cannot be verified "+
					"(registry %q has no signing_key, or the package is unsigned / a raw git source). "+
					"An attacker who controls the source or the network could ship malicious code. "+
					"Re-run with --allow-unverified to install anyway, or configure a signing_key on the registry to enforce verification",
				pkg.Slug, pkg.Provider)
		}
		log := opts.Log
		if log == nil {
			log = zap.NewNop()
		}
		log.Warn("SEC-7: installing UNVERIFIED skill package — authenticity could not be cryptographically verified; proceeding only because --allow-unverified was set",
			zap.String("slug", pkg.Slug),
			zap.String("version", pkg.Version),
			zap.String("provider", pkg.Provider),
			zap.Bool("signed", pkg.Signature != ""),
		)
		fmt.Fprintf(out, "⚠ WARNING: installing UNVERIFIED package %s@%s — authenticity could not be verified (--allow-unverified).\n", pkg.Slug, pkg.Version)
	}

	// 2. Fetch into a staging dir — checksum verification happens inside the
	// provider for archives; git sources derive integrity from the clone.
	if err := os.MkdirAll(opts.SkillsDir, 0o755); err != nil {
		return err
	}
	staging := filepath.Join(opts.SkillsDir, fmt.Sprintf(".staging-%d", time.Now().UnixNano()))
	if err := eng.Fetch(ctx, pkg, staging); err != nil {
		_ = os.RemoveAll(staging)
		return fmt.Errorf("fetch failed: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(staging) }

	// A skill package must carry SKILL.md at its root.
	if _, err := os.Stat(filepath.Join(staging, "SKILL.md")); err != nil {
		cleanup()
		return fmt.Errorf("package %q has no SKILL.md at its root — not a skill package", pkg.Slug)
	}

	// 3. Safety introspection (E20).
	pipeline := opts.Pipeline
	if pipeline == nil {
		pipeline = &introspect.Pipeline{DryRun: &introspect.DryRunConfig{Timeout: 5 * time.Second}}
	}
	var manifest *plugin.Manifest
	if m, merr := plugininstall.ReadManifest(staging); merr == nil {
		manifest = &m
	}
	report := pipeline.Run(ctx, staging, manifest)
	printSecurityReport(out, report)

	// Package summary for the consent decision.
	if heading := skillHeading(staging); heading != "" {
		fmt.Fprintf(out, "Skill: %s\n", heading)
	}
	if manifest != nil {
		if len(manifest.Tools) > 0 {
			fmt.Fprintf(out, "Declares %d tool librar%s.\n", len(manifest.Tools), pluralYIes(len(manifest.Tools)))
		}
		if len(manifest.Migrations) > 0 {
			fmt.Fprintln(out, "Declared schema migrations:")
			for _, mg := range manifest.Migrations {
				fmt.Fprintf(out, "  - %s\n", mg.Name)
			}
		}
		if len(manifest.Permissions) > 0 {
			fmt.Fprintln(out, "Requested capabilities:")
			for _, p := range manifest.Permissions {
				fmt.Fprintf(out, "  - %s\n", p.Cap)
			}
		}
		if len(manifest.Credentials) > 0 {
			fmt.Fprintln(out, "Requested credentials:")
			for _, cr := range manifest.Credentials {
				fmt.Fprintf(out, "  - %s ← vault: %s\n", cr.Key, cr.From)
			}
		}
	}

	// 4. Consent. --yes never bypasses a danger verdict.
	prompt := fmt.Sprintf("Install %s@%s? [y/N] ", pkg.Slug, pkg.Version)
	switch {
	case report.Verdict == introspect.VerdictDanger:
		fmt.Fprintln(out, "⚠ CRITICAL findings — explicit confirmation required (--yes does not apply).")
		if opts.Confirm == nil || !opts.Confirm(prompt) {
			cleanup()
			return fmt.Errorf("install aborted (danger verdict not confirmed)")
		}
	case opts.AssumeYes:
		// consent given on the command line
	case opts.Confirm == nil || !opts.Confirm(prompt):
		cleanup()
		return fmt.Errorf("install aborted by user")
	}

	// 5. Activate: move staging → ~/.soulacy/skills/<name>.
	name := skillDirName(pkg.Slug)
	dest := filepath.Join(opts.SkillsDir, name)
	if _, err := os.Stat(dest); err == nil {
		cleanup()
		return fmt.Errorf("skill %q is already installed at %s — remove it first", name, dest)
	}
	if err := os.Rename(staging, dest); err != nil {
		// cross-device fallback
		if cerr := copyDir(staging, dest); cerr != nil {
			cleanup()
			return fmt.Errorf("install failed: %v (copy fallback: %v)", err, cerr)
		}
		cleanup()
	}
	fmt.Fprintf(out, "✓ Installed %s@%s → %s\n", pkg.Slug, pkg.Version, dest)

	// 6. Hot-load through the gateway; degrade to a restart hint.
	if opts.Rescan != nil {
		if err := opts.Rescan(); err != nil {
			fmt.Fprintf(out, "Gateway rescan failed (%v) — the skill loads on the next gateway restart.\n", err)
		} else {
			fmt.Fprintln(out, "✓ Gateway rescanned — skill is live.")
		}
	}
	return nil
}

func printSecurityReport(out io.Writer, report introspect.SecurityReport) {
	badge := map[string]string{
		introspect.VerdictPass:    "✓ passed safety checks",
		introspect.VerdictCaution: "⚠ caution — review findings",
		introspect.VerdictDanger:  "✗ DANGER — critical findings",
	}[report.Verdict]
	fmt.Fprintf(out, "Safety introspection: %s\n", badge)
	for _, f := range report.Findings {
		loc := ""
		if f.File != "" {
			loc = " [" + f.File
			if f.Line > 0 {
				loc += fmt.Sprintf(":%d", f.Line)
			}
			loc += "]"
		}
		fmt.Fprintf(out, "  %s (%s)%s: %s\n", strings.ToUpper(string(f.Severity)), f.Check, loc, f.Message)
	}
}

// skillDirName derives the install directory name from a slug:
// "github.com/user/my-skill" → "my-skill"; trailing ".git" stripped.
func skillDirName(slug string) string {
	name := path.Base(strings.TrimSuffix(strings.TrimSuffix(slug, "/"), ".git"))
	name = strings.TrimSuffix(name, ".git")
	if name == "" || name == "." || name == "/" {
		return "skill"
	}
	return name
}

// firstLine trims a description to its first non-empty line.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if l := strings.TrimSpace(line); l != "" {
			return l
		}
	}
	return ""
}

// skillHeading returns the first markdown heading of the staged SKILL.md.
func skillHeading(dir string) string {
	body, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(body), "\n") {
		l := strings.TrimSpace(line)
		if strings.HasPrefix(l, "#") {
			return strings.TrimSpace(strings.TrimLeft(l, "#"))
		}
	}
	return ""
}

func pluralYIes(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}
