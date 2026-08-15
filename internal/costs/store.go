// Package costs tracks token usage per agent and session.
package costs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/soulacy/soulacy/internal/sqlitex"
	"github.com/soulacy/soulacy/internal/wsroot"
)

// UsageRecord records one LLM call's token consumption.
type UsageRecord struct {
	Subject             string    `json:"subject"`
	Workspace           string    `json:"workspace"`
	AgentID             string    `json:"agent_id"`
	SessionID           string    `json:"session_id"`
	RunID               string    `json:"run_id"`
	CallID              string    `json:"call_id"`
	Source              string    `json:"source"`
	Trigger             string    `json:"trigger"`
	Provider            string    `json:"provider"`
	Model               string    `json:"model"`
	PromptTokens        int       `json:"prompt_tokens"`
	CompTokens          int       `json:"completion_tokens"`
	TotalTokens         int       `json:"total_tokens"`
	CacheCreationTokens int       `json:"cache_creation_tokens"`
	CacheReadTokens     int       `json:"cache_read_tokens"`
	ReasoningTokens     int       `json:"reasoning_tokens"`
	ToolUsePromptTokens int       `json:"tool_use_prompt_tokens"`
	CostUSD             float64   `json:"cost_usd"` // estimated; 0 if pricing not configured
	CostMicros          int64     `json:"cost_micros"`
	PricingStatus       string    `json:"pricing_status"`
	PricingVersion      string    `json:"pricing_version"`
	ProviderRequestID   string    `json:"provider_request_id"`
	ProviderRequestIDs  []string  `json:"provider_request_ids,omitempty"`
	AttemptCount        int       `json:"attempt_count"`
	Status              string    `json:"status"`
	ErrorCode           string    `json:"error_code"`
	CreatedAt           time.Time `json:"created_at"`
}

// AgentCost is the aggregated cost summary for one agent.
type AgentCost struct {
	AgentID      string  `json:"agent_id"`
	TotalTokens  int     `json:"total_tokens"`
	PromptTokens int     `json:"prompt_tokens"`
	CompTokens   int     `json:"comp_tokens"`
	CostUSD      float64 `json:"cost_usd"`
}

// SessionCost is the aggregated cost summary for one session.
type SessionCost struct {
	SessionID   string  `json:"session_id"`
	TotalTokens int     `json:"total_tokens"`
	CostUSD     float64 `json:"cost_usd"`
}

type ChargebackRow struct {
	Subject     string  `json:"subject,omitempty"`
	Source      string  `json:"source,omitempty"`
	Provider    string  `json:"provider,omitempty"`
	Model       string  `json:"model,omitempty"`
	Calls       int     `json:"calls"`
	Attempts    int     `json:"attempts"`
	TotalTokens int64   `json:"total_tokens"`
	CostMicros  int64   `json:"cost_micros"`
	CostUSD     float64 `json:"cost_usd"`
}

// UsageStats summarizes ledger completeness for operational dashboards.
type UsageStats struct {
	Calls           int   `json:"calls"`
	AttributedCalls int   `json:"attributed_calls"`
	RejectedCalls   int   `json:"rejected_calls"`
	Attempts        int64 `json:"attempts"`
	UnknownPriced   int   `json:"unknown_priced_calls"`
	FailedCalls     int   `json:"failed_calls"`
	TotalTokens     int64 `json:"total_tokens"`
	CostMicros      int64 `json:"cost_micros"`
}

// Reconciliation compares Soulacy's estimate with a provider billing export.
type Reconciliation struct {
	Provider        string    `json:"provider"`
	PeriodStart     time.Time `json:"period_start"`
	PeriodEnd       time.Time `json:"period_end"`
	EstimatedMicros int64     `json:"estimated_micros"`
	ActualMicros    int64     `json:"actual_micros"`
	VarianceMicros  int64     `json:"variance_micros"`
	Source          string    `json:"source"`
	ImportedAt      time.Time `json:"imported_at"`
}

// VarianceRatio returns absolute estimate-vs-actual variance. When the local
// estimate is zero, any non-zero provider charge is a full (100%) variance.
func (r Reconciliation) VarianceRatio() float64 {
	denominator := r.EstimatedMicros
	if r.ActualMicros > denominator {
		denominator = r.ActualMicros
	}
	if denominator <= 0 {
		return 0
	}
	variance := r.VarianceMicros
	if variance < 0 {
		variance = -variance
	}
	return float64(variance) / float64(denominator)
}

// ReservationPolicy is evaluated in the same SQLite transaction that inserts
// the reservation, preventing concurrent gateway processes from all observing
// the same remaining budget.
type ReservationPolicy struct {
	Now                      time.Time
	DailyStart               time.Time
	MonthlyStart             time.Time
	TokenWindowStart         time.Time
	ProviderTokenWindowStart time.Time
	GlobalDailyMicros        int64
	GlobalMonthlyMicros      int64
	UserDailyMicros          int64
	AgentDailyMicros         int64
	UserTokenLimit           int64
	AgentTokenLimit          int64
	ProviderTokenLimit       int64
}

type ReservationCapacity struct {
	AvailableMicros int64
	AvailableTokens int64
}

