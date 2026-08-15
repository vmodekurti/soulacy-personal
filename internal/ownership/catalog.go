// Package ownership is the authoritative tenant-ownership catalog for every
// Soulacy resource and durable table. Multi-user code must update this catalog
// before introducing persistence so an unclassified store cannot silently
// become cross-tenant state.
package ownership

import (
	"fmt"
	"sort"
	"strings"
)

type Class string

const (
	PlatformGlobal    Class = "platform-global"
	OrganizationOwned Class = "organization-owned"
	WorkspaceOwned    Class = "workspace-owned"
	UserPrivate       Class = "user-private"
	Ephemeral         Class = "ephemeral"
)

type Isolation string

const (
	Scoped       Isolation = "scoped"
	PersonalOnly Isolation = "personal-only"
)

type Resource struct {
	Name      string
	Class     Class
	ScopeKey  string
	Retention string
	Export    string
	Deletion  string
	Backup    string
}

type Table struct {
	Source              string
	Name                string
	Resource            string
	Class               Class
	ScopeKey            string
	CompositeUniqueness bool
	Isolation           Isolation
	IsolationTest       string
}

// Repository classifies source files that define Store/Archive/Vault-style
// persistence contracts, including file, JSON, in-memory, and external stores
// that do not declare a SQL table themselves.
type Repository struct {
	Source        string
	Resource      string
	Class         Class
	ScopeKey      string
	Isolation     Isolation
	IsolationTest string
}

