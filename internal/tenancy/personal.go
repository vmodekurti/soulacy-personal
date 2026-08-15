// Package tenancy owns Soulacy's organization, workspace, user, membership,
// and legacy-resource assignment metadata.
package tenancy

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"

	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/sqlitex"
)

const PersonalTenantSchemaVersion = 1

type PersonalTenant struct {
	OrganizationID string
	WorkspaceID    string
	UserID         string
	MembershipID   string
}

type ResourceAssignment struct {
	Kind string
	Path string
}

type PersonalMigrationPlan struct {
	DatabasePath string
	Version      int
	AlreadyDone  bool
	Resources    []ResourceAssignment
}

// PlanPersonalMigration performs only filesystem reads. It is safe for CLI
// previews and never creates the tenant database or workspace directories.
func PlanPersonalMigration(ws config.Paths) (PersonalMigrationPlan, error) {
	plan := PersonalMigrationPlan{
		DatabasePath: filepath.Join(ws.Data, "tenants.db"),
		Version:      PersonalTenantSchemaVersion,
	}
	if version, err := currentVersion(plan.DatabasePath); err != nil {
		return plan, err
	} else if version >= PersonalTenantSchemaVersion {
		plan.AlreadyDone = true
	}

	candidates := []ResourceAssignment{
		{Kind: "agents", Path: ws.Agents},
		{Kind: "skills", Path: ws.Skills},
		{Kind: "plugins", Path: ws.Plugins},
		{Kind: "templates", Path: ws.Templates},
		{Kind: "memory", Path: ws.Memory},
		{Kind: "credentials", Path: ws.CredentialsDB()},
		{Kind: "action_events", Path: ws.DB("actions")},
		{Kind: "conversations", Path: ws.DB("history")},
		{Kind: "memory_archive", Path: ws.DB("archive")},
		{Kind: "schedules", Path: ws.DB("schedules")},
		{Kind: "knowledge", Path: ws.DB("knowledge")},
		{Kind: "costs", Path: ws.DB("costs")},
		{Kind: "studio", Path: ws.DB("studio")},
		{Kind: "workboard", Path: ws.DB("workboard")},
		{Kind: "approvals", Path: ws.DB("checkpoints")},
		{Kind: "secrets", Path: ws.Secrets},
	}
	for _, candidate := range candidates {
		if _, err := os.Stat(candidate.Path); err == nil {
			candidate.Path = relativeOrClean(ws.Root, candidate.Path)
			plan.Resources = append(plan.Resources, candidate)
		} else if !os.IsNotExist(err) {
			return plan, fmt.Errorf("inspect %s: %w", candidate.Path, err)
		}
	}
	sort.Slice(plan.Resources, func(i, j int) bool {
		if plan.Resources[i].Kind == plan.Resources[j].Kind {
			return plan.Resources[i].Path < plan.Resources[j].Path
		}
		return plan.Resources[i].Kind < plan.Resources[j].Kind
	})
	return plan, nil
}

// EnsurePersonalTenant creates the implicit Personal tenant exactly once. A
// fresh catalog is built in a temporary SQLite file and atomically renamed,
// so interruption cannot replace a working installation with a partial file.
// Existing catalogs are advanced in one transaction and are safely resumable.
func EnsurePersonalTenant(ctx context.Context, ws config.Paths) (PersonalTenant, PersonalMigrationPlan, error) {
	plan, err := PlanPersonalMigration(ws)
	if err != nil {
		return PersonalTenant{}, plan, err
	}
	if err := os.MkdirAll(filepath.Dir(plan.DatabasePath), 0o755); err != nil {
		return PersonalTenant{}, plan, err
	}

	target := plan.DatabasePath
	workPath := target
	fresh := false
	if _, statErr := os.Stat(target); os.IsNotExist(statErr) {
		fresh = true
		workPath = target + ".migrating-" + uuid.NewString()
		defer os.Remove(workPath)
	}

	opts := sqlitex.DefaultOptions()
	opts.ForeignKeys = true
	var db *sql.DB
	if fresh {
		u := &url.URL{Scheme: "file", Path: workPath}
		db, err = sql.Open("sqlite3", u.String()+"?_journal_mode=DELETE&_foreign_keys=on&_busy_timeout=30000")
	} else {
		db, err = sqlitex.Open(workPath, opts)
	}
	if err != nil {
		return PersonalTenant{}, plan, fmt.Errorf("open tenant catalog: %w", err)
	}
	tenant, migrateErr := migratePersonal(ctx, db, plan)
	closeErr := db.Close()
	if migrateErr != nil {
		return PersonalTenant{}, plan, migrateErr
	}
	if closeErr != nil {
		return PersonalTenant{}, plan, closeErr
	}
	if fresh {
		if err := os.Rename(workPath, target); err != nil {
			return PersonalTenant{}, plan, fmt.Errorf("publish tenant catalog: %w", err)
		}
	}
	plan.AlreadyDone = true
	return tenant, plan, nil
}