// ReservationRejectedError reports the tightest remaining capacity observed
// atomically. Callers may safely clamp and retry; the second transaction will
// recheck all concurrent reservations.
type ReservationRejectedError struct {
	Capacity ReservationCapacity
	Reason   string
	Scope    string
	ResetAt  time.Time
}

func (e *ReservationRejectedError) Error() string { return e.Reason }

// Store persists token usage records to SQLite.
type Store struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS token_usage (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    agent_id      TEXT NOT NULL,
    session_id    TEXT NOT NULL,
    provider      TEXT NOT NULL,
    model         TEXT NOT NULL,
    prompt_tokens INTEGER NOT NULL DEFAULT 0,
    comp_tokens   INTEGER NOT NULL DEFAULT 0,
    total_tokens  INTEGER NOT NULL DEFAULT 0,
    cost_usd      REAL NOT NULL DEFAULT 0,
    created_at    DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_usage_agent   ON token_usage(agent_id);
CREATE INDEX IF NOT EXISTS idx_usage_session ON token_usage(session_id);
CREATE INDEX IF NOT EXISTS idx_usage_created ON token_usage(created_at);
`

const usageSchemaV2 = `
ALTER TABLE token_usage ADD COLUMN subject TEXT NOT NULL DEFAULT '';
ALTER TABLE token_usage ADD COLUMN workspace TEXT NOT NULL DEFAULT '';
ALTER TABLE token_usage ADD COLUMN run_id TEXT NOT NULL DEFAULT '';
ALTER TABLE token_usage ADD COLUMN call_id TEXT NOT NULL DEFAULT '';
ALTER TABLE token_usage ADD COLUMN source TEXT NOT NULL DEFAULT '';
ALTER TABLE token_usage ADD COLUMN trigger_name TEXT NOT NULL DEFAULT '';
ALTER TABLE token_usage ADD COLUMN cache_creation_tokens INTEGER NOT NULL DEFAULT 0;
ALTER TABLE token_usage ADD COLUMN cache_read_tokens INTEGER NOT NULL DEFAULT 0;
ALTER TABLE token_usage ADD COLUMN reasoning_tokens INTEGER NOT NULL DEFAULT 0;
ALTER TABLE token_usage ADD COLUMN tool_use_prompt_tokens INTEGER NOT NULL DEFAULT 0;
ALTER TABLE token_usage ADD COLUMN cost_micros INTEGER NOT NULL DEFAULT 0;
ALTER TABLE token_usage ADD COLUMN pricing_status TEXT NOT NULL DEFAULT 'unknown';
ALTER TABLE token_usage ADD COLUMN pricing_version TEXT NOT NULL DEFAULT '';
ALTER TABLE token_usage ADD COLUMN provider_request_id TEXT NOT NULL DEFAULT '';
ALTER TABLE token_usage ADD COLUMN status TEXT NOT NULL DEFAULT 'success';
ALTER TABLE token_usage ADD COLUMN error_code TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_usage_subject ON token_usage(subject);
CREATE INDEX IF NOT EXISTS idx_usage_run ON token_usage(run_id);
CREATE INDEX IF NOT EXISTS idx_usage_source ON token_usage(source);
CREATE UNIQUE INDEX IF NOT EXISTS idx_usage_call_unique ON token_usage(call_id) WHERE call_id <> '';
CREATE TABLE IF NOT EXISTS cost_reservations (
    id             TEXT PRIMARY KEY,
	 subject        TEXT NOT NULL DEFAULT '',
	 agent_id       TEXT NOT NULL DEFAULT '',
    estimated_micros INTEGER NOT NULL DEFAULT 0,
    estimated_tokens INTEGER NOT NULL DEFAULT 0,
    created_at     DATETIME NOT NULL,
    expires_at     DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_cost_reservation_expiry ON cost_reservations(expires_at);
CREATE TABLE IF NOT EXISTS cost_reconciliations (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    provider TEXT NOT NULL,
    period_start DATETIME NOT NULL,
    period_end DATETIME NOT NULL,
    estimated_micros INTEGER NOT NULL,
    actual_micros INTEGER NOT NULL,
    source TEXT NOT NULL DEFAULT '',
    imported_at DATETIME NOT NULL,
    UNIQUE(provider, period_start, period_end)
);
`

const usageSchemaV3 = `
ALTER TABLE token_usage ADD COLUMN provider_request_ids_json TEXT NOT NULL DEFAULT '[]';
ALTER TABLE token_usage ADD COLUMN attempt_count INTEGER NOT NULL DEFAULT 1;
`

const usageSchemaV4 = `
ALTER TABLE cost_reservations ADD COLUMN provider TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_cost_reservation_provider ON cost_reservations(provider);
`

// usageSchemaV5 brings the tenant boundary to accounting.
//
// token_usage already had a `workspace` column and Record already wrote it —
// it was simply never used as a predicate. cost_reservations had no workspace
// at all, which is the half that matters: a reservation is in-flight spend
// counted against a ceiling, so without it one tenant's outstanding
// reservations reduce another tenant's available budget. That is a denial of
// service as well as a leak.
const usageSchemaV5 = `
ALTER TABLE cost_reservations ADD COLUMN workspace TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_usage_workspace ON token_usage(workspace, created_at);
CREATE INDEX IF NOT EXISTS idx_usage_workspace_agent ON token_usage(workspace, agent_id, created_at);
CREATE INDEX IF NOT EXISTS idx_usage_workspace_subject ON token_usage(workspace, subject, created_at);
CREATE INDEX IF NOT EXISTS idx_cost_reservation_workspace ON cost_reservations(workspace);
`

// NewStore opens (or creates) the costs SQLite database at path.
func NewStore(path string) (*Store, error) {
	db, err := sqlitex.Open(path, sqlitex.DefaultOptions())
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}

	// Schema versioning (E22 adoption): v1 = the idempotent bootstrap above;
	// future changes go through sqlitex.MigrateSchema with v2+.
	if err := sqlitex.RecordSchemaVersion(db, "costs", 1); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := sqlitex.MigrateSchema(db, "costs", []sqlitex.SchemaMigration{
		{Version: 2, SQL: usageSchemaV2}, {Version: 3, SQL: usageSchemaV3}, {Version: 4, SQL: usageSchemaV4},
		{Version: 5, SQL: usageSchemaV5},
	}); err != nil {
		db.Close()
		return nil, err
	}
	if err := backfillWorkspace(db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// backfillWorkspace assigns pre-tenant accounting rows to the personal
// workspace, which is what a single-user installation's spend was.
//
// It runs on every open rather than only inside the versioned migration. A row
// with an empty workspace matches no scoped query, so it is not a leak — it is
// spend that silently stopped counting against any budget, which is worse:
// the ceiling would quietly stop being enforced. Reachable by an interrupted
// migration or a direct write.
func backfillWorkspace(db *sql.DB) error {
	for _, table := range []string{"token_usage", "cost_reservations"} {
		if _, err := db.Exec(
			`UPDATE `+table+` SET workspace = ? WHERE workspace IS NULL OR workspace = ''`,
			wsroot.PersonalWorkspaceID); err != nil {
			return fmt.Errorf("costs: backfill %s workspace: %w", table, err)
		}
	}
	return nil
}

// ErrWorkspaceRequired is returned when accounting is attempted with no
// tenant. Spend with no owner is spend charged to everyone's budget.
var ErrWorkspaceRequired = errors.New("costs: workspace is required")

// requireWorkspace normalizes a tenant and refuses an absent one.
func requireWorkspace(workspaceID string) (string, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		return "", ErrWorkspaceRequired
	}
	return wsroot.Normalize(workspaceID), nil
}

// Record appends a usage record.
func (s *Store) Record(ctx context.Context, r UsageRecord) error {
	// Spend with no owner is spend that counts against no budget, so a record
	// without a workspace is refused rather than filed under the empty one.
	workspace, err := requireWorkspace(r.Workspace)
	if err != nil {
		return err
	}
	r.Workspace = workspace
	createdAt := r.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	requestIDs, _ := json.Marshal(r.ProviderRequestIDs)
	if r.AttemptCount <= 0 && r.Status != "rejected" {
		r.AttemptCount = 1
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO token_usage
		    (subject, workspace, agent_id, session_id, run_id, call_id, source, trigger_name,
		     provider, model, prompt_tokens, comp_tokens, total_tokens,
		     cache_creation_tokens, cache_read_tokens, reasoning_tokens, tool_use_prompt_tokens,
		     cost_usd, cost_micros, pricing_status, pricing_version, provider_request_id,
		     provider_request_ids_json, attempt_count, status, error_code, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.Subject, r.Workspace, r.AgentID, r.SessionID, r.RunID, r.CallID, r.Source, r.Trigger,
		r.Provider, r.Model, r.PromptTokens, r.CompTokens, r.TotalTokens,
		r.CacheCreationTokens, r.CacheReadTokens, r.ReasoningTokens, r.ToolUsePromptTokens,
		r.CostUSD, r.CostMicros, r.PricingStatus, r.PricingVersion, r.ProviderRequestID,
		string(requestIDs), r.AttemptCount, r.Status, r.ErrorCode,
		createdAt.UTC().Format("2006-01-02 15:04:05"),
	)
	return err
}

// SumCostMicrosSince returns durable recorded spend since the supplied time.
func (s *Store) SumCostMicrosSince(ctx context.Context, workspaceID string, since time.Time) (int64, error) {
	workspaceID, err := requireWorkspace(workspaceID)
	if err != nil {
		return 0, err
	}
	var total int64
	err = s.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(cost_micros), 0) FROM token_usage WHERE workspace = ? AND created_at >= ?`,
		workspaceID, since.UTC().Format("2006-01-02 15:04:05")).Scan(&total)
	return total, err
}