var Resources = []Resource{
	{Name: "agents", Class: WorkspaceOwned, ScopeKey: "workspace_id", Retention: "until deleted", Export: "SOUL.yaml/package export", Deletion: "soft delete then retention purge", Backup: "workspace database and artifact backup"},
	{Name: "definitions", Class: WorkspaceOwned, ScopeKey: "workspace_id", Retention: "version policy", Export: "agent package", Deletion: "with agent/version policy", Backup: "workspace database"},
	{Name: "sessions", Class: UserPrivate, ScopeKey: "workspace_id,user_id", Retention: "runtime retention policy", Export: "conversation export", Deletion: "user or workspace purge", Backup: "workspace database"},
	{Name: "messages", Class: UserPrivate, ScopeKey: "workspace_id,user_id", Retention: "conversation retention policy", Export: "conversation export", Deletion: "with session or privacy request", Backup: "workspace database"},
	{Name: "events", Class: WorkspaceOwned, ScopeKey: "workspace_id", Retention: "action-event retention policy", Export: "audit/run export", Deletion: "retention purge", Backup: "workspace database and log archive"},
	{Name: "memory", Class: UserPrivate, ScopeKey: "workspace_id,user_id", Retention: "memory policy", Export: "memory export", Deletion: "user/workspace purge", Backup: "workspace database"},
	{Name: "costs", Class: WorkspaceOwned, ScopeKey: "workspace_id", Retention: "billing retention policy", Export: "CSV/JSON", Deletion: "policy purge; aggregates may remain", Backup: "workspace database"},
	{Name: "credentials", Class: UserPrivate, ScopeKey: "principal -> memberships", Retention: "until revoked/expired", Export: "metadata only; never secrets", Deletion: "revoke then purge hash", Backup: "encrypted database; restore requires KMS"},
	{Name: "secrets", Class: WorkspaceOwned, ScopeKey: "workspace_id", Retention: "until deleted", Export: "names only; values excluded", Deletion: "cryptographic erasure and purge", Backup: "encrypted vault; restore requires KMS"},
	{Name: "schedules", Class: WorkspaceOwned, ScopeKey: "workspace_id", Retention: "until deleted", Export: "agent/package export", Deletion: "disable then purge", Backup: "workspace database"},
	{Name: "approvals", Class: UserPrivate, ScopeKey: "workspace_id,user_id", Retention: "audit retention policy", Export: "audit export", Deletion: "retention purge", Backup: "workspace database"},
	{Name: "knowledge", Class: WorkspaceOwned, ScopeKey: "workspace_id", Retention: "until KB/document deletion", Export: "source documents and manifest", Deletion: "document, chunks, embeddings, jobs", Backup: "workspace database and objects"},
	{Name: "vectors", Class: WorkspaceOwned, ScopeKey: "workspace_id", Retention: "follows source resource", Export: "regenerable metadata", Deletion: "with source resource", Backup: "optional; rebuild from sources"},
	{Name: "artifacts", Class: WorkspaceOwned, ScopeKey: "workspace_id", Retention: "artifact retention policy", Export: "artifact download", Deletion: "metadata and object purge", Backup: "object store and manifest"},
	{Name: "workboard", Class: WorkspaceOwned, ScopeKey: "workspace_id", Retention: "until project deletion", Export: "JSON/CSV and artifacts", Deletion: "cascade task data", Backup: "workspace database and objects"},
	{Name: "studio-drafts", Class: UserPrivate, ScopeKey: "workspace_id,user_id", Retention: "draft retention policy", Export: "SOUL.yaml", Deletion: "user/workspace purge", Backup: "workspace database"},
	{Name: "studio-traces", Class: WorkspaceOwned, ScopeKey: "workspace_id", Retention: "trace retention policy", Export: "diagnostic bundle", Deletion: "retention purge", Backup: "workspace database/log archive"},
	{Name: "studio-learning", Class: WorkspaceOwned, ScopeKey: "workspace_id", Retention: "learning policy", Export: "lessons/rules export", Deletion: "lesson/rule purge", Backup: "workspace database"},
	{Name: "skills", Class: WorkspaceOwned, ScopeKey: "workspace_id", Retention: "until uninstalled", Export: "package/reference", Deletion: "workspace uninstall", Backup: "package manifest and workspace files"},
	{Name: "mcp", Class: WorkspaceOwned, ScopeKey: "workspace_id", Retention: "until removed", Export: "redacted configuration", Deletion: "workspace removal", Backup: "redacted config plus secret references"},
	{Name: "plugins", Class: OrganizationOwned, ScopeKey: "organization_id", Retention: "until uninstalled", Export: "package/reference", Deletion: "organization uninstall", Backup: "package manifest and shared files"},
	{Name: "registries", Class: OrganizationOwned, ScopeKey: "organization_id", Retention: "until removed", Export: "redacted configuration", Deletion: "organization removal", Backup: "configuration"},
	{Name: "channels", Class: WorkspaceOwned, ScopeKey: "workspace_id", Retention: "until removed", Export: "redacted configuration", Deletion: "disconnect and purge", Backup: "redacted config plus secret references"},
	{Name: "webhooks", Class: WorkspaceOwned, ScopeKey: "workspace_id", Retention: "until removed", Export: "redacted configuration", Deletion: "disable and purge", Backup: "redacted config plus secret references"},
	{Name: "shares", Class: WorkspaceOwned, ScopeKey: "workspace_id", Retention: "until expiry/revocation", Export: "metadata only", Deletion: "revoke then purge", Backup: "workspace database"},
	{Name: "api-keys", Class: UserPrivate, ScopeKey: "principal -> memberships", Retention: "until expiry/revocation", Export: "metadata only; never plaintext", Deletion: "revoke then purge hash", Backup: "workspace database"},
	{Name: "audit", Class: OrganizationOwned, ScopeKey: "organization_id", Retention: "audit retention/legal policy", Export: "signed audit export", Deletion: "policy-controlled purge", Backup: "append-only database/archive"},
	{Name: "tenancy", Class: PlatformGlobal, ScopeKey: "tenant foreign keys", Retention: "account lifecycle", Export: "administrative export", Deletion: "ordered tenant deletion", Backup: "platform database"},
	{Name: "queue-dlq", Class: WorkspaceOwned, ScopeKey: "workspace_id", Retention: "DLQ retention policy", Export: "diagnostic export", Deletion: "acknowledge/purge", Backup: "workspace database or durable queue"},
	{Name: "idempotency", Class: Ephemeral, ScopeKey: "workspace_id", Retention: "24h replay window, bounded by eviction", Export: "not applicable", Deletion: "TTL expiry, eviction, or process restart", Backup: "none; a lost record only means a retry re-executes"},
	{Name: "schema-metadata", Class: PlatformGlobal, ScopeKey: "none", Retention: "permanent", Export: "not applicable", Deletion: "never during normal operation", Backup: "with containing database"},
}

