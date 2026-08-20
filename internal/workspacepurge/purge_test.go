// purge_test.go — the assertions are about what the report ADMITS, not about
// what the purge removed.
//
// Removal is the easy half and the half a reviewer naturally tests. The half
// that decides whether this system can honestly tell a customer their data is
// gone is whether an incomplete run says so — loudly, by name, in a field that
// cannot be set to `true` by hand.
package workspacepurge

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"

	"github.com/soulacy/soulacy/internal/ownership"
	"github.com/soulacy/soulacy/internal/wsroot"
)

func purgerFor(resource string, removed Removed, err error) Purger {
	return Purger{Resource: resource, Purge: func(context.Context, string) (Removed, error) {
		return removed, err
	}}
}

func entryFor(t *testing.T, report Report, resource string) Entry {
	t.Helper()
	for _, entry := range report.Entries {
		if entry.Resource == resource {
			return entry
		}
	}
	t.Fatalf("resource %q is absent from the report", resource)
	return Entry{}
}

// THE PROPERTY. A class with no purger is named in the report as surviving,
// rather than being absent — because absent reads exactly like "there was
// nothing to delete".
func TestAClassWithNoPurgerIsReportedAsSurviving(t *testing.T) {
	report, err := Run(Options{
		WorkspaceID: "ws_a", RequestedBy: "usr_alice",
		Purgers: []Purger{purgerFor("agents", Removed{Rows: 3}, nil)},
	})
	if err != nil {
		t.Fatal(err)
	}

	plan := ownership.WorkspaceDeletionPlan()
	if len(report.Entries) != len(plan) {
		t.Fatalf("report carries %d entries for a %d-class plan", len(report.Entries), len(plan))
	}
	if report.Complete {
		t.Fatal("a purge covering one class of many reported itself complete")
	}
	if len(report.Survivors) == 0 {
		t.Fatal("nothing was named as surviving a purge that covered one class")
	}
	for _, resource := range report.Survivors {
		if entryFor(t, report, resource).Outcome.Settled() {
			t.Errorf("%q is listed as a survivor but its outcome is settled", resource)
		}
	}
	agents := entryFor(t, report, "agents")
	if agents.Outcome != OutcomePurged || agents.Rows != 3 {
		t.Fatalf("agents entry = %+v", agents)
	}
	// Every unsettled entry must say WHY. "not-purged" with no reason is a
	// report that names a problem and withholds the one fact needed to act.
	for _, entry := range report.Entries {
		if !entry.Outcome.Settled() && strings.TrimSpace(entry.Detail) == "" {
			t.Errorf("%q survived with no explanation", entry.Resource)
		}
	}
}

// Complete is computed from the entries, never assigned. Asserted by driving a
// run that covers everything the catalog demands and checking it flips.
func TestCompleteIsTrueOnlyWhenEveryClassIsSettled(t *testing.T) {
	var purgers []Purger
	for _, planned := range ownership.WorkspaceDeletionPlan() {
		if planned.Disposition == ownership.Retained {
			continue // a decision, not a gap — no purger is expected
		}
		purgers = append(purgers, purgerFor(planned.Resource, Removed{Rows: 1}, nil))
	}
	report, err := Run(Options{WorkspaceID: "ws_a", Purgers: purgers})
	if err != nil {
		t.Fatal(err)
	}
	if !report.Complete {
		t.Fatalf("a fully covered purge reported incomplete; survivors: %v", report.Survivors)
	}
	if len(report.Survivors) != 0 {
		t.Fatalf("survivors = %v, want none", report.Survivors)
	}
	// Survivors is present-and-empty rather than nil, so a reader can tell
	// "nothing survived" from a field an older build did not emit.
	if report.Survivors == nil {
		t.Fatal("Survivors is nil; an absent field and an empty one mean different things here")
	}
}