func (s *Store) SumCostMicrosScope(ctx context.Context, workspaceID string, since time.Time, subject, agentID string) (int64, error) {
	workspaceID, err := requireWorkspace(workspaceID)
	if err != nil {
		return 0, err
	}
	query := `SELECT COALESCE(SUM(cost_micros), 0) FROM token_usage WHERE workspace = ? AND created_at >= ?`
	args := []any{workspaceID, since.UTC().Format("2006-01-02 15:04:05")}
	if subject != "" {
		query += ` AND subject = ?`
		args = append(args, subject)
	}
	if agentID != "" {
		query += ` AND agent_id = ?`
		args = append(args, agentID)
	}
	var total int64
	err = s.db.QueryRowContext(ctx, query, args...).Scan(&total)
	return total, err
}

// StatsSince reports accounting coverage and outcomes since a boundary.
func (s *Store) StatsSince(ctx context.Context, workspaceID string, since time.Time) (UsageStats, error) {
	workspaceID, err := requireWorkspace(workspaceID)
	if err != nil {
		return UsageStats{}, err
	}
	var stats UsageStats
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*),
		COALESCE(SUM(CASE WHEN call_id <> '' THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN status = 'rejected' THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(attempt_count), 0),
		COALESCE(SUM(CASE WHEN pricing_status = 'unknown' THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN status <> 'success' THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(total_tokens), 0), COALESCE(SUM(cost_micros), 0)
		FROM token_usage WHERE workspace = ? AND created_at >= ?`,
		workspaceID, since.UTC().Format("2006-01-02 15:04:05")).
		Scan(&stats.Calls, &stats.AttributedCalls, &stats.RejectedCalls, &stats.Attempts,
			&stats.UnknownPriced, &stats.FailedCalls, &stats.TotalTokens, &stats.CostMicros)
	return stats, err
}

// TotalsBySource returns cumulative usage for a feature surface. It is used by
// bounded multi-call operations such as Studio's repair loop.
func (s *Store) TotalsBySource(ctx context.Context, workspaceID, source string) (UsageRecord, error) {
	workspaceID, err := requireWorkspace(workspaceID)
	if err != nil {
		return UsageRecord{}, err
	}
	var out UsageRecord
	err = s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(prompt_tokens), 0),
		COALESCE(SUM(comp_tokens), 0), COALESCE(SUM(total_tokens), 0),
		COALESCE(SUM(cost_usd), 0), COALESCE(SUM(cost_micros), 0)
		FROM token_usage WHERE workspace = ? AND source = ?`, workspaceID, source).
		Scan(&out.PromptTokens, &out.CompTokens, &out.TotalTokens, &out.CostUSD, &out.CostMicros)
	return out, err
}

