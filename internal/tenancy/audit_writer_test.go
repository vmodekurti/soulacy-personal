// audit_writer_test.go — one writer for the mutation audit table.
//
// The bug this exists to stop was live: a hand-written INSERT into
// tenant_mutation_audit named `before_state`/`after_state` against a table that
// defines `before_data`/`after_data`, and omitted `id`, which is a NOT NULL
// primary key with a format CHECK. Three disagreements with the schema in one
// statement. It ran inside the OIDC identity-linking transaction with its error
// returned, so first-time OIDC sign-in failed on the audit write — and the
// symptom, "login is broken", points nowhere near an audit table.
//
// Compilation cannot catch it (SQL is a string), and no test exercised the
// Postgres path. So the guard is structural: exactly one place writes this
// table, and a second INSERT is a build failure rather than a second chance to
// disagree.
package tenancy

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

var auditInsert = regexp.MustCompile(`(?i)INSERT\s+INTO\s+tenant_mutation_audit`)

func TestOnlyInsertAuditWritesTheMutationAuditTable(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		source, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range auditInsert.FindAllStringIndex(string(source), -1) {
			line := strings.Count(string(source)[:match[0]], "\n") + 1
			found++
			// The one legitimate writer. Anything else is a second statement
			// that has to be kept in step with the DDL by hand, which is the
			// thing that failed.
			if !(name == "postgres.go" && withinInsertAudit(string(source), match[0])) {
				t.Errorf("%s:%d writes tenant_mutation_audit directly — route it through insertAudit, "+
					"which is the one statement kept in step with the schema", name, line)
			}
		}
	}
	if found == 0 {
		// Without this the guard passes vacuously the moment the table is
		// renamed, which is exactly when a stale hand-written insert would be
		// most likely to appear.
		t.Fatal("no INSERT INTO tenant_mutation_audit found at all — has the table been renamed?")
	}
}

// withinInsertAudit reports whether an offset falls inside func insertAudit.
func withinInsertAudit(source string, offset int) bool {
	start := strings.Index(source, "func insertAudit(")
	if start < 0 || offset < start {
		return false
	}
	end := strings.Index(source[start:], "\n}\n")
	if end < 0 {
		return false
	}
	return offset < start+end
}

// The columns insertAudit names must be the ones the DDL defines. A rename on
// one side is invisible until a mutation actually runs against Postgres, which
// in this repo means until somebody signs in.
func TestTheAuditInsertNamesTheColumnsTheSchemaDefines(t *testing.T) {
	source, err := os.ReadFile("postgres.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	ddlStart := strings.Index(text, "CREATE TABLE IF NOT EXISTS tenant_mutation_audit(")
	if ddlStart < 0 {
		t.Fatal("the tenant_mutation_audit DDL is gone")
	}
	ddl := text[ddlStart : ddlStart+strings.Index(text[ddlStart:], "`")]

	insertStart := strings.Index(text, "INSERT INTO tenant_mutation_audit(")
	if insertStart < 0 {
		t.Fatal("the tenant_mutation_audit INSERT is gone")
	}
	columnList := text[insertStart+len("INSERT INTO tenant_mutation_audit("):]
	columnList = columnList[:strings.Index(columnList, ")")]

	named := map[string]bool{}
	for _, column := range strings.Split(columnList, ",") {
		column = strings.TrimSpace(column)
		if column == "" {
			continue
		}
		named[column] = true
		if !strings.Contains(ddl, column) {
			t.Errorf("the audit INSERT names column %q, which the DDL does not define", column)
		}
	}
	// And the NOT NULL primary key must be among them. Omitting it is the
	// other half of the original bug, and it is the half a column-name check
	// alone would miss.
	//
	// Matched as a whole column rather than as a substring: `request_id` and
	// `resource_id` both contain "id", so a substring check passes with the
	// primary key gone — which is precisely the mutation that found this.
	if !named["id"] {
		t.Error("the audit INSERT omits the id column, which is a NOT NULL primary key with a format CHECK")
	}
}