// Tables is deliberately explicit. PersonalOnly entries remain valid for
// backwards-compatible Personal deployments but cannot serve Team/Scale data.
var Tables = []Table{
	{Source: "internal/actionlog/actionlog.go", Name: "agent_events", Resource: "events", Class: WorkspaceOwned, ScopeKey: "workspace_id", CompositeUniqueness: true, Isolation: Scoped, IsolationTest: "internal/actionlog/workspace_test.go"},
	{Source: "internal/agentmemory/rulelog.go", Name: "rulebook_locks", Resource: "studio-learning", Class: WorkspaceOwned, ScopeKey: "workspace_id", CompositeUniqueness: true, Isolation: PersonalOnly, IsolationTest: "internal/ownership/catalog_test.go"},
	{Source: "internal/agentmemory/rulelog.go", Name: "rulebook_versions", Resource: "studio-learning", Class: WorkspaceOwned, ScopeKey: "workspace_id", CompositeUniqueness: true, Isolation: PersonalOnly, IsolationTest: "internal/ownership/catalog_test.go"},
	{Source: "internal/auth/apikeys/postgres.go", Name: "access_credentials", Resource: "api-keys", Class: UserPrivate, ScopeKey: "organization_id,workspace_ids,subject_id", CompositeUniqueness: true, Isolation: Scoped, IsolationTest: "internal/auth/apikeys/postgres_test.go"},
	{Source: "internal/auth/apikeys/store.go", Name: "api_keys", Resource: "api-keys", Class: UserPrivate, ScopeKey: "workspace_id,user_id", CompositeUniqueness: true, Isolation: PersonalOnly, IsolationTest: "internal/ownership/catalog_test.go"},
	// cost_reconciliations is reclassified, not scoped. It records a comparison
	// against the *provider's invoice*, and providers bill the deployment
	// rather than the tenant. There is no honest way to split one invoice
	// across workspaces here, so scoping it would add fiction rather than
	// isolation. Per-tenant attribution is what the chargeback query is for,
	// and that one is workspace-scoped.
	{Source: "internal/costs/store.go", Name: "cost_reconciliations", Resource: "costs", Class: PlatformGlobal, ScopeKey: "provider invoice period", CompositeUniqueness: true, Isolation: PersonalOnly, IsolationTest: "internal/ownership/catalog_test.go"},
	{Source: "internal/costs/store.go", Name: "cost_reservations", Resource: "costs", Class: WorkspaceOwned, ScopeKey: "workspace_id (column: workspace)", CompositeUniqueness: true, Isolation: Scoped, IsolationTest: "internal/costs/workspace_test.go"},
	{Source: "internal/costs/store.go", Name: "token_usage", Resource: "costs", Class: WorkspaceOwned, ScopeKey: "workspace_id (column: workspace)", CompositeUniqueness: true, Isolation: Scoped, IsolationTest: "internal/costs/workspace_test.go"},
	{Source: "internal/credentials/rotation.go", Name: "credential_versions", Resource: "secrets", Class: WorkspaceOwned, ScopeKey: "workspace_id", CompositeUniqueness: true, Isolation: Scoped, IsolationTest: "internal/credentials/workspace_test.go"},
	{Source: "internal/credentials/vault.go", Name: "credentials", Resource: "secrets", Class: WorkspaceOwned, ScopeKey: "workspace_id", CompositeUniqueness: true, Isolation: Scoped, IsolationTest: "internal/credentials/workspace_test.go"},
	{Source: "internal/knowledge/jobs.go", Name: "ingest_jobs", Resource: "knowledge", Class: WorkspaceOwned, ScopeKey: "workspace_id", CompositeUniqueness: true, Isolation: Scoped, IsolationTest: "internal/knowledge/workspace_test.go"},
	{Source: "internal/knowledge/store.go", Name: "chunks", Resource: "knowledge", Class: WorkspaceOwned, ScopeKey: "workspace_id", CompositeUniqueness: true, Isolation: Scoped, IsolationTest: "internal/knowledge/workspace_test.go"},
	{Source: "internal/knowledge/store.go", Name: "documents", Resource: "knowledge", Class: WorkspaceOwned, ScopeKey: "workspace_id", CompositeUniqueness: true, Isolation: Scoped, IsolationTest: "internal/knowledge/workspace_test.go"},
	{Source: "internal/knowledge/store.go", Name: "knowledge_bases", Resource: "knowledge", Class: WorkspaceOwned, ScopeKey: "workspace_id", CompositeUniqueness: true, Isolation: Scoped, IsolationTest: "internal/knowledge/workspace_test.go"},
	{Source: "internal/memory/sqlite.go", Name: "memories", Resource: "memory", Class: UserPrivate, ScopeKey: "workspace_id,user_id", CompositeUniqueness: true, Isolation: Scoped, IsolationTest: "internal/memory/workspace_test.go"},
	{Source: "internal/memory/vector.go", Name: "memory_vector_meta", Resource: "vectors", Class: WorkspaceOwned, ScopeKey: "workspace_id", CompositeUniqueness: true, Isolation: Scoped, IsolationTest: "internal/memory/vector_workspace_test.go"},
	{Source: "internal/pluginmigrate/runner.go", Name: "plugin_schema_migrations", Resource: "schema-metadata", Class: PlatformGlobal, ScopeKey: "none", Isolation: Scoped, IsolationTest: "internal/pluginmigrate/runner_test.go"},
	{Source: "internal/queue/dlq/dlq.go", Name: "dead_letters", Resource: "queue-dlq", Class: WorkspaceOwned, ScopeKey: "workspace_id", CompositeUniqueness: true, Isolation: PersonalOnly, IsolationTest: "internal/ownership/catalog_test.go"},
	{Source: "internal/rbac/store.go", Name: "rbac_agent_grants", Resource: "agents", Class: WorkspaceOwned, ScopeKey: "workspace_id", CompositeUniqueness: true, Isolation: Scoped, IsolationTest: "internal/rbac/workspace_test.go"},
	{Source: "internal/runtime/checkpoint.go", Name: "workflow_checkpoints", Resource: "sessions", Class: UserPrivate, ScopeKey: "workspace_id,user_id", CompositeUniqueness: true, Isolation: PersonalOnly, IsolationTest: "internal/ownership/catalog_test.go"},
	{Source: "internal/session/history.go", Name: "conversation_history", Resource: "messages", Class: UserPrivate, ScopeKey: "workspace_id,user_id", CompositeUniqueness: true, Isolation: Scoped, IsolationTest: "internal/session/history_workspace_test.go"},
	{Source: "internal/session/ownership.go", Name: "session_owners", Resource: "sessions", Class: UserPrivate, ScopeKey: "workspace_id,creator", CompositeUniqueness: true, Isolation: Scoped, IsolationTest: "internal/session/ownership_test.go"},
	{Source: "internal/session/store.go", Name: "session_resources", Resource: "sessions", Class: UserPrivate, ScopeKey: "workspace_id,user_id via session ownership", CompositeUniqueness: true, Isolation: Scoped, IsolationTest: "internal/session/resources_workspace_test.go"},
	{Source: "internal/sqlitex/schemaversion.go", Name: "soulacy_schema_version", Resource: "schema-metadata", Class: PlatformGlobal, ScopeKey: "none", Isolation: Scoped, IsolationTest: "internal/sqlitex/schemaversion_test.go"},
	{Source: "internal/storage/postgres/postgres.go", Name: "agent_events", Resource: "events", Class: WorkspaceOwned, ScopeKey: "workspace_id", CompositeUniqueness: true, Isolation: Scoped, IsolationTest: "internal/storage/postgres/workspace_test.go"},
	{Source: "internal/storage/postgres/postgres.go", Name: "memories", Resource: "memory", Class: UserPrivate, ScopeKey: "workspace_id,user_id", CompositeUniqueness: true, Isolation: Scoped, IsolationTest: "internal/memory/workspace_test.go"},
	{Source: "internal/studio/lessons.go", Name: "lesson_meta", Resource: "studio-learning", Class: WorkspaceOwned, ScopeKey: "workspace_id", CompositeUniqueness: true, Isolation: Scoped, IsolationTest: "internal/gateway/studio_scope_test.go"},
	{Source: "internal/studio/lessons.go", Name: "lessons", Resource: "studio-learning", Class: WorkspaceOwned, ScopeKey: "workspace_id", CompositeUniqueness: true, Isolation: Scoped, IsolationTest: "internal/gateway/studio_scope_test.go"},
	{Source: "internal/tenancy/personal.go", Name: "legacy_resource_assignments", Resource: "tenancy", Class: WorkspaceOwned, ScopeKey: "workspace_id", CompositeUniqueness: true, Isolation: Scoped, IsolationTest: "internal/tenancy/personal_test.go"},
	{Source: "internal/tenancy/personal.go", Name: "memberships", Resource: "tenancy", Class: PlatformGlobal, ScopeKey: "tenant foreign keys", Isolation: Scoped, IsolationTest: "internal/tenancy/personal_test.go"},
	{Source: "internal/tenancy/personal.go", Name: "organizations", Resource: "tenancy", Class: PlatformGlobal, ScopeKey: "tenant primary key", Isolation: Scoped, IsolationTest: "internal/tenancy/personal_test.go"},
	{Source: "internal/tenancy/personal.go", Name: "personal_tenant", Resource: "tenancy", Class: PlatformGlobal, ScopeKey: "singleton tenant foreign keys", Isolation: Scoped, IsolationTest: "internal/tenancy/personal_test.go"},
	{Source: "internal/tenancy/personal.go", Name: "tenant_migrations", Resource: "schema-metadata", Class: PlatformGlobal, ScopeKey: "none", Isolation: Scoped, IsolationTest: "internal/tenancy/personal_test.go"},
	{Source: "internal/tenancy/personal.go", Name: "users", Resource: "tenancy", Class: PlatformGlobal, ScopeKey: "tenant primary key", Isolation: Scoped, IsolationTest: "internal/tenancy/personal_test.go"},
	{Source: "internal/tenancy/personal.go", Name: "workspaces", Resource: "tenancy", Class: PlatformGlobal, ScopeKey: "organization foreign key", Isolation: Scoped, IsolationTest: "internal/tenancy/personal_test.go"},
	{Source: "internal/tenancy/postgres.go", Name: "credentials", Resource: "credentials", Class: UserPrivate, ScopeKey: "workspace_id via principal memberships", CompositeUniqueness: true, Isolation: PersonalOnly, IsolationTest: "internal/ownership/catalog_test.go"},
	{Source: "internal/tenancy/postgres.go", Name: "identities", Resource: "tenancy", Class: PlatformGlobal, ScopeKey: "user foreign key", Isolation: Scoped, IsolationTest: "internal/tenancy/postgres_test.go"},
	{Source: "internal/tenancy/postgres.go", Name: "invitations", Resource: "tenancy", Class: WorkspaceOwned, ScopeKey: "workspace_id", CompositeUniqueness: true, Isolation: Scoped, IsolationTest: "internal/tenancy/postgres_test.go"},
	{Source: "internal/tenancy/postgres.go", Name: "memberships", Resource: "tenancy", Class: WorkspaceOwned, ScopeKey: "workspace_id", CompositeUniqueness: true, Isolation: Scoped, IsolationTest: "internal/tenancy/postgres_test.go"},
	{Source: "internal/tenancy/postgres.go", Name: "organizations", Resource: "tenancy", Class: PlatformGlobal, ScopeKey: "tenant primary key", Isolation: Scoped, IsolationTest: "internal/tenancy/postgres_test.go"},
	{Source: "internal/tenancy/postgres.go", Name: "service_account_workspaces", Resource: "credentials", Class: WorkspaceOwned, ScopeKey: "workspace_id", CompositeUniqueness: true, Isolation: Scoped, IsolationTest: "internal/tenancy/postgres_test.go"},
	{Source: "internal/tenancy/postgres.go", Name: "service_accounts", Resource: "credentials", Class: WorkspaceOwned, ScopeKey: "workspace_id", CompositeUniqueness: true, Isolation: Scoped, IsolationTest: "internal/tenancy/postgres_test.go"},
	{Source: "internal/tenancy/postgres.go", Name: "tenant_mutation_audit", Resource: "audit", Class: PlatformGlobal, ScopeKey: "resource reference", Isolation: Scoped, IsolationTest: "internal/tenancy/postgres_test.go"},
	{Source: "internal/tenancy/postgres.go", Name: "users", Resource: "tenancy", Class: PlatformGlobal, ScopeKey: "tenant primary key", Isolation: Scoped, IsolationTest: "internal/tenancy/postgres_test.go"},
	{Source: "internal/tenancy/postgres.go", Name: "workspaces", Resource: "tenancy", Class: OrganizationOwned, ScopeKey: "organization_id", Isolation: Scoped, IsolationTest: "internal/tenancy/postgres_test.go"},
	{Source: "internal/workboard/artifacts.go", Name: "workboard_artifacts", Resource: "artifacts", Class: WorkspaceOwned, ScopeKey: "workspace_id via workboard_tasks", CompositeUniqueness: true, Isolation: Scoped, IsolationTest: "internal/workboard/workspace_test.go"},
	{Source: "internal/workboard/collab.go", Name: "workboard_comments", Resource: "workboard", Class: WorkspaceOwned, ScopeKey: "workspace_id via workboard_tasks", CompositeUniqueness: true, Isolation: Scoped, IsolationTest: "internal/workboard/workspace_test.go"},
	{Source: "internal/workboard/runs.go", Name: "workboard_runs", Resource: "workboard", Class: WorkspaceOwned, ScopeKey: "workspace_id via workboard_tasks", CompositeUniqueness: true, Isolation: Scoped, IsolationTest: "internal/workboard/workspace_test.go"},
	{Source: "internal/workboard/store.go", Name: "workboard_tasks", Resource: "workboard", Class: WorkspaceOwned, ScopeKey: "workspace_id", CompositeUniqueness: true, Isolation: Scoped, IsolationTest: "internal/workboard/workspace_test.go"},
}