// TotalsByRun isolates bounded multi-call operations from concurrent work on
// the same feature surface.
func (s *Store) TotalsByRun(ctx context.Context, workspaceID, runID string) (UsageRecord, error) {
	workspaceID, err := requireWorkspace(workspaceID)
	if err != nil {
		return UsageRecord{}, err
	}
	var out UsageRecord
	err = s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(prompt_tokens), 0),
		COALESCE(SUM(comp_tokens), 0), COALESCE(SUM(total_tokens), 0),
		COALESCE(SUM(cost_usd), 0), COALESCE(SUM(cost_micros), 0)
		FROM token_usage WHERE workspace = ? AND run_id = ?`, workspaceID, runID).
		Scan(&out.PromptTokens, &out.CompTokens, &out.TotalTokens, &out.CostUSD, &out.CostMicros)
	return out, err
}

// ListUsage returns recent prompt-free accounting records for operations and
// reconciliation. The bounded limit prevents accidental unbounded exports.
func (s *Store) ListUsage(ctx context.Context, workspaceID string, since time.Time, limit int) ([]UsageRecord, error) {
	workspaceID, err := requireWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, `SELECT subject, workspace, agent_id, session_id,
		run_id, call_id, source, trigger_name, provider, model, prompt_tokens,
		comp_tokens, total_tokens, cache_creation_tokens, cache_read_tokens,
		reasoning_tokens, tool_use_prompt_tokens, cost_usd, cost_micros,
		pricing_status, pricing_version, provider_request_id, provider_request_ids_json,
		attempt_count, status, error_code, created_at
		FROM token_usage WHERE workspace = ? AND created_at >= ? ORDER BY created_at DESC, id DESC LIMIT ?`,
		workspaceID, since.UTC().Format("2006-01-02 15:04:05"), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]UsageRecord, 0)
	for rows.Next() {
		var record UsageRecord
		var requestIDs string
		if err := rows.Scan(&record.Subject, &record.Workspace, &record.AgentID, &record.SessionID,
			&record.RunID, &record.CallID, &record.Source, &record.Trigger, &record.Provider, &record.Model,
			&record.PromptTokens, &record.CompTokens, &record.TotalTokens, &record.CacheCreationTokens,
			&record.CacheReadTokens, &record.ReasoningTokens, &record.ToolUsePromptTokens, &record.CostUSD,
			&record.CostMicros, &record.PricingStatus, &record.PricingVersion, &record.ProviderRequestID,
			&requestIDs, &record.AttemptCount,
			&record.Status, &record.ErrorCode, &record.CreatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(requestIDs), &record.ProviderRequestIDs)
		out = append(out, record)
	}
	return out, rows.Err()
}

// ReconcileProvider upserts one provider billing period and calculates the
// variance from the detailed local ledger.
// ReconcileProvider is deliberately deployment-wide, and cost_reconciliations
// is classified PlatformGlobal rather than workspace-owned.
//
// A reconciliation compares our estimate against the *provider's invoice*, and
// providers bill the deployment, not the tenant. There is no honest way to
// split one invoice across workspaces here: any split would be a number this
// store invented. Scoping it would therefore not add isolation, it would add
// fiction. Per-tenant attribution is what Chargeback is for, and that one is
// workspace-scoped.
func (s *Store) ReconcileProvider(ctx context.Context, provider string, start, end time.Time, actualMicros int64, source string) (Reconciliation, error) {
	var estimated int64
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(cost_micros), 0) FROM token_usage
		WHERE provider = ? AND created_at >= ? AND created_at < ?`, provider,
		start.UTC().Format("2006-01-02 15:04:05"), end.UTC().Format("2006-01-02 15:04:05")).Scan(&estimated)
	if err != nil {
		return Reconciliation{}, err
	}
	imported := time.Now().UTC()
	_, err = s.db.ExecContext(ctx, `INSERT INTO cost_reconciliations
		(provider, period_start, period_end, estimated_micros, actual_micros, source, imported_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(provider, period_start, period_end) DO UPDATE SET
		estimated_micros=excluded.estimated_micros, actual_micros=excluded.actual_micros,
		source=excluded.source, imported_at=excluded.imported_at`, provider,
		start.UTC().Format("2006-01-02 15:04:05"), end.UTC().Format("2006-01-02 15:04:05"),
		estimated, actualMicros, source, imported.Format("2006-01-02 15:04:05"))
	if err != nil {
		return Reconciliation{}, err
	}
	return Reconciliation{Provider: provider, PeriodStart: start.UTC(), PeriodEnd: end.UTC(),
		EstimatedMicros: estimated, ActualMicros: actualMicros,
		VarianceMicros: actualMicros - estimated, Source: source, ImportedAt: imported}, nil
}

