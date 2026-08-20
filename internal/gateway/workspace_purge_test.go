// workspace_purge_test.go — the deletion coverage gap is a written list, not a
// silence.
//
// MU-032 criterion 4 covers twenty-seven resource classes and this build purges
// a few of them. That is a normal state for work in progress; what is NOT
// acceptable is the gap being discoverable only by reading a deletion report
// from a customer's workspace. Every uncovered class is named here with the
// reason it is uncovered, so adding a resource to the ownership catalog
// without either a purger or an entry below fails the build.
package gateway

import (
	"sort"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/internal/runs"
	"github.com/soulacy/soulacy/internal/workboard"
	"github.com/soulacy/soulacy/internal/workspacepolicy"
	"github.com/soulacy/soulacy/internal/workspacepurge"
)

// notYetPurged is the deletion gap, one line per class, each with what is
// missing. An entry is a claim a reviewer can check; the absence of one is the
// build failing.
var notYetPurged = map[string]string{
	"agents":          "the agent file tree plus rbac_agent_grants; needs the loader's per-workspace dirs and the RBAC database handle together, since a tree removal alone would leave the grants",
	"definitions":     "version snapshots live beside each agent, so this is settled by the agents purger once that exists",
	"approvals":       "internal/approvals has PurgeWorkspace; the gateway does not hold the store",
	"schedules":       "internal/schedules has PurgeWorkspace; the gateway reaches the scheduler, not its store",
	"queue-dlq":       "internal/queue/dlq SQLiteStore has PurgeWorkspace; s.dlqStore is the interface, which does not declare it",
	"knowledge":       "needs the knowledge store's database handle, which the engine owns",
	"events":          "the action log is a file archive plus agent_events rows; both halves need doing together",
	"memory":          "file-backed and SQLite variants, with different roots",
	"vectors":         "follows the source resource; purged with memory and knowledge",
	"messages":        "conversation history database handle is held by the session store",
	"sessions":        "session ownership and resources span three tables in a store the gateway holds as an interface",
	"studio-drafts":   "per-workspace, per-user directory under <root>/studio/drafts",
	"studio-traces":   "trace directory, which is env-configurable",
	"studio-learning": "four tables plus the lesson files",
	"skills":          "per-workspace skill inventory under its own base",
	"mcp":             "redacted configuration lives in config.yaml, not in a workspace store; deleting a workspace's MCP entries means editing config, which needs a decision about who may",
	"channels":        "same as mcp: configuration, not a store",
	"webhooks":        "same as mcp: configuration, not a store",
	"shares":          "share records are file-backed under their own root",
	"artifacts":       "workboard_artifacts is purged with workboard; object storage is not",
	"secrets":         "cryptographic erasure, not a DELETE — the catalog says so, and doing it as a row delete would leave the ciphertext recoverable from a backup",
	"credentials":     "revoke-then-purge across three tables, and revocation has to happen first so a credential cannot be used between the two",
	"api-keys":        "same shape as credentials",
	"workspace-files": "there is no single workspace file tree; each store applies wsroot to its own base, so this needs one purger per class rather than one for all",
}

func TestTheWorkspacePurgeCoverageGapIsAKnownList(t *testing.T) {
	// A server with the stores PRESENT, because this guard is about what the
	// build can purge, not about what one instance happens to have wired. A
	// zero-valued store is enough: the coverage question is which resources
	// are registered, and no purger runs here.
	server := &Server{
		runStore:          &runs.Store{},
		workboardStore:    &workboard.Store{},
		workspacePolicies: &workspacepolicy.Store{},
	}
	purgers := server.workspacePurgers()
	if err := workspacepurge.ValidatePurgers(purgers); err != nil {
		t.Fatalf("the registered purgers disagree with the ownership catalog: %v", err)
	}

	uncovered := workspacepurge.Coverage(purgers)
	var undeclared []string
	for _, resource := range uncovered {
		reason, declared := notYetPurged[resource]
		if !declared {
			undeclared = append(undeclared, resource)
			continue
		}
		if strings.TrimSpace(reason) == "" {
			t.Errorf("%q is declared uncovered with no reason", resource)
		}
	}
	sort.Strings(undeclared)
	for _, resource := range undeclared {
		t.Errorf("resource %q survives a workspace deletion and is not in notYetPurged — "+
			"register a purger in workspacePurgers, or add it here with what is missing", resource)
	}

	// The other direction. An entry naming a class that IS now purged is a
	// stale claim, and leaving it makes the list read as a bigger gap than
	// exists — which is how a list stops being maintained.
	covered := map[string]bool{}
	for _, resource := range uncovered {
		covered[resource] = true
	}
	for resource := range notYetPurged {
		if !covered[resource] {
			t.Errorf("%q is listed as not-yet-purged but is now covered — remove the entry", resource)
		}
	}
}

// A server with nothing wired must still produce a valid purger set, because
// that is the state a directly constructed or partially wired gateway is in,
// and a purge that panics is worse than one that reports no coverage.
func TestPurgersFromAnUnwiredServerAreStillValid(t *testing.T) {
	purgers := (&Server{}).workspacePurgers()
	if err := workspacepurge.ValidatePurgers(purgers); err != nil {
		t.Fatalf("an unwired server produced invalid purgers: %v", err)
	}
	report, err := workspacepurge.Run(workspacepurge.Options{
		WorkspaceID: "ws_a", RequestedBy: "usr_alice", Purgers: purgers,
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Complete {
		t.Fatal("an unwired server reported a complete deletion")
	}
}