var Repositories = []Repository{
	{Source: "internal/agentmemory/store.go", Resource: "memory", Class: UserPrivate, ScopeKey: "workspace_id,user_id", Isolation: PersonalOnly, IsolationTest: "internal/ownership/catalog_test.go"},
	{Source: "internal/auth/apikeys/postgres.go", Resource: "api-keys", Class: UserPrivate, ScopeKey: "workspace_id,subject_id", Isolation: Scoped, IsolationTest: "internal/auth/apikeys/postgres_test.go"},
	{Source: "internal/auth/apikeys/store.go", Resource: "api-keys", Class: UserPrivate, ScopeKey: "workspace_id,user_id", Isolation: PersonalOnly, IsolationTest: "internal/ownership/catalog_test.go"},
	{Source: "internal/auth/jwt.go", Resource: "credentials", Class: UserPrivate, ScopeKey: "workspace_id,user_id", Isolation: PersonalOnly, IsolationTest: "internal/ownership/catalog_test.go"},
	{Source: "internal/auth/oidc_flow.go", Resource: "credentials", Class: Ephemeral, ScopeKey: "verified provider subject", Isolation: Scoped, IsolationTest: "internal/auth/oidc_flow_test.go"},
	{Source: "internal/costs/store.go", Resource: "costs", Class: WorkspaceOwned, ScopeKey: "workspace_id (column: workspace)", Isolation: Scoped, IsolationTest: "internal/costs/workspace_test.go"},
	{Source: "internal/credentials/rotation.go", Resource: "secrets", Class: WorkspaceOwned, ScopeKey: "workspace_id", Isolation: Scoped, IsolationTest: "internal/credentials/workspace_test.go"},
	{Source: "internal/credentials/vault.go", Resource: "secrets", Class: WorkspaceOwned, ScopeKey: "workspace_id", Isolation: Scoped, IsolationTest: "internal/credentials/workspace_test.go"},
	{Source: "internal/gateway/idempotency.go", Resource: "idempotency", Class: Ephemeral, ScopeKey: "workspace_id,method,route", Isolation: Scoped, IsolationTest: "internal/gateway/idempotency_test.go"},
	{Source: "internal/gateway/chat_attachments.go", Resource: "artifacts", Class: UserPrivate, ScopeKey: "workspace_id,user_id via session ownership", Isolation: Scoped, IsolationTest: "internal/session/resources_workspace_test.go"},
	{Source: "internal/knowledge/store.go", Resource: "knowledge", Class: WorkspaceOwned, ScopeKey: "workspace_id", Isolation: Scoped, IsolationTest: "internal/knowledge/workspace_test.go"},
	{Source: "internal/learning/store.go", Resource: "studio-learning", Class: WorkspaceOwned, ScopeKey: "workspace_id", Isolation: Scoped, IsolationTest: "internal/learning/workspace_test.go"},
	{Source: "internal/memory/sqlite.go", Resource: "memory", Class: UserPrivate, ScopeKey: "workspace_id,user_id", Isolation: Scoped, IsolationTest: "internal/memory/workspace_test.go"},
	{Source: "internal/memory/store.go", Resource: "memory", Class: UserPrivate, ScopeKey: "workspace_id,user_id", Isolation: Scoped, IsolationTest: "internal/memory/workspace_test.go"},
	{Source: "internal/memory/vector.go", Resource: "vectors", Class: WorkspaceOwned, ScopeKey: "workspace_id", Isolation: Scoped, IsolationTest: "internal/memory/vector_workspace_test.go"},
	{Source: "internal/plugins/loader.go", Resource: "plugins", Class: OrganizationOwned, ScopeKey: "organization_id", Isolation: PersonalOnly, IsolationTest: "internal/ownership/catalog_test.go"},
	{Source: "internal/pairing/pairing.go", Resource: "credentials", Class: Ephemeral, ScopeKey: "request principal", Isolation: Scoped, IsolationTest: "internal/pairing/pairing_test.go"},
	{Source: "internal/queue/dlq/dlq.go", Resource: "queue-dlq", Class: WorkspaceOwned, ScopeKey: "workspace_id", Isolation: PersonalOnly, IsolationTest: "internal/ownership/catalog_test.go"},
	{Source: "internal/rbac/store.go", Resource: "agents", Class: WorkspaceOwned, ScopeKey: "workspace_id", Isolation: Scoped, IsolationTest: "internal/rbac/workspace_test.go"},
	{Source: "internal/runtime/loader.go", Resource: "agents", Class: WorkspaceOwned, ScopeKey: "workspace_id", Isolation: Scoped, IsolationTest: "internal/runtime/loader_workspace_test.go"},
	{Source: "internal/runtime/checkpoint.go", Resource: "sessions", Class: UserPrivate, ScopeKey: "workspace_id,user_id", Isolation: PersonalOnly, IsolationTest: "internal/ownership/catalog_test.go"},
	{Source: "internal/runtime/engine.go", Resource: "sessions", Class: Ephemeral, ScopeKey: "verified principal", Isolation: Scoped, IsolationTest: "internal/runtime/principal_test.go"},
	{Source: "internal/runtime/engine_tool_queue.go", Resource: "queue-dlq", Class: Ephemeral, ScopeKey: "verified principal", Isolation: Scoped, IsolationTest: "internal/runtime/principal_test.go"},
	{Source: "internal/runtime/flowtrace.go", Resource: "studio-traces", Class: Ephemeral, ScopeKey: "verified principal", Isolation: Scoped, IsolationTest: "internal/runtime/principal_test.go"},
	{Source: "internal/session/history.go", Resource: "messages", Class: UserPrivate, ScopeKey: "workspace_id,user_id", Isolation: Scoped, IsolationTest: "internal/session/history_workspace_test.go"},
	{Source: "internal/session/ownership.go", Resource: "sessions", Class: UserPrivate, ScopeKey: "workspace_id,creator", Isolation: Scoped, IsolationTest: "internal/session/ownership_test.go"},
	{Source: "internal/session/store.go", Resource: "sessions", Class: UserPrivate, ScopeKey: "workspace_id,user_id via session ownership", Isolation: Scoped, IsolationTest: "internal/session/resources_workspace_test.go"},
	{Source: "internal/skills/loader.go", Resource: "skills", Class: WorkspaceOwned, ScopeKey: "workspace_id", Isolation: PersonalOnly, IsolationTest: "internal/ownership/catalog_test.go"},
	{Source: "internal/storage/postgres/postgres.go", Resource: "memory", Class: UserPrivate, ScopeKey: "workspace_id,user_id", Isolation: PersonalOnly, IsolationTest: "internal/ownership/catalog_test.go"},
	{Source: "internal/storage/sqlite/sqlite.go", Resource: "memory", Class: UserPrivate, ScopeKey: "workspace_id,user_id", Isolation: PersonalOnly, IsolationTest: "internal/ownership/catalog_test.go"},
	{Source: "internal/studio/deployrecord.go", Resource: "definitions", Class: WorkspaceOwned, ScopeKey: "workspace_id", Isolation: PersonalOnly, IsolationTest: "internal/ownership/catalog_test.go"},
	{Source: "internal/studio/lessons.go", Resource: "studio-learning", Class: WorkspaceOwned, ScopeKey: "workspace_id", Isolation: Scoped, IsolationTest: "internal/gateway/studio_scope_test.go"},
	{Source: "internal/studio/library.go", Resource: "studio-drafts", Class: UserPrivate, ScopeKey: "workspace_id,user_id", Isolation: Scoped, IsolationTest: "internal/gateway/studio_scope_test.go"},
	{Source: "internal/studio/macros.go", Resource: "studio-learning", Class: WorkspaceOwned, ScopeKey: "workspace_id", Isolation: Scoped, IsolationTest: "internal/studio/observers_workspace_test.go"},
	{Source: "internal/studio/preferences.go", Resource: "studio-learning", Class: UserPrivate, ScopeKey: "workspace_id,user_id", Isolation: Scoped, IsolationTest: "internal/gateway/studio_scope_test.go"},
	{Source: "internal/studio/rulesstore.go", Resource: "studio-learning", Class: WorkspaceOwned, ScopeKey: "workspace_id", Isolation: Scoped, IsolationTest: "internal/gateway/studio_scope_test.go"},
	{Source: "internal/studio/strategyfit.go", Resource: "studio-learning", Class: WorkspaceOwned, ScopeKey: "workspace_id", Isolation: Scoped, IsolationTest: "internal/studio/observers_workspace_test.go"},
	{Source: "internal/studio/trace.go", Resource: "studio-traces", Class: WorkspaceOwned, ScopeKey: "workspace_id", Isolation: Scoped, IsolationTest: "internal/studio/trace_workspace_test.go"},
	{Source: "internal/tenancy/postgres.go", Resource: "tenancy", Class: PlatformGlobal, ScopeKey: "tenant foreign keys", Isolation: Scoped, IsolationTest: "internal/tenancy/postgres_test.go"},
	{Source: "internal/vector/qdrant/qdrant.go", Resource: "vectors", Class: WorkspaceOwned, ScopeKey: "workspace_id payload pre-filter", Isolation: Scoped, IsolationTest: "internal/vector/qdrant/workspace_test.go"},
	{Source: "internal/vector/sqlitevec/sqlitevec.go", Resource: "vectors", Class: WorkspaceOwned, ScopeKey: "workspace_id", Isolation: Scoped, IsolationTest: "internal/memory/vector_workspace_test.go"},
	{Source: "internal/workboard/store.go", Resource: "workboard", Class: WorkspaceOwned, ScopeKey: "workspace_id", Isolation: Scoped, IsolationTest: "internal/workboard/workspace_test.go"},
}