func (s *Store) ListReconciliations(ctx context.Context, limit int) ([]Reconciliation, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT provider, period_start, period_end,
		estimated_micros, actual_micros, source, imported_at
		FROM cost_reconciliations ORDER BY period_end DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Reconciliation, 0)
	for rows.Next() {
		var item Reconciliation
		if err := rows.Scan(&item.Provider, &item.PeriodStart, &item.PeriodEnd,
			&item.EstimatedMicros, &item.ActualMicros, &item.Source, &item.ImportedAt); err != nil {
			return nil, err
		}
		item.VarianceMicros = item.ActualMicros - item.EstimatedMicros
		out = append(out, item)
	}
	return out, rows.Err()
}

// Reserve records in-flight worst-case spend. The caller serializes the
// check-and-reserve decision; the durable row prevents a restart from losing
// visibility into outstanding reservations.
func (s *Store) Reserve(ctx context.Context, workspaceID, id, subject, agentID, provider string, costMicros int64, tokens int, expiresAt time.Time) error {
	workspaceID, err := requireWorkspace(workspaceID)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO cost_reservations
		(id, workspace, subject, agent_id, provider, estimated_micros, estimated_tokens, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, workspaceID, subject, agentID, provider, costMicros, tokens, time.Now().UTC().Format("2006-01-02 15:04:05"),
		expiresAt.UTC().Format("2006-01-02 15:04:05"))
	return err
}

