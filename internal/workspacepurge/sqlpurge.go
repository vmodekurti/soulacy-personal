package workspacepurge

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/soulacy/soulacy/internal/ownership"
	"github.com/soulacy/soulacy/internal/wsroot"
)

// CatalogTables returns the durable tables the ownership catalog attributes to
// one resource class, in a stable order.
//
// READING THE CATALOG rather than taking a list is what makes a purger stay
// correct. A table added to a resource — `internal/knowledge` gaining a fourth
// table, say — is purged the day it is classified, with no second edit
// anywhere. The alternative is a hand-written list per purger, which is the
// same second-inventory mistake the exporter avoids, except that here the
// consequence of drift is data surviving a deletion rather than missing from
// an archive.
func CatalogTables(resource string) []string {
	seen := map[string]bool{}
	var names []string
	for _, table := range ownership.Tables {
		if table.Resource != resource {
			continue
		}
		if table.Class != ownership.WorkspaceOwned && table.Class != ownership.UserPrivate {
			// Platform-global and org-owned tables outlive the workspace even
			// when a workspace-owned resource happens to reference them.
			continue
		}
		if seen[table.Name] {
			continue
		}
		seen[table.Name] = true
		names = append(names, table.Name)
	}
	sort.Strings(names)
	return names
}

// workspaceColumn finds the column a table scopes by, from the table's ACTUAL
// schema.
//
// Read from `PRAGMA table_info` rather than from the catalog's ScopeKey prose,
// which says things like "workspace_id (column: workspace)". Parsing a
// human-written sentence for a SQL identifier would work until somebody
// rephrases it, and the failure would be a DELETE that matches nothing and
// reports success. Asking the database is both simpler and impossible to get
// out of step.
func workspaceColumn(ctx context.Context, db *sql.DB, table string) (string, error) {
	rows, err := db.QueryContext(ctx, `SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	columns := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return "", err
		}
		columns[name] = true
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if len(columns) == 0 {
		// The table is classified and absent. That is not "nothing to delete":
		// it means the catalog and the schema disagree, and the safe reading
		// of a disagreement during a DELETION is that something is not being
		// deleted.
		return "", fmt.Errorf("table %q is in the ownership catalog but not in this database", table)
	}
	for _, candidate := range []string{"workspace_id", "workspace"} {
		if columns[candidate] {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("table %q has no workspace column, so its rows cannot be scoped to a tenant", table)
}

// PurgeCatalogTables deletes one workspace's rows from every table the catalog
// attributes to a resource.
//
// IN ONE TRANSACTION, deliberately. A partial delete across a resource's
// tables leaves referential wreckage — chunks without their document, versions
// without their credential — that is worse than not having started, because
// the next attempt has to reason about a state no code produced. Either the
// resource is gone or it is untouched.
//
// The identifier is interpolated because SQL placeholders cannot name a table.
// It is safe here for a reason worth stating rather than assuming: the value
// comes from `ownership.Tables`, a compiled-in Go literal, never from a
// request. `assertSafeIdentifier` enforces that at the point of use so the
// reasoning survives someone later making the source dynamic.
func PurgeCatalogTables(ctx context.Context, db *sql.DB, resource, workspaceID string) (Removed, error) {
	tables := CatalogTables(resource)
	if len(tables) == 0 {
		return Removed{}, fmt.Errorf("no durable tables are classified under resource %q", resource)
	}
	workspaceID = wsroot.Normalize(workspaceID)

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return Removed{}, err
	}
	defer func() { _ = tx.Rollback() }()

	var total int64
	for _, table := range tables {
		if err := assertSafeIdentifier(table); err != nil {
			return Removed{}, err
		}
		column, err := workspaceColumn(ctx, db, table)
		if err != nil {
			return Removed{}, err
		}
		if err := assertSafeIdentifier(column); err != nil {
			return Removed{}, err
		}
		result, err := tx.ExecContext(ctx,
			fmt.Sprintf(`DELETE FROM %q WHERE %q = ?`, table, column), workspaceID)
		if err != nil {
			return Removed{}, fmt.Errorf("purging %s: %w", table, err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return Removed{}, err
		}
		total += affected
	}
	if err := tx.Commit(); err != nil {
		return Removed{}, err
	}
	return Removed{Rows: total, Note: "tables: " + strings.Join(tables, ", ")}, nil
}

// assertSafeIdentifier refuses anything that is not a plain SQL identifier.
//
// Belt and braces: every value that reaches here today is a compiled-in
// constant, so this can never fire. It exists so that if a future caller
// derives a table name from configuration or a request, the failure is an
// error at that call rather than an injection.
func assertSafeIdentifier(name string) error {
	if name == "" {
		return fmt.Errorf("empty SQL identifier")
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
		default:
			return fmt.Errorf("refusing %q as a SQL identifier: it is not [A-Za-z0-9_]", name)
		}
	}
	return nil
}

// TablePurger builds a Purger that clears a resource's catalogued tables.
func TablePurger(resource string, db *sql.DB) Purger {
	return Purger{
		Resource: resource,
		Purge: func(ctx context.Context, workspaceID string) (Removed, error) {
			if db == nil {
				return Removed{}, fmt.Errorf("no database is wired for resource %q", resource)
			}
			return PurgeCatalogTables(ctx, db, resource, workspaceID)
		},
	}
}
