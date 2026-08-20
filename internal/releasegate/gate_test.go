// gate_test.go — the inventory must be true, and the gate must run it.
//
// Two failures, and the second is the one that produced this package. An
// inventory whose test files do not exist is fiction. An inventory the release
// gate does not RUN is documentation — which is what thirty packages of
// cross-tenant tests were, while `make security` executed three things.
package releasegate

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

func repoRoot() string { return filepath.Clean(filepath.Join("..", "..")) }

func TestTheInventoryIsInternallyConsistent(t *testing.T) {
	if err := Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestEverySurfaceNamesTestsThatExist(t *testing.T) {
	root := repoRoot()
	for _, surface := range Surfaces {
		if _, err := os.Stat(filepath.Join(root, surface.Package)); err != nil {
			t.Errorf("surface %q names package %s, which is not there: %v", surface.ID, surface.Package, err)
		}
		for _, test := range surface.Tests {
			if _, err := os.Stat(filepath.Join(root, test)); err != nil {
				t.Errorf("surface %q claims coverage in %s, which does not exist — an inventory of "+
					"tests that are not there is worse than no inventory, because it reports the "+
					"surface as covered", surface.ID, test)
			}
		}
	}
}

func TestEveryNamedTestFileActuallyAttemptsACrossTenantAccess(t *testing.T) {
	// Existence is not coverage. A file could be renamed, emptied, or replaced
	// by something unrelated and still satisfy the check above. This looks for
	// the shape of an adversarial tenancy test: two distinct workspaces, or a
	// victim, appearing in the source.
	//
	// Deliberately loose. It cannot verify that a test is CORRECT — that is
	// what the Why sentence and a reviewer are for — but it does catch the
	// specific decay this inventory is exposed to, which is a named file
	// drifting into something that no longer mentions a second tenant.
	twoTenant := regexp.MustCompile(`(?i)(ws[-_]?victim|ws[-_]?attacker|ws[-_]?b\b|workspace[-_]?b\b|another workspace|other tenant|cross[- ]tenant|neighbour)`)
	// A build guard's tell is that it reads the repository: it walks or parses
	// source and fails on something unclassified. It has no second tenant to
	// mention, and demanding one would mean writing a fake workspace into a
	// test that parses Go files.
	buildGuard := regexp.MustCompile(`(?i)(parser\.ParseFile|filepath\.WalkDir|os\.ReadDir|os\.ReadFile|MustCompile)`)
	// A fault-injection test's tell is that it abandons the sequence and then
	// asks what recovered. It has no second tenant either — its adversary is a
	// crash.
	faultInjection := regexp.MustCompile(`(?i)(crash|abandon|recover|lease.?lost|orphan)`)

	root := repoRoot()
	for _, surface := range Surfaces {
		want := twoTenant
		shape := "a second workspace or an adversary"
		switch surface.Kind {
		case KindBuildGuard:
			want, shape = buildGuard, "any reading of the repository's source"
		case KindFaultInjection:
			want, shape = faultInjection, "a crash, an abandonment, or a recovery"
		}
		covered := false
		for _, test := range surface.Tests {
			source, err := os.ReadFile(filepath.Join(root, test))
			if err != nil {
				continue // reported by the existence test
			}
			if want.Match(source) {
				covered = true
			}
		}
		if !covered {
			t.Errorf("surface %q is declared %s and names %d test file(s), none of which mentions %s — "+
				"the files exist but no longer look like what the surface claims",
				surface.ID, surface.Kind, len(surface.Tests), shape)
		}
	}
}

// securityTarget extracts the commands the `security` Makefile target runs.
var securityTarget = regexp.MustCompile(`(?ms)^security:\n((?:\t.*\n)+)`)

func TestTheReleaseGateRunsEverySurfaceItClaims(t *testing.T) {
	// THE POINT OF THE WHOLE PACKAGE. Coverage nobody runs at the moment of
	// release is documentation: every one of these suites could rot, or start
	// passing for the wrong reason, and a gate that does not execute them stays
	// green throughout.
	//
	// The Makefile is read rather than generated because it is edited by hand
	// and protected in this repository; the check tells whoever edits it what
	// they have left out, which is the useful direction.
	makefile, err := os.ReadFile(filepath.Join(repoRoot(), "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	match := securityTarget.FindSubmatch(makefile)
	if match == nil {
		t.Fatal("the Makefile has no `security:` target; this guard now vouches for nothing")
	}
	commands := string(match[1])

	var missing []string
	for _, pkg := range Packages() {
		// Matched as ./<pkg>/ so internal/runs does not satisfy a claim on
		// internal/runtime by prefix.
		if !strings.Contains(commands, "./"+pkg+"/") {
			missing = append(missing, pkg)
		}
	}
	sort.Strings(missing)
	for _, pkg := range missing {
		t.Errorf("the release gate does not run %s, but the inventory claims a surface there. Add it "+
			"to the Makefile's `security` target — an inventory the gate does not execute is a list of "+
			"tests that are allowed to rot", pkg)
	}
}

func TestGapsAreReportedRatherThanHidden(t *testing.T) {
	// Blockers is the honest answer to "is this ready". The test asserts the
	// mechanism works rather than asserting a particular number of gaps, since
	// the number is meant to change: pinning it would make closing a gap a
	// test failure, which is how a project learns to stop recording them.
	blockers := Blockers()
	for _, blocker := range blockers {
		if !strings.Contains(blocker, ":") {
			t.Errorf("blocker %q does not name its surface", blocker)
		}
	}
	// Every gap must belong to a declared surface, or Blockers is reporting
	// something nobody can act on.
	ids := map[string]bool{}
	for _, surface := range Surfaces {
		ids[surface.ID] = true
	}
	for _, blocker := range blockers {
		id := strings.SplitN(blocker, ":", 2)[0]
		if !ids[id] {
			t.Errorf("blocker names unknown surface %q", id)
		}
	}
}

func TestEveryCriterionPhraseComesFromTheStory(t *testing.T) {
	// The Criterion field is a quote, and a quote nobody checks is a
	// paraphrase. This asserts each one appears in the backlog's MU-037
	// section, so the mapping from surface to acceptance criterion can be
	// verified rather than trusted.
	backlog, err := os.ReadFile(filepath.Join(repoRoot(), "docs", "MULTI_USER_BACKLOG.md"))
	if err != nil {
		t.Skipf("backlog unavailable: %v", err)
	}
	// The backlog wraps lines, so compare against a whitespace-collapsed copy.
	// Case-insensitive: the backlog capitalises the first word of each bullet,
	// and requiring the quote to match that is a formatting test wearing the
	// costume of a correctness one.
	flat := strings.ToLower(strings.Join(strings.Fields(string(backlog)), " "))
	for _, surface := range Surfaces {
		// The stored phrase uses "..." for elisions; check each fragment.
		for _, fragment := range strings.Split(surface.Criterion, "...") {
			fragment = strings.ToLower(strings.Join(strings.Fields(fragment), " "))
			if fragment == "" {
				continue
			}
			if !strings.Contains(flat, fragment) {
				t.Errorf("surface %q quotes %q, which is not in the backlog — the mapping from "+
					"surface to acceptance criterion cannot be checked", surface.ID, fragment)
			}
		}
	}
}