// TryReserve atomically checks every configured scope and inserts an in-flight
// reservation. A write is performed first so SQLite serializes competing
// admissions before any capacity reads occur.
func (s *Store) TryReserve(ctx context.Context, workspaceID, id, subject, agentID, provider string, costMicros int64, tokens int, expiresAt time.Time, policy ReservationPolicy) error {
	workspaceID, err := requireWorkspace(workspaceID)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := policy.Now.UTC()
	if _, err := tx.ExecContext(ctx, `DELETE FROM cost_reservations WHERE expires_at <= ?`, now.Format("2006-01-02 15:04:05")); err != nil {
		return err
	}
	capacity := ReservationCapacity{AvailableMicros: int64(^uint64(0) >> 1), AvailableTokens: int64(^uint64(0) >> 1)}
	limitedCost, limitedTokens := false, false
	costScopes := []struct {
		limit    int64
		since    time.Time
		subject  string
		agentID  string
		required bool
		label    string
		resetAt  time.Time
	}{
		// The "global" scopes are global *within one workspace*. Every scope
		// query below carries the tenant predicate, so a configured ceiling
		// applies per tenant rather than to the deployment as a whole. That is
		// the point: a shared ceiling means the busiest tenant starves the
		// rest, and one tenant's spend becomes an observable signal to another.
		// In Personal there is exactly one workspace, so the numbers are
		// identical to what they were.
		{policy.GlobalDailyMicros, policy.DailyStart, "", "", false, "global_daily", policy.DailyStart.AddDate(0, 0, 1)},
		{policy.GlobalMonthlyMicros, policy.MonthlyStart, "", "", false, "global_monthly", policy.MonthlyStart.AddDate(0, 1, 0)},
		{policy.UserDailyMicros, policy.DailyStart, subject, "", true, "user_daily", policy.DailyStart.AddDate(0, 0, 1)},
		{policy.AgentDailyMicros, policy.DailyStart, "", agentID, true, "agent_daily", policy.DailyStart.AddDate(0, 0, 1)},
	}
	tightCostScope := ""
	var tightCostReset time.Time
	for _, scope := range costScopes {
		if scope.limit <= 0 || (scope.required && scope.subject == "" && scope.agentID == "") {
			continue
		}
		used, reserved, err := txCostScope(ctx, tx, workspaceID, scope.since, scope.subject, scope.agentID)
		if err != nil {
			return err
		}
		remaining := scope.limit - used - reserved
		if !limitedCost || remaining < capacity.AvailableMicros {
			capacity.AvailableMicros = remaining
			tightCostScope, tightCostReset = scope.label, scope.resetAt
		}
		limitedCost = true
	}
	tokenScopes := []struct {
		limit    int64
		since    time.Time
		subject  string
		agentID  string
		provider string
		label    string
		resetAt  time.Time
	}{
		{policy.UserTokenLimit, policy.TokenWindowStart, subject, "", "", "user_tokens_24h", policy.Now.Add(24 * time.Hour)},
		{policy.AgentTokenLimit, policy.TokenWindowStart, "", agentID, "", "agent_tokens_24h", policy.Now.Add(24 * time.Hour)},
		{policy.ProviderTokenLimit, policy.ProviderTokenWindowStart, "", "", provider, "provider_tokens_1m", policy.Now.Add(time.Minute)},
	}
	tightTokenScope := ""
	var tightTokenReset time.Time
	for _, scope := range tokenScopes {
		if scope.limit <= 0 || (scope.subject == "" && scope.agentID == "" && scope.provider == "") {
			continue
		}
		used, reserved, err := txTokenScope(ctx, tx, workspaceID, scope.since, scope.subject, scope.agentID, scope.provider)
		if err != nil {
			return err
		}
		remaining := scope.limit - used - reserved
		if !limitedTokens || remaining < capacity.AvailableTokens {
			capacity.AvailableTokens = remaining
			tightTokenScope = scope.label
			tightTokenReset = scope.resetAt
		}
		limitedTokens = true
	}
	if limitedCost && costMicros > capacity.AvailableMicros {
		return &ReservationRejectedError{Capacity: capacity, Scope: tightCostScope, ResetAt: tightCostReset,
			Reason: fmt.Sprintf("%s dollar budget exhausted: %d micro-dollars available; resets at %s", tightCostScope, capacity.AvailableMicros, tightCostReset.Format(time.RFC3339))}
	}
	if limitedTokens && int64(tokens) > capacity.AvailableTokens {
		return &ReservationRejectedError{Capacity: capacity, Scope: tightTokenScope, ResetAt: tightTokenReset,
			Reason: fmt.Sprintf("%s budget exhausted: %d tokens available; rolling window frees capacity by %s", tightTokenScope, capacity.AvailableTokens, tightTokenReset.Format(time.RFC3339))}
	}
	// The workspace is written here as well as in Reserve. A reservation row
	// with an empty workspace matches no scoped capacity read, so it would be
	// invisible to the very ceiling it is supposed to consume — the budget
	// would silently stop being enforced rather than visibly fail.
	if _, err := tx.ExecContext(ctx, `INSERT INTO cost_reservations
		(id, workspace, subject, agent_id, provider, estimated_micros, estimated_tokens, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, workspaceID, subject, agentID, provider, costMicros, tokens, now.Format("2006-01-02 15:04:05"),
		expiresAt.UTC().Format("2006-01-02 15:04:05")); err != nil {
		return err
	}
	return tx.Commit()
}

// txCostScope and txTokenScope sum recorded spend and in-flight reservations
// for one budget scope. The workspace predicate is not optional the way
// subject and agent are: those narrow a ceiling within a tenant, while the
// workspace *is* the tenant boundary, and omitting it would let one tenant's
// reservations consume another's headroom.
func txCostScope(ctx context.Context, tx *sql.Tx, workspaceID string, since time.Time, subject, agentID string) (int64, int64, error) {
	usageQuery := `SELECT COALESCE(SUM(cost_micros), 0) FROM token_usage WHERE workspace = ? AND created_at >= ?`
	reserveQuery := `SELECT COALESCE(SUM(estimated_micros), 0) FROM cost_reservations WHERE workspace = ?`
	usageArgs := []any{workspaceID, since.UTC().Format("2006-01-02 15:04:05")}
	reserveArgs := []any{workspaceID}
	if subject != "" {
		usageQuery += ` AND subject = ?`
		reserveQuery += ` AND subject = ?`
		usageArgs, reserveArgs = append(usageArgs, subject), append(reserveArgs, subject)
	}
	if agentID != "" {
		usageQuery += ` AND agent_id = ?`
		reserveQuery += ` AND agent_id = ?`
		usageArgs, reserveArgs = append(usageArgs, agentID), append(reserveArgs, agentID)
	}
	var used, reserved int64
	if err := tx.QueryRowContext(ctx, usageQuery, usageArgs...).Scan(&used); err != nil {
		return 0, 0, err
	}
	if err := tx.QueryRowContext(ctx, reserveQuery, reserveArgs...).Scan(&reserved); err != nil {
		return 0, 0, err
	}
	return used, reserved, nil
}

func txTokenScope(ctx context.Context, tx *sql.Tx, workspaceID string, since time.Time, subject, agentID, provider string) (int64, int64, error) {
	usageQuery := `SELECT COALESCE(SUM(total_tokens), 0) FROM token_usage WHERE workspace = ? AND created_at >= ?`
	reserveQuery := `SELECT COALESCE(SUM(estimated_tokens), 0) FROM cost_reservations WHERE workspace = ?`
	usageArgs := []any{workspaceID, since.UTC().Format("2006-01-02 15:04:05")}
	reserveArgs := []any{workspaceID}
	if subject != "" {
		usageQuery += ` AND subject = ?`
		reserveQuery += ` AND subject = ?`
		usageArgs, reserveArgs = append(usageArgs, subject), append(reserveArgs, subject)
	}
	if agentID != "" {
		usageQuery += ` AND agent_id = ?`
		reserveQuery += ` AND agent_id = ?`
		usageArgs, reserveArgs = append(usageArgs, agentID), append(reserveArgs, agentID)
	}
	if provider != "" {
		usageQuery += ` AND provider = ?`
		reserveQuery += ` AND provider = ?`
		usageArgs, reserveArgs = append(usageArgs, provider), append(reserveArgs, provider)
	}
	var used, reserved int64
	if err := tx.QueryRowContext(ctx, usageQuery, usageArgs...).Scan(&used); err != nil {
		return 0, 0, err
	}
	if err := tx.QueryRowContext(ctx, reserveQuery, reserveArgs...).Scan(&reserved); err != nil {
		return 0, 0, err
	}
	return used, reserved, nil
}

// Release removes an in-flight reservation after completion or failure.
func (s *Store) Release(ctx context.Context, workspaceID, id string) error {
	workspaceID, err := requireWorkspace(workspaceID)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx,
		`DELETE FROM cost_reservations WHERE workspace = ? AND id = ?`, workspaceID, id)
	return err
}

// ReservedCostMicros returns live reservations and removes expired entries.
func (s *Store) ReservedCostMicros(ctx context.Context, workspaceID string, now time.Time) (int64, error) {
	workspaceID, err := requireWorkspace(workspaceID)
	if err != nil {
		return 0, err
	}
	// Expiry sweeps stay deployment-wide: an expired reservation is garbage in
	// every tenant, and leaving another tenant's stale rows behind would keep
	// consuming a ceiling nobody is spending against.
	if _, err := s.db.ExecContext(ctx, `DELETE FROM cost_reservations WHERE expires_at <= ?`,
		now.UTC().Format("2006-01-02 15:04:05")); err != nil {
		return 0, err
	}
	var total int64
	err = s.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(estimated_micros), 0) FROM cost_reservations WHERE workspace = ?`, workspaceID).Scan(&total)
	return total, err
}

