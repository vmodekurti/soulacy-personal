// exportplan_test.go — MU-032 criteria 1 and 5, made structural.
//
// "Export enumerates all owned resource classes" is the kind of requirement
// that is satisfied on the day it is written and quietly stops being satisfied
// the first time somebody adds a store. The failure is silent in both
// directions: a resource missing from the export is data a customer is told
// they have and does not, and a resource missing from the deletion plan is
// data that survives a deletion whose report says it completed.
package ownership

import (
	"strings"
	"testing"
)

// THE GUARD. A resource added to the catalog without an export or deletion
// decision, or with a deletion sentence nobody can classify, fails the build —
// so the decision is made at the moment the store is introduced, by the person
// who knows what it holds.
func TestEveryWorkspaceResourceHasAnExportAndADeletionDecision(t *testing.T) {
	if err := ValidateExportPlan(); err != nil {
		t.Fatalf("%v\n\nAdd the missing Export/Deletion sentence to internal/ownership/catalog.go. "+
			"A deletion sentence has to say what HAPPENS (purge, tombstone, retain), not only what is affected.", err)
	}
}

// Without this the guard above passes vacuously the moment the projection
// stops finding anything — which is how a catalog-derived check becomes a
// no-op that still reads as coverage.
func TestTheExportPlanCoversTheCatalog(t *testing.T) {
	plan := WorkspaceExportPlan()
	if len(plan) < 20 {
		t.Fatalf("the export plan has %d entries, which is fewer than the catalog's workspace resources", len(plan))
	}
	planned := map[string]bool{}
	for _, entry := range plan {
		planned[entry.Resource] = true
	}
	for _, resource := range Resources {
		if resource.Class != WorkspaceOwned && resource.Class != UserPrivate {
			continue
		}
		if !planned[resource.Name] {
			t.Errorf("%s is workspace data and is not in the export plan", resource.Name)
		}
	}
	// And nothing that outlives the workspace is in it. An export that swept
	// up organization-owned plugins or platform tenancy rows would be handing
	// one customer another's configuration.
	for _, entry := range plan {
		if entry.Class != WorkspaceOwned && entry.Class != UserPrivate {
			t.Errorf("%s is %s and must not be in a WORKSPACE export", entry.Resource, entry.Class)
		}
	}
}

// User-private data is in the workspace export deliberately. An export that
// omitted conversations and memory would be an export of the workspace's
// configuration, and a customer asking to move their data means the data.
func TestUserPrivateDataIsPartOfAWorkspaceExport(t *testing.T) {
	planned := map[string]bool{}
	for _, entry := range WorkspaceExportPlan() {
		planned[entry.Resource] = true
	}
	for _, name := range []string{"sessions", "messages", "memory", "studio-drafts"} {
		if !planned[name] {
			t.Errorf("%s is missing from the workspace export", name)
		}
	}
}

// MU-032 criterion 6: "completion produces a deletion report without secret
// values." The classes whose catalog sentence promises that must actually be
// marked, or the promise is prose nobody enforces.
func TestClassesThatPromiseNoSecretsAreMarked(t *testing.T) {
	marked := map[string]bool{}
	for _, entry := range WorkspaceExportPlan() {
		marked[entry.Resource] = entry.SecretsExcluded
	}
	for _, name := range []string{"secrets", "credentials", "api-keys", "channels", "webhooks", "mcp", "shares"} {
		if !marked[name] {
			t.Errorf("%s exports secret material without the catalog saying so", name)
		}
	}
}

// The disposition is the catalog's own sentence classified, not a second
// policy. These pin the two that are not "purge", because they are the ones a
// deletion report has to explain rather than assert.
func TestTheNonPurgeDispositionsAreDeliberate(t *testing.T) {
	byName := map[string]PlanEntry{}
	for _, entry := range WorkspaceExportPlan() {
		byName[entry.Resource] = entry
	}
	costs := byName["costs"]
	if costs.Disposition != Retained {
		t.Fatalf("costs disposition = %q, want retained — aggregates survive because a provider invoice was reconciled against them", costs.Disposition)
	}
	if !strings.Contains(strings.ToLower(costs.Deletion), "may remain") {
		t.Fatalf("the costs deletion sentence no longer explains why anything remains: %q", costs.Deletion)
	}
}

// A deletion sentence that lists what is affected without saying what happens
// to it is not a policy. `knowledge` said exactly that — "document, chunks,
// embeddings, jobs" — and this guard is what surfaced it.
func TestASentenceWithoutAVerbIsNotAPolicy(t *testing.T) {
	if got := dispositionOf(Resource{Deletion: "document, chunks, embeddings, jobs"}); got != "" {
		t.Fatalf("a sentence with no verb classified as %q", got)
	}
	if got := dispositionOf(Resource{Deletion: ""}); got != "" {
		t.Fatalf("an empty deletion policy classified as %q", got)
	}
	if got := dispositionOf(Resource{Deletion: "purge document, chunks, embeddings, and ingest jobs"}); got != Purged {
		t.Fatalf("a purge sentence classified as %q", got)
	}
}