func migratePersonal(ctx context.Context, db *sql.DB, plan PersonalMigrationPlan) (tenant PersonalTenant, err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return tenant, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	for _, statement := range schemaStatements {
		if _, err = tx.ExecContext(ctx, statement); err != nil {
			return tenant, fmt.Errorf("tenant schema: %w", err)
		}
	}

	err = tx.QueryRowContext(ctx, `SELECT organization_id, workspace_id, user_id, membership_id
		FROM personal_tenant WHERE singleton = 1`).Scan(
		&tenant.OrganizationID, &tenant.WorkspaceID, &tenant.UserID, &tenant.MembershipID)
	if err == sql.ErrNoRows {
		tenant = PersonalTenant{
			OrganizationID: newID("org"),
			WorkspaceID:    newID("ws"),
			UserID:         newID("usr"),
			MembershipID:   newID("mem"),
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		if _, err = tx.ExecContext(ctx, `INSERT INTO organizations(id, name, created_at) VALUES(?, ?, ?)`, tenant.OrganizationID, "Personal", now); err != nil {
			return tenant, err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO workspaces(id, organization_id, name, created_at) VALUES(?, ?, ?, ?)`, tenant.WorkspaceID, tenant.OrganizationID, "Personal Workspace", now); err != nil {
			return tenant, err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO users(id, display_name, created_at) VALUES(?, ?, ?)`, tenant.UserID, "Local Owner", now); err != nil {
			return tenant, err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO memberships(id, organization_id, workspace_id, user_id, role, created_at) VALUES(?, ?, ?, ?, 'owner', ?)`, tenant.MembershipID, tenant.OrganizationID, tenant.WorkspaceID, tenant.UserID, now); err != nil {
			return tenant, err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO personal_tenant(singleton, organization_id, workspace_id, user_id, membership_id) VALUES(1, ?, ?, ?, ?)`, tenant.OrganizationID, tenant.WorkspaceID, tenant.UserID, tenant.MembershipID); err != nil {
			return tenant, err
		}
	} else if err != nil {
		return tenant, err
	}

	for _, resource := range plan.Resources {
		if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO legacy_resource_assignments(workspace_id, kind, resource_ref) VALUES(?, ?, ?)`, tenant.WorkspaceID, resource.Kind, resource.Path); err != nil {
			return tenant, fmt.Errorf("assign %s: %w", resource.Path, err)
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO tenant_migrations(version, applied_at) VALUES(?, ?)
		ON CONFLICT(version) DO NOTHING`, PersonalTenantSchemaVersion, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return tenant, err
	}
	if err = tx.Commit(); err != nil {
		return tenant, err
	}
	return tenant, nil
}

func currentVersion(path string) (int, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return 0, nil
	} else if err != nil {
		return 0, err
	}
	u := &url.URL{Scheme: "file", Path: path}
	db, err := sql.Open("sqlite3", u.String()+"?mode=ro&_query_only=on")
	if err != nil {
		return 0, err
	}
	defer db.Close()
	var version int
	err = db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM tenant_migrations`).Scan(&version)
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "no such table") {
		return 0, nil
	}
	return version, err
}

func newID(prefix string) string { return prefix + "_" + strings.ReplaceAll(uuid.NewString(), "-", "") }

func relativeOrClean(root, path string) string {
	if relative, err := filepath.Rel(root, path); err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return filepath.ToSlash(relative)
	}
	return filepath.Clean(path)
}

var schemaStatements = []string{
	`CREATE TABLE IF NOT EXISTS organizations(id TEXT PRIMARY KEY, name TEXT NOT NULL, created_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS workspaces(id TEXT PRIMARY KEY, organization_id TEXT NOT NULL REFERENCES organizations(id), name TEXT NOT NULL, created_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS users(id TEXT PRIMARY KEY, display_name TEXT NOT NULL, created_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS memberships(id TEXT PRIMARY KEY, organization_id TEXT NOT NULL REFERENCES organizations(id), workspace_id TEXT NOT NULL REFERENCES workspaces(id), user_id TEXT NOT NULL REFERENCES users(id), role TEXT NOT NULL, created_at TEXT NOT NULL, UNIQUE(workspace_id, user_id))`,
	`CREATE TABLE IF NOT EXISTS personal_tenant(singleton INTEGER PRIMARY KEY CHECK(singleton = 1), organization_id TEXT NOT NULL, workspace_id TEXT NOT NULL, user_id TEXT NOT NULL, membership_id TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS legacy_resource_assignments(workspace_id TEXT NOT NULL, kind TEXT NOT NULL, resource_ref TEXT NOT NULL, PRIMARY KEY(workspace_id, kind, resource_ref))`,
	`CREATE TABLE IF NOT EXISTS tenant_migrations(version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`,
}