func (s *Store) ReservedCostMicrosScope(ctx context.Context, workspaceID string, now time.Time, subject, agentID string) (int64, error) {
	workspaceID, err := requireWorkspace(workspaceID)
	if err != nil {
		return 0, err
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM cost_reservations WHERE expires_at <= ?`,
		now.UTC().Format("2006-01-02 15:04:05")); err != nil {
		return 0, err
	}
	query := `SELECT COALESCE(SUM(estimated_micros), 0) FROM cost_reservations WHERE workspace = ?`
	args := []any{workspaceID}
	if subject != "" {
		query += ` AND subject = ?`
		args = append(args, subject)
	}
	if agentID != "" {
		query += ` AND agent_id = ?`
		args = append(args, agentID)
	}
	var total int64
	err = s.db.QueryRowContext(ctx, query, args...).Scan(&total)
	return total, err
}

// SumTokensSince returns recorded tokens for an optional subject and/or agent.
func (s *Store) SumTokensSince(ctx context.Context, workspaceID string, since time.Time, subject, agentID string) (int64, error) {
	workspaceID, err := requireWorkspace(workspaceID)
	if err != nil {
		return 0, err
	}
	query := `SELECT COALESCE(SUM(total_tokens), 0) FROM token_usage WHERE workspace = ? AND created_at >= ?`
	args := []any{workspaceID, since.UTC().Format("2006-01-02 15:04:05")}
	if subject != "" {
		query += ` AND subject = ?`
		args = append(args, subject)
	}
	if agentID != "" {
		query += ` AND agent_id = ?`
		args = append(args, agentID)
	}
	var total int64
	err = s.db.QueryRowContext(ctx, query, args...).Scan(&total)
	return total, err
}

// ReservedTokens returns in-flight token reservations for an optional scope.
func (s *Store) ReservedTokens(ctx context.Context, workspaceID string, now time.Time, subject, agentID string) (int64, error) {
	workspaceID, err := requireWorkspace(workspaceID)
	if err != nil {
		return 0, err
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM cost_reservations WHERE expires_at <= ?`,
		now.UTC().Format("2006-01-02 15:04:05")); err != nil {
		return 0, err
	}
	query := `SELECT COALESCE(SUM(estimated_tokens), 0) FROM cost_reservations WHERE workspace = ?`
	args := []any{workspaceID}
	if subject != "" {
		query += ` AND subject = ?`
		args = append(args, subject)
	}
	if agentID != "" {
		query += ` AND agent_id = ?`
		args = append(args, agentID)
	}
	var total int64
	err = s.db.QueryRowContext(ctx, query, args...).Scan(&total)
	return total, err
}

