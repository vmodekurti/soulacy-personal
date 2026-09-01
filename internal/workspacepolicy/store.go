// Package workspacepolicy stores the limits a workspace has set on ITSELF.
//
// MU-030 criterion 1: "per-workspace budgets, quotas and retention". The
// multi-level quota machinery (internal/quota, MU-024) has always been able to
// express a per-workspace ceiling; what it could not do is let anyone but the
// deployment operator set one, because the only source was a YAML file on the
// gateway's disk. A Team Preview customer with twenty workspaces could not give
// one team a smaller budget than another without a config edit and a restart.
//
// THE ONE PROPERTY THAT MAKES THIS SAFE TO EXPOSE: a stored entry can only
// TIGHTEN. `Compose` folds these values into the operator's policy with the
// same rule the config levels already use — the smaller positive value wins —
// so a workspace owner writing `daily_usd: 1000000` gets whatever the
// deployment already allowed and not a penny more.
//
// That is not a check bolted onto the write path, where a future endpoint
// could forget it. It is the composition itself: there is no code path in
// which a stored number becomes an effective ceiling larger than the
// configured one, because the only function that turns stored numbers into
// effective ones takes the minimum. A validation on write would be the version
// that a second write path bypasses.
package workspacepolicy

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/soulacy/soulacy/internal/quota"
	"github.com/soulacy/soulacy/internal/sqlitex"
	"github.com/soulacy/soulacy/internal/wsroot"
)

