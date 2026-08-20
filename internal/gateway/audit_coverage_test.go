// audit_coverage_test.go — MU-031 criterion 2: "authentication, membership,
// token, secret, policy, extension, approval, schedule, export, deletion, and
// support-access actions are covered."
//
// A list of categories in a backlog is satisfied on the day somebody reads it
// and stops being satisfied silently. Six of the eleven were uncovered when
// this was written — authentication entirely, membership only in a separate
// store nobody reading the workspace trail would find, and extension,
// approval, schedule and most deletions not at all.
//
// So the categories live here, each mapped to the audit actions that cover it,
// and the guard reads the SOURCE for the action strings actually recorded. A
// category whose actions all disappear fails the build; an action string in
// this file that no longer exists in the source fails it too, so the map
// cannot become a description of what the system used to do.
package gateway

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// auditedCategories maps each class the criterion names to the audit actions
// that cover it. Every category must have at least one action, and every
// action must appear in the source.
var auditedCategories = map[string][]string{
	"authentication": {"auth.login", "auth.logout", "auth.reauthenticate"},
	"membership":     {"membership.role_changed", "membership.status_changed", "membership.removed", "invitation.created"},
	"token":          {"credential.create", "credential.revoke", "credential.rotate", "credential.status"},
	"secret":         {"secret.set", "secret.delete", "secret.key_rotate", "secret.key_reencrypt"},
	"policy":         {"config.patch"},
	"extension":      {"plugin.stage", "plugin.approve", "plugin.discard", "plugin.enable", "plugin.disable", "plugin.reapprove", "plugin.remove"},
	"approval":       {"approval.approve", "approval.deny"},
	"schedule":       {"schedule.enable", "schedule.disable"},
	"export":         {"support.bundle", "workspace.export_requested", "workspace.export_downloaded"},
	"deletion":       {"agent.delete", "knowledge.delete", "knowledge.document_delete", "memory.purge_session", "provider.delete", "share.revoke", "workspace.deletion_requested", "workspace.deletion_cancelled"},
	"support-access": {"support.bundle"},
}

// recordedActions harvests every audit action string the gateway records,
// from both the direct recorder and the route wrapper.
var (
	directRecorder = regexp.MustCompile(`recordAdminAudit\(\s*c\s*,\s*"([^"]+)"`)
	wrappedRoute   = regexp.MustCompile(`auditing\(\s*"([^"]+)"`)
	authEventLit   = regexp.MustCompile(`Action:\s*"(auth\.[^"]+)"`)
)

func recordedActions(t *testing.T) map[string]bool {
	t.Helper()
	found := map[string]bool{}
	// The gateway records most actions; internal/auth emits the
	// authentication ones through the sink the gateway installs, so both trees
	// have to be read or the authentication category reads as uncovered.
	for _, dir := range []string{".", filepath.Join("..", "auth")} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			source, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatal(err)
			}
			for _, re := range []*regexp.Regexp{directRecorder, wrappedRoute, authEventLit} {
				for _, match := range re.FindAllStringSubmatch(string(source), -1) {
					found[match[1]] = true
				}
			}
		}
	}
	return found
}

func TestEveryAuditedCategoryIsCovered(t *testing.T) {
	found := recordedActions(t)
	if len(found) < 15 {
		// Without this the whole guard passes vacuously the moment the regexes
		// stop matching — which is how a source-reading test becomes a no-op
		// that still reads as coverage.
		t.Fatalf("only %d audit actions found in the source; the harvest is broken: %v", len(found), sorted(found))
	}
	for category, actions := range auditedCategories {
		covered := false
		for _, action := range actions {
			if found[action] {
				covered = true
				break
			}
		}
		if !covered {
			t.Errorf("no action covers the %q category — MU-031 criterion 2 names it explicitly", category)
		}
	}
}

// The other direction: an action listed here that the source no longer records
// is a claim of coverage with nothing behind it, and the category it belongs to
// may now be covered by nothing at all.
func TestTheCoverageMapDoesNotDescribeThePast(t *testing.T) {
	found := recordedActions(t)
	for category, actions := range auditedCategories {
		for _, action := range actions {
			if !found[action] {
				t.Errorf("%s: %q is listed as covering it and is not recorded anywhere", category, action)
			}
		}
	}
}

// The criterion names eleven classes. Dropping one from this map would make
// the guard above pass by asking less, which is the failure mode of every
// checklist that checks itself.
func TestTheCategoryListMatchesTheCriterion(t *testing.T) {
	want := []string{
		"approval", "authentication", "deletion", "export", "extension",
		"membership", "policy", "schedule", "secret", "support-access", "token",
	}
	got := make([]string, 0, len(auditedCategories))
	for category := range auditedCategories {
		got = append(got, category)
	}
	sort.Strings(got)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("the category list has drifted from MU-031 criterion 2:\n got %v\nwant %v", got, want)
	}
}

// A failed action must be recorded as failed rather than not recorded.
// "Somebody tried to make themselves an owner and the store refused" is one of
// the more interesting lines a trail can carry, and a trail of successes
// cannot show an attack that did not work.
func TestAFailedActionIsStillRecorded(t *testing.T) {
	if auditOutcome(nil) != "ok" {
		t.Fatal("a successful action is not recorded as ok")
	}
	if auditOutcome(os.ErrPermission) != "failed" {
		t.Fatal("a refused action is not recorded as failed")
	}
}

func sorted(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}