// SumByAgent returns total tokens and estimated cost grouped by agent_id.
// If since is non-zero, only rows after that time are included.
func (s *Store) SumByAgent(ctx context.Context, workspaceID string, since time.Time) ([]AgentCost, error) {
	workspaceID, err := requireWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}
	var rows *sql.Rows
	if since.IsZero() {
		rows, err = s.db.QueryContext(ctx, `
			SELECT agent_id,
			       SUM(total_tokens)  AS total_tokens,
			       SUM(prompt_tokens) AS prompt_tokens,
			       SUM(comp_tokens)   AS comp_tokens,
			       SUM(cost_usd)      AS cost_usd
			FROM token_usage
			WHERE workspace = ?
			GROUP BY agent_id
			ORDER BY agent_id`, workspaceID)
	} else {
		rows, err = s.db.QueryContext(ctx, `
			SELECT agent_id,
			       SUM(total_tokens)  AS total_tokens,
			       SUM(prompt_tokens) AS prompt_tokens,
			       SUM(comp_tokens)   AS comp_tokens,
			       SUM(cost_usd)      AS cost_usd
			FROM token_usage
			WHERE workspace = ? AND created_at > ?
			GROUP BY agent_id
			ORDER BY agent_id`,
			workspaceID, since.UTC().Format("2006-01-02 15:04:05"))
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []AgentCost
	for rows.Next() {
		var ac AgentCost
		if err := rows.Scan(&ac.AgentID, &ac.TotalTokens, &ac.PromptTokens, &ac.CompTokens, &ac.CostUSD); err != nil {
			return nil, err
		}
		out = append(out, ac)
	}
	return out, rows.Err()
}

// SumBySession returns total tokens for one agent's sessions.
// If since is non-zero, only rows after that time are included.
func (s *Store) SumBySession(ctx context.Context, workspaceID, agentID string, since time.Time) ([]SessionCost, error) {
	workspaceID, err := requireWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}
	var rows *sql.Rows
	if since.IsZero() {
		rows, err = s.db.QueryContext(ctx, `
			SELECT session_id,
			       SUM(total_tokens) AS total_tokens,
			       SUM(cost_usd)     AS cost_usd
			FROM token_usage
			WHERE workspace = ? AND agent_id = ?
			GROUP BY session_id
			ORDER BY session_id`,
			workspaceID, agentID)
	} else {
		rows, err = s.db.QueryContext(ctx, `
			SELECT session_id,
			       SUM(total_tokens) AS total_tokens,
			       SUM(cost_usd)     AS cost_usd
			FROM token_usage
			WHERE agent_id = ?
			  AND created_at > ?
			GROUP BY session_id
			ORDER BY session_id`,
			agentID, since.UTC().Format("2006-01-02 15:04:05"))
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []SessionCost
	for rows.Next() {
		var sc SessionCost
		if err := rows.Scan(&sc.SessionID, &sc.TotalTokens, &sc.CostUSD); err != nil {
			return nil, err
		}
		out = append(out, sc)
	}
	return out, rows.Err()
}

// Chargeback groups prompt-free usage by an explicit allowlisted dimension
// set. Supported names are user, feature, provider, and model.
func (s *Store) Chargeback(ctx context.Context, workspaceID string, since time.Time, groupBy []string) ([]ChargebackRow, error) {
	workspaceID, err := requireWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}
	selected := map[string]bool{}
	for _, dimension := range groupBy {
		switch strings.ToLower(strings.TrimSpace(dimension)) {
		case "user":
			selected["subject"] = true
		case "feature":
			selected["source"] = true
		case "provider":
			selected["provider"] = true
		case "model":
			selected["model"] = true
		default:
			return nil, fmt.Errorf("unsupported chargeback dimension %q", dimension)
		}
	}
	if len(selected) == 0 {
		selected = map[string]bool{"subject": true, "source": true, "provider": true, "model": true}
	}
	columns := []string{"subject", "source", "provider", "model"}
	selects, groups := make([]string, 0, 4), make([]string, 0, 4)
	for _, column := range columns {
		if selected[column] {
			selects = append(selects, column)
			groups = append(groups, column)
		} else {
			selects = append(selects, "'' AS "+column)
		}
	}
	query := `SELECT ` + strings.Join(selects, ", ") + `,
		COUNT(*) AS calls, COALESCE(SUM(attempt_count), 0) AS attempts,
		COALESCE(SUM(total_tokens), 0) AS total_tokens,
		COALESCE(SUM(cost_micros), 0) AS cost_micros, COALESCE(SUM(cost_usd), 0) AS cost_usd
		FROM token_usage WHERE workspace = ? AND created_at >= ? GROUP BY ` + strings.Join(groups, ", ") +
		` ORDER BY cost_micros DESC, total_tokens DESC`
	rows, err := s.db.QueryContext(ctx, query, workspaceID, since.UTC().Format("2006-01-02 15:04:05"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]ChargebackRow, 0)
	for rows.Next() {
		var row ChargebackRow
		if err := rows.Scan(&row.Subject, &row.Source, &row.Provider, &row.Model,
			&row.Calls, &row.Attempts, &row.TotalTokens, &row.CostMicros, &row.CostUSD); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// Close closes the DB.
func (s *Store) Close() error {
	return s.db.Close()
}