// Policy is one workspace's self-imposed limits.
//
// Every field is a CEILING and zero means "not set here", never "zero
// allowed". An operator or owner clearing a field must get back whatever the
// deployment permits, not a workspace that can spend nothing — the alternative
// makes an empty form a denial of service against your own team.
type Policy struct {
	WorkspaceID string `json:"workspace_id"`
	// DailyUSD, MonthlyUSD, DailyTokens and Concurrency mirror
	// config.QuotaLimit so an owner setting one recognises it from the docs.
	DailyUSD    float64 `json:"daily_usd,omitempty"`
	MonthlyUSD  float64 `json:"monthly_usd,omitempty"`
	DailyTokens int64   `json:"daily_tokens,omitempty"`
	// PerUserDailyTokens is the rolling 24-hour token allowance for each
	// principal in this workspace. It is separate from DailyTokens, which is
	// the aggregate ceiling shared by the whole workspace.
	PerUserDailyTokens int64 `json:"per_user_daily_tokens,omitempty"`
	Concurrency        int   `json:"concurrency,omitempty"`
	// ConversationHistory, ActionEvents and AuditLogs are retention windows in
	// Go duration syntax. Empty means the deployment's window applies.
	ConversationHistory string `json:"conversation_history,omitempty"`
	ActionEvents        string `json:"action_events,omitempty"`
	AuditLogs           string `json:"audit_logs,omitempty"`

	UpdatedBy string    `json:"updated_by,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

// ErrInvalidPolicy reports a policy that cannot be stored.
var ErrInvalidPolicy = errors.New("workspacepolicy: invalid policy")

// Validate refuses values that are wrong rather than merely permissive.
//
// NEGATIVE is the case worth naming. A negative retention parses as a valid
// duration and every downstream `retention > 0` guard reads it as "disabled" —
// so `-1h` silently turns pruning off while an operator reading the setting
// sees a retention policy in force. That exact bug was found in the
// deployment-wide config on this branch; storing it per workspace would
// reintroduce it once per tenant.
func (p Policy) Validate() error {
	var problems []string
	if p.DailyUSD < 0 || p.MonthlyUSD < 0 {
		problems = append(problems, "budgets must not be negative")
	}
	if p.DailyTokens < 0 {
		problems = append(problems, "daily_tokens must not be negative")
	}
	if p.PerUserDailyTokens < 0 {
		problems = append(problems, "per_user_daily_tokens must not be negative")
	}
	if p.Concurrency < 0 {
		problems = append(problems, "concurrency must not be negative")
	}
	for field, value := range map[string]string{
		"conversation_history": p.ConversationHistory,
		"action_events":        p.ActionEvents,
		"audit_logs":           p.AuditLogs,
	} {
		if strings.TrimSpace(value) == "" {
			continue
		}
		parsed, err := time.ParseDuration(value)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %q is not a valid duration", field, value))
			continue
		}
		if parsed <= 0 {
			problems = append(problems, fmt.Sprintf(
				"%s: %q is not positive — a negative or zero window parses fine and reads downstream as "+
					"'retention disabled', so it silently turns pruning off", field, value))
		}
	}
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrInvalidPolicy, strings.Join(problems, "; "))
}

// Limit projects the quota half into the units the reservation transaction
// uses.
func (p Policy) Limit() quota.Limit {
	return quota.Limit{
		DailyMicros:   usdToMicros(p.DailyUSD),
		MonthlyMicros: usdToMicros(p.MonthlyUSD),
		DailyTokens:   p.DailyTokens,
		Concurrency:   p.Concurrency,
	}
}

func usdToMicros(usd float64) int64 {
	if usd <= 0 {
		return 0
	}
	return int64(usd * 1_000_000)
}

// Store persists one deployment's per-workspace policies.
type Store struct{ db *sql.DB }

const schema = `
CREATE TABLE IF NOT EXISTS workspace_policies(
	workspace_id         TEXT PRIMARY KEY,
	daily_micros         INTEGER NOT NULL DEFAULT 0,
	monthly_micros       INTEGER NOT NULL DEFAULT 0,
	daily_tokens         INTEGER NOT NULL DEFAULT 0,
	per_user_daily_tokens INTEGER NOT NULL DEFAULT 0,
	concurrency          INTEGER NOT NULL DEFAULT 0,
	conversation_history TEXT NOT NULL DEFAULT '',
	action_events        TEXT NOT NULL DEFAULT '',
	audit_logs           TEXT NOT NULL DEFAULT '',
	updated_by           TEXT NOT NULL DEFAULT '',
	updated_at           TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);`

// NewStore opens the policy store.
//
// workspace_id is the PRIMARY KEY rather than a filtered column, which is what
// makes one tenant's policy structurally unable to be a second row under
// another's id: there is exactly one row per workspace and the key is the
// tenant.
func NewStore(path string) (*Store, error) {
	db, err := sqlitex.Open(path, sqlitex.DefaultOptions())
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := ensurePolicyColumn(db, "per_user_daily_tokens", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func ensurePolicyColumn(db *sql.DB, name, definition string) error {
	rows, err := db.Query(`PRAGMA table_info(workspace_policies)`)
	if err != nil {
		return err
	}
	found := false
	for rows.Next() {
		var cid int
		var column, kind string
		var notNull, primaryKey int
		var defaultValue any
		if err := rows.Scan(&cid, &column, &kind, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			return err
		}
		if column == name {
			found = true
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if found {
		return nil
	}
	_, err = db.Exec(`ALTER TABLE workspace_policies ADD COLUMN ` + name + ` ` + definition)
	return err
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// Get returns a workspace's stored policy. A workspace with none set gets a
// zero policy, which composes to "the deployment's limits apply".
func (s *Store) Get(ctx context.Context, workspaceID string) (Policy, error) {
	if s == nil || s.db == nil {
		return Policy{}, errors.New("workspacepolicy: store is unavailable")
	}
	workspaceID = wsroot.Normalize(workspaceID)
	policy := Policy{WorkspaceID: workspaceID}
	var dailyMicros, monthlyMicros int64
	err := s.db.QueryRowContext(ctx, `SELECT daily_micros, monthly_micros, daily_tokens, per_user_daily_tokens, concurrency,
		conversation_history, action_events, audit_logs, updated_by, updated_at
		FROM workspace_policies WHERE workspace_id = ?`, workspaceID).Scan(
		&dailyMicros, &monthlyMicros, &policy.DailyTokens, &policy.PerUserDailyTokens, &policy.Concurrency,
		&policy.ConversationHistory, &policy.ActionEvents, &policy.AuditLogs,
		&policy.UpdatedBy, &policy.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return policy, nil
	}
	if err != nil {
		return Policy{}, err
	}
	policy.DailyUSD = float64(dailyMicros) / 1_000_000
	policy.MonthlyUSD = float64(monthlyMicros) / 1_000_000
	return policy, nil
}

// Set stores a workspace's policy, replacing whatever was there.
//
// Stored in MICROS rather than as the float an owner typed: a budget compared
// against accumulated spend has to be exact, and float dollars accumulate
// rounding until "$25.00" is not $25.00. The API keeps dollars because that is
// what a person means.
func (s *Store) Set(ctx context.Context, workspaceID, actor string, policy Policy) (Policy, error) {
	if s == nil || s.db == nil {
		return Policy{}, errors.New("workspacepolicy: store is unavailable")
	}
	if err := policy.Validate(); err != nil {
		return Policy{}, err
	}
	workspaceID = wsroot.Normalize(workspaceID)
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx, `INSERT INTO workspace_policies(
		workspace_id, daily_micros, monthly_micros, daily_tokens, per_user_daily_tokens, concurrency,
		conversation_history, action_events, audit_logs, updated_by, updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(workspace_id) DO UPDATE SET
			daily_micros=excluded.daily_micros, monthly_micros=excluded.monthly_micros,
			daily_tokens=excluded.daily_tokens, per_user_daily_tokens=excluded.per_user_daily_tokens,
			concurrency=excluded.concurrency,
			conversation_history=excluded.conversation_history, action_events=excluded.action_events,
			audit_logs=excluded.audit_logs, updated_by=excluded.updated_by, updated_at=excluded.updated_at`,
		workspaceID, usdToMicros(policy.DailyUSD), usdToMicros(policy.MonthlyUSD),
		policy.DailyTokens, policy.PerUserDailyTokens, policy.Concurrency,
		strings.TrimSpace(policy.ConversationHistory), strings.TrimSpace(policy.ActionEvents),
		strings.TrimSpace(policy.AuditLogs), actor, now)
	if err != nil {
		return Policy{}, err
	}
	return s.Get(ctx, workspaceID)
}

// All returns every stored policy, for composing the effective quota policy.
func (s *Store) All(ctx context.Context) ([]Policy, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspacepolicy: store is unavailable")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT workspace_id, daily_micros, monthly_micros, daily_tokens, per_user_daily_tokens,
		concurrency, conversation_history, action_events, audit_logs, updated_by, updated_at
		FROM workspace_policies ORDER BY workspace_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Policy
	for rows.Next() {
		var policy Policy
		var dailyMicros, monthlyMicros int64
		if err := rows.Scan(&policy.WorkspaceID, &dailyMicros, &monthlyMicros, &policy.DailyTokens, &policy.PerUserDailyTokens,
			&policy.Concurrency, &policy.ConversationHistory, &policy.ActionEvents, &policy.AuditLogs,
			&policy.UpdatedBy, &policy.UpdatedAt); err != nil {
			return nil, err
		}
		policy.DailyUSD = float64(dailyMicros) / 1_000_000
		policy.MonthlyUSD = float64(monthlyMicros) / 1_000_000
		out = append(out, policy)
	}
	return out, rows.Err()
}

// PurgeWorkspace removes one workspace's policy (MU-032 criterion 4).
func (s *Store) PurgeWorkspace(ctx context.Context, workspaceID string) (int64, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("workspacepolicy: store is unavailable")
	}
	result, err := s.db.ExecContext(ctx,
		`DELETE FROM workspace_policies WHERE workspace_id = ?`, wsroot.Normalize(workspaceID))
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