// A class the catalog says is RETAINED is settled without a purger. That is a
// documented decision — audit records under a legal hold, aggregates a
// provider invoice was reconciled against — not a gap, and reporting it as one
// would train readers to ignore the survivor list.
func TestARetainedClassIsSettledWithoutAPurger(t *testing.T) {
	report, err := Run(Options{WorkspaceID: "ws_a"})
	if err != nil {
		t.Fatal(err)
	}
	retained := 0
	for _, planned := range ownership.WorkspaceDeletionPlan() {
		if planned.Disposition != ownership.Retained {
			continue
		}
		retained++
		entry := entryFor(t, report, planned.Resource)
		if entry.Outcome != OutcomeRetained {
			t.Errorf("%q is retained by policy but reported %q", planned.Resource, entry.Outcome)
		}
		if entry.Policy != planned.Deletion {
			t.Errorf("%q does not carry the catalog's own policy sentence", planned.Resource)
		}
	}
	if retained == 0 {
		t.Skip("no retained classes in the catalog; this test would prove nothing")
	}
}

// One class failing must not abandon the run. A purge that stops at the first
// error leaves the caller with no idea which of the remaining classes were
// reached, and the natural retry re-runs the ones that already succeeded.
func TestOneFailingClassDoesNotAbandonTheRest(t *testing.T) {
	report, err := Run(Options{
		WorkspaceID: "ws_a",
		Purgers: []Purger{
			purgerFor("agents", Removed{}, errors.New("the agent tree is unreadable")),
			purgerFor("runs", Removed{Rows: 7}, nil),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	failed := entryFor(t, report, "agents")
	if failed.Outcome != OutcomeFailed {
		t.Fatalf("agents outcome = %q, want failed", failed.Outcome)
	}
	if !strings.Contains(failed.Detail, "unreadable") {
		t.Fatalf("the failure gives no usable reason: %q", failed.Detail)
	}
	if got := entryFor(t, report, "runs"); got.Outcome != OutcomePurged || got.Rows != 7 {
		t.Fatalf("a class after the failure was not attempted: %+v", got)
	}
	if report.Complete {
		t.Fatal("a purge with a failed class reported complete")
	}
	// failed and not-purged are distinct: a permanent gap must not look like a
	// transient one, or a known gap gets retried forever instead of fixed.
	if failed.Outcome == OutcomeNotPurged {
		t.Fatal("a failure was collapsed into not-purged")
	}
}

// A purger naming a class the catalog does not carry is a typo whose only
// symptom is the class it meant to cover staying unpurged.
func TestAPurgerThatDisagreesWithTheCatalogIsRefused(t *testing.T) {
	if err := ValidatePurgers([]Purger{purgerFor("agents", Removed{}, nil)}); err != nil {
		t.Fatalf("a catalogued resource was refused: %v", err)
	}
	err := ValidatePurgers([]Purger{purgerFor("agnets", Removed{}, nil)})
	if err == nil || !strings.Contains(err.Error(), "agnets") {
		t.Fatalf("a misspelled resource was accepted: %v", err)
	}
	err = ValidatePurgers([]Purger{{Resource: "agents"}})
	if err == nil || !strings.Contains(err.Error(), "no purge function") {
		t.Fatalf("a purger with no function was accepted: %v", err)
	}
	// A purger for a class the catalog RETAINS is a contradiction worth
	// stopping at the boundary: either the code deletes data the policy keeps,
	// or the policy is stale.
	for _, planned := range ownership.WorkspaceDeletionPlan() {
		if planned.Disposition != ownership.Retained {
			continue
		}
		if err := ValidatePurgers([]Purger{purgerFor(planned.Resource, Removed{}, nil)}); err == nil {
			t.Fatalf("a purger was accepted for %q, which the catalog retains", planned.Resource)
		}
		return
	}
}

// A cancelled run stops, and every class it did not reach says so rather than
// being reported as needing no work.
func TestACancelledPurgeReportsWhatItDidNotReach(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	report, err := Run(Options{
		WorkspaceID: "ws_a", Ctx: ctx,
		Purgers: []Purger{purgerFor("agents", Removed{Rows: 1}, nil)},
	})
	if err == nil {
		t.Fatal("a cancelled purge reported success")
	}
	if report.Complete {
		t.Fatal("a cancelled purge reported complete")
	}
	if got := entryFor(t, report, "agents"); got.Outcome != OutcomeNotPurged {
		t.Fatalf("a class the cancelled run did not reach = %q", got.Outcome)
	}
}

// ---------------------------------------------------------------------------
// The catalog-driven SQL purger
// ---------------------------------------------------------------------------

func sqlDB(t *testing.T, statements ...string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, stmt := range statements {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	return db
}

// The tables come from the catalog, so a table added to a resource is purged
// the day it is classified — with no second edit anywhere.
func TestTheTablesPurgedComeFromTheOwnershipCatalog(t *testing.T) {
	tables := CatalogTables("knowledge")
	if len(tables) < 2 {
		t.Fatalf("knowledge resolves to %v; this test would prove little", tables)
	}
	var stmts []string
	for _, table := range tables {
		stmts = append(stmts,
			`CREATE TABLE "`+table+`" (workspace_id TEXT, id TEXT)`,
			`INSERT INTO "`+table+`" VALUES ('ws_a','1'),('ws_a','2'),('ws_b','3')`)
	}
	db := sqlDB(t, stmts...)

	removed, err := PurgeCatalogTables(context.Background(), db, "knowledge", "ws_a")
	if err != nil {
		t.Fatal(err)
	}
	if want := int64(2 * len(tables)); removed.Rows != want {
		t.Fatalf("removed %d rows, want %d", removed.Rows, want)
	}
	for _, table := range tables {
		var remaining int
		if err := db.QueryRow(`SELECT COUNT(*) FROM "` + table + `"`).Scan(&remaining); err != nil {
			t.Fatal(err)
		}
		if remaining != 1 {
			t.Fatalf("%s has %d rows left, want ws_b's 1 — another tenant's rows were deleted", table, remaining)
		}
	}
}

// The workspace column is read from the real schema, not from the catalog's
// prose. Some tables name it `workspace`; parsing "workspace_id (column:
// workspace)" would work until somebody rephrased it, and the failure would be
// a DELETE matching nothing and reporting success.
func TestTheWorkspaceColumnIsReadFromTheSchema(t *testing.T) {
	tables := CatalogTables("costs")
	if len(tables) == 0 {
		t.Skip("no costs tables classified")
	}
	var stmts []string
	for _, table := range tables {
		stmts = append(stmts,
			`CREATE TABLE "`+table+`" (workspace TEXT, id TEXT)`,
			`INSERT INTO "`+table+`" VALUES ('ws_a','1'),('ws_b','2')`)
	}
	db := sqlDB(t, stmts...)

	removed, err := PurgeCatalogTables(context.Background(), db, "costs", "ws_a")
	if err != nil {
		t.Fatalf("a table scoped by `workspace` was not purged: %v", err)
	}
	if removed.Rows != int64(len(tables)) {
		t.Fatalf("removed %d rows across %d tables", removed.Rows, len(tables))
	}
}

// A classified table that is absent, or present with no workspace column, is a
// disagreement between the catalog and the schema. During a DELETION the safe
// reading of a disagreement is that something is not being deleted, so it is
// an error rather than a no-op.
func TestACatalogSchemaDisagreementFailsRatherThanSilentlyDeletingNothing(t *testing.T) {
	empty := sqlDB(t)
	_, err := PurgeCatalogTables(context.Background(), empty, "knowledge", "ws_a")
	if err == nil {
		t.Fatal("purging a resource whose tables do not exist reported success")
	}
	// SQLite would refuse the DELETE on its own with "no such table", so the
	// failure is not what this guard adds — the DIAGNOSIS is. An operator
	// reading a deletion report needs to know the catalog and the schema
	// disagree, not that one statement referenced a missing name.
	if !strings.Contains(err.Error(), "ownership catalog") {
		t.Fatalf("the error blames the SQL rather than the catalog/schema disagreement: %v", err)
	}

	tables := CatalogTables("knowledge")
	var stmts []string
	for _, table := range tables {
		stmts = append(stmts, `CREATE TABLE "`+table+`" (id TEXT)`)
	}
	unscoped := sqlDB(t, stmts...)
	_, err = PurgeCatalogTables(context.Background(), unscoped, "knowledge", "ws_a")
	if err == nil || !strings.Contains(err.Error(), "workspace column") {
		t.Fatalf("a table with no workspace column was purged anyway: %v", err)
	}
}

// Either the resource is gone or it is untouched. A partial delete across a
// resource's tables leaves referential wreckage — chunks without their
// document — that is worse than not having started.
func TestAFailurePartwayLeavesEveryTableUntouched(t *testing.T) {
	tables := CatalogTables("knowledge")
	if len(tables) < 2 {
		t.Skip("knowledge has fewer than two tables")
	}
	var stmts []string
	for i, table := range tables {
		if i == len(tables)-1 {
			// The last one has no workspace column, so the purge fails after
			// deleting from the earlier ones.
			stmts = append(stmts, `CREATE TABLE "`+table+`" (id TEXT)`)
			continue
		}
		stmts = append(stmts,
			`CREATE TABLE "`+table+`" (workspace_id TEXT, id TEXT)`,
			`INSERT INTO "`+table+`" VALUES ('ws_a','1')`)
	}
	db := sqlDB(t, stmts...)

	if _, err := PurgeCatalogTables(context.Background(), db, "knowledge", "ws_a"); err == nil {
		t.Fatal("a purge that could not finish reported success")
	}
	for i, table := range tables {
		if i == len(tables)-1 {
			continue
		}
		var remaining int
		if err := db.QueryRow(`SELECT COUNT(*) FROM "` + table + `"`).Scan(&remaining); err != nil {
			t.Fatal(err)
		}
		if remaining != 1 {
			t.Fatalf("%s lost its rows to a purge that failed partway — the transaction did not roll back", table)
		}
	}
}

func TestASQLIdentifierThatIsNotAnIdentifierIsRefused(t *testing.T) {
	for _, name := range []string{"", "a b", `a"b`, "a;DROP TABLE x", "a-b", "a.b"} {
		if err := assertSafeIdentifier(name); err == nil {
			t.Errorf("%q was accepted as a SQL identifier", name)
		}
	}
	for _, name := range []string{"agent_events", "workspace_id", "T1"} {
		if err := assertSafeIdentifier(name); err != nil {
			t.Errorf("%q was refused: %v", name, err)
		}
	}
}

func TestCoverageNamesTheClassesAPurgerSetDoesNotReach(t *testing.T) {
	all := Coverage(nil)
	if len(all) == 0 {
		t.Fatal("an empty purger set covers everything; this guard would never fire")
	}
	for _, resource := range all {
		for _, planned := range ownership.WorkspaceDeletionPlan() {
			if planned.Resource == resource && planned.Disposition == ownership.Retained {
				t.Errorf("%q is retained by policy and reported as uncovered", resource)
			}
		}
	}
	withOne := Coverage([]Purger{purgerFor(all[0], Removed{}, nil)})
	if len(withOne) != len(all)-1 {
		t.Fatalf("covering one class moved the uncovered count from %d to %d", len(all), len(withOne))
	}
}

// ---------------------------------------------------------------------------
// The filesystem tree purger
// ---------------------------------------------------------------------------

// THE DANGEROUS CASE. wsroot.Dir resolves the personal workspace — and any
// unusable id — to the BASE directory, which is the safe failure for a read
// and the catastrophic one for a delete: "remove this tenant's tree" becomes
// "remove every tenant's tree, and the installation's data with it".
func TestPurgingATreeRefusesToResolveToTheSharedRoot(t *testing.T) {
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "agents", "SOUL.yaml"), []byte("id: bot\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// The message has to distinguish the three causes, because they send an
	// operator to three different places: an uninstall, a bad id, and a bug.
	cases := map[string]string{
		"":                "personal workspace",
		"ws_personal":     "personal workspace",
		".":               "not a usable directory name",
		"..":              "not a usable directory name",
		"Not A Workspace": "not a usable directory name",
	}
	for id, want := range cases {
		_, err := PurgeTree(context.Background(), base, id)
		if err == nil {
			t.Errorf("workspace id %q was allowed to purge the shared base directory", id)
			continue
		}
		if !strings.Contains(err.Error(), want) {
			t.Errorf("workspace id %q refused with %q, which does not say %q", id, err, want)
		}
	}
	if _, err := os.Stat(filepath.Join(base, "agents", "SOUL.yaml")); err != nil {
		t.Fatalf("the shared base directory was deleted: %v", err)
	}
}

func TestPurgingATreeRemovesOnlyThatWorkspace(t *testing.T) {
	base := t.TempDir()
	write := func(ws, name, body string) string {
		dir := wsroot.Dir(base, ws)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	mine := write("ws_a", "a.yaml", "id: mine\n")
	theirs := write("ws_b", "b.yaml", "id: theirs\n")

	removed, err := PurgeTree(context.Background(), base, "ws_a")
	if err != nil {
		t.Fatal(err)
	}
	if removed.Rows != 1 || removed.Bytes == 0 {
		t.Fatalf("removed = %+v, want one file with a size", removed)
	}
	if _, err := os.Stat(mine); !os.IsNotExist(err) {
		t.Fatal("the workspace's own tree survived")
	}
	if _, err := os.Stat(theirs); err != nil {
		t.Fatalf("another workspace's tree was removed: %v", err)
	}

	// A tree that was already absent is a purge that succeeded: the obligation
	// is that the data is gone, not that this call removed it.
	if _, err := PurgeTree(context.Background(), base, "ws_a"); err != nil {
		t.Fatalf("purging an already-empty tree failed: %v", err)
	}
}

// The shared-root check has to be on the WORKSPACE directory, before the
// subpath is appended. Checked on the final path instead, a personal workspace
// slips through whenever sub is non-empty — `<base>/exports` is not `<base>`,
// so the comparison passes and the removal takes every workspace's exports.
func TestASubtreePurgeStillRefusesThePersonalWorkspace(t *testing.T) {
	base := t.TempDir()
	shared := filepath.Join(base, "exports", "everyones.tar.gz")
	if err := os.MkdirAll(filepath.Dir(shared), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shared, []byte("archive"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{"", "ws_personal", ".."} {
		if _, err := PurgeSubtree(context.Background(), base, id, "exports"); err == nil {
			t.Errorf("workspace id %q was allowed to purge the shared exports directory", id)
		}
	}
	if _, err := os.Stat(shared); err != nil {
		t.Fatalf("the shared exports directory was deleted: %v", err)
	}
}

func TestASubtreePurgeRemovesOnlyThatWorkspacesSubdirectory(t *testing.T) {
	base := t.TempDir()
	seed := func(ws string) (string, string) {
		dir := filepath.Join(wsroot.Dir(base, ws), "exports")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		archive := filepath.Join(dir, "a.tar.gz")
		if err := os.WriteFile(archive, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		sibling := filepath.Join(wsroot.Dir(base, ws), "keepme.txt")
		if err := os.WriteFile(sibling, []byte("y"), 0o600); err != nil {
			t.Fatal(err)
		}
		return archive, sibling
	}
	mine, myOther := seed("ws_a")
	theirs, _ := seed("ws_b")

	if _, err := PurgeSubtree(context.Background(), base, "ws_a", "exports"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(mine); !os.IsNotExist(err) {
		t.Fatal("the workspace's own exports survived")
	}
	// A subtree purge takes its subtree and nothing else — the rest of the
	// workspace's files belong to other resource classes with their own
	// purgers and their own report lines.
	if _, err := os.Stat(myOther); err != nil {
		t.Fatalf("a subtree purge removed a sibling directory: %v", err)
	}
	if _, err := os.Stat(theirs); err != nil {
		t.Fatalf("another workspace's exports were removed: %v", err)
	}
}