func ValidateCatalog() error {
	resources := make(map[string]Resource, len(Resources))
	for _, resource := range Resources {
		if strings.TrimSpace(resource.Name) == "" || !validClass(resource.Class) {
			return fmt.Errorf("invalid ownership resource %#v", resource)
		}
		if _, duplicate := resources[resource.Name]; duplicate {
			return fmt.Errorf("duplicate ownership resource %q", resource.Name)
		}
		if resource.Retention == "" || resource.Export == "" || resource.Deletion == "" || resource.Backup == "" {
			return fmt.Errorf("resource %q has an incomplete lifecycle contract", resource.Name)
		}
		resources[resource.Name] = resource
	}
	seenTables := map[string]bool{}
	for _, table := range Tables {
		key := table.Source + ":" + table.Name
		if seenTables[key] {
			return fmt.Errorf("duplicate durable table %q", key)
		}
		seenTables[key] = true
		if _, ok := resources[table.Resource]; !ok {
			return fmt.Errorf("table %q references unknown resource %q", key, table.Resource)
		}
		if !validClass(table.Class) || table.IsolationTest == "" {
			return fmt.Errorf("table %q has incomplete ownership or isolation-test metadata", key)
		}
		if (table.Class == WorkspaceOwned || table.Class == UserPrivate) && (!strings.Contains(table.ScopeKey, "workspace_id") || !table.CompositeUniqueness) {
			return fmt.Errorf("tenant table %q must require workspace_id and composite uniqueness", key)
		}
		if table.Isolation != Scoped && table.Isolation != PersonalOnly {
			return fmt.Errorf("table %q has invalid isolation state %q", key, table.Isolation)
		}
	}
	seenRepositories := map[string]bool{}
	for _, repository := range Repositories {
		if seenRepositories[repository.Source] {
			return fmt.Errorf("duplicate durable repository %q", repository.Source)
		}
		seenRepositories[repository.Source] = true
		if _, ok := resources[repository.Resource]; !ok {
			return fmt.Errorf("repository %q references unknown resource %q", repository.Source, repository.Resource)
		}
		if !validClass(repository.Class) || repository.IsolationTest == "" {
			return fmt.Errorf("repository %q has incomplete ownership or isolation-test metadata", repository.Source)
		}
		if (repository.Class == WorkspaceOwned || repository.Class == UserPrivate) && !strings.Contains(repository.ScopeKey, "workspace_id") {
			return fmt.Errorf("tenant repository %q must require workspace_id", repository.Source)
		}
		if repository.Isolation != Scoped && repository.Isolation != PersonalOnly {
			return fmt.Errorf("repository %q has invalid isolation state %q", repository.Source, repository.Isolation)
		}
	}
	return nil
}

func MultiUserBlockers() []string {
	var blockers []string
	for _, table := range Tables {
		if table.Isolation == PersonalOnly && table.Class != PlatformGlobal && table.Class != Ephemeral {
			blockers = append(blockers, table.Source+":"+table.Name)
		}
	}
	for _, repository := range Repositories {
		if repository.Isolation == PersonalOnly && repository.Class != PlatformGlobal && repository.Class != Ephemeral {
			blockers = append(blockers, repository.Source)
		}
	}
	sort.Strings(blockers)
	return blockers
}

func validClass(class Class) bool {
	switch class {
	case PlatformGlobal, OrganizationOwned, WorkspaceOwned, UserPrivate, Ephemeral:
		return true
	default:
		return false
	}
}
