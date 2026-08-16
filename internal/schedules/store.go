// Package schedules is the durable, workspace-scoped record of a recurring
// agent trigger, and the claim that makes it fire exactly once (MU-023).
//
// Before this, a schedule was a cron entry in one process's memory, keyed by
// AGENT ID alone. Three things followed from that, and they compound:
//
//   - Agent IDs are unique per workspace, not per deployment. Two workspaces
//     with a "daily-report" agent shared one cron entry: registering the
//     second silently replaced the first, so one tenant's schedule stopped
//     firing and the other's fired under a principal belonging to neither.
//     The same collision ran through the running-run lock, the consecutive-
//     failure counter and the completed-fire state file.
//   - Nothing coordinated two gateway processes. Both held the whole cron
//     table, so every schedule fired once per instance. "Run two for
//     availability" and "send the customer one email" were incompatible.
//   - The whole arrangement lived in one `scheduler.principal`, so a
//     multi-user deployment could only be correct by refusing to fire at all —
//     which is what RequirePrincipal did. Fail-closed, and also feature-absent.
//
// The record fixes the first, the claim fixes the second, and having both is
// what lets the third become "fires, in the right workspace".
//
// EXACTLY-ONCE IS A CLAIM, NOT A LOCK. Each occurrence of a schedule has a
// deterministic key — the scheduled instant, in UTC. Every instance that
// believes an occurrence is due tries to INSERT that key; the unique index
// means exactly one insert succeeds, and that instance fires. There is no
// lock to acquire, no lock to release, and no lock to leak: an instance that
// dies mid-run leaves a row whose lease expires, and the row is the audit
// trail of what happened either way.
package schedules

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/soulacy/soulacy/internal/sqlitex"
	"github.com/soulacy/soulacy/internal/wsroot"
)

// Misfire policies (criterion 4).
const (
	// MisfireSkip forgets occurrences missed while nothing was running. The
	// default, because for most schedules a missed 09:00 report is stale by
	// the time anyone notices — sending yesterday's at 14:00 is worse than
	// not sending it.
	MisfireSkip = "skip"
	// MisfireCatchUp replays missed occurrences, newest first, up to
	// CatchUpLimit. Bounded on purpose: a gateway down for a week with a
	// */5 schedule has missed two thousand occurrences, and replaying them
	// is not recovery, it is a self-inflicted denial of service.
	MisfireCatchUp = "catchup"
)

// DefaultCatchUpLimit bounds a catch-up burst when a schedule does not set its
// own. Three is a judgement, not a measurement: enough to cover a deploy or a
// crash loop, few enough that the burst cannot outpace ordinary operation.
const DefaultCatchUpLimit = 3

// DefaultLease is how long a claim is honoured before another instance may
// take the occurrence.
//
// It must exceed the longest run an agent can take, or a slow run gets its
// occurrence stolen and executed twice — the exact failure this package
// exists to prevent. The scheduler's own ceiling is one hour (maxRunDuration),
// so this is deliberately longer than that rather than equal to it.
const DefaultLease = 90 * time.Minute

// Occurrence claim outcomes.
const (
	ClaimClaimed   = "claimed"
	ClaimCompleted = "completed"
	ClaimFailed    = "failed"
)

var (
	// ErrNotFound covers an unknown schedule and one belonging to another
	// workspace, indistinguishably (product invariant 8).
	ErrNotFound = errors.New("schedules: not found")
	// ErrAlreadyClaimed reports that another instance owns this occurrence.
	// Not an error condition: it is how the loser of a race finds out, and
	// exactly one instance is meant to lose.
	ErrAlreadyClaimed = errors.New("schedules: occurrence already claimed")
)

// Schedule is one agent's recurring trigger within a workspace.
type Schedule struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	AgentID     string `json:"agent_id"`

	// AgentVersion pins which version of the agent a fire runs. Recorded
	// because an agent edited between two fires must not silently change what
	// a schedule means — "what did the 09:00 run actually execute" has to stay
	// answerable after somebody rewrote the prompt at 09:05.
	AgentVersion string `json:"agent_version,omitempty"`
	// VersionPolicy is how a fire chooses its version: "pinned" runs
	// AgentVersion, "latest" runs whatever is current. Stored rather than
	// assumed, because both are legitimate and the difference is invisible
	// until it matters.
	VersionPolicy string `json:"version_policy"`

	Cron string `json:"cron"`
	// Timezone is the zone the cron expression is read in. Stored explicitly
	// because "07:00" without a zone is not a time: the same schedule shifts
	// by an hour twice a year, and a deployment whose host TZ differs from the
	// creator's fires at the wrong hour with no error anywhere.
	Timezone string `json:"timezone"`

	NextFireAt *time.Time `json:"next_fire_at,omitempty"`
	LastFireAt *time.Time `json:"last_fire_at,omitempty"`

	Enabled bool `json:"enabled"`
	// DisabledReason is why a schedule stopped firing (criterion 5). Empty for
	// a schedule an operator simply switched off; set when the system did it,
	// because "it isn't running and nobody knows why" is the failure mode this
	// field exists to prevent.
	DisabledReason string `json:"disabled_reason,omitempty"`

	MisfirePolicy string `json:"misfire_policy"`
	CatchUpLimit  int    `json:"catch_up_limit"`

	// CreatedBy is the member who created the schedule. A schedule outlives
	// the conversation that produced it; who asked for this to run every day
	// is not reconstructable later.
	CreatedBy string    `json:"created_by,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Occurrence is one scheduled instant of one schedule, and the record of who
// claimed it.
type Occurrence struct {
	WorkspaceID string    `json:"workspace_id"`
	ScheduleID  string    `json:"schedule_id"`
	Key         string    `json:"key"`
	ScheduledAt time.Time `json:"scheduled_at"`

	// ClaimedBy identifies the gateway instance that won. Recorded so a
	// failover investigation can answer "which process was holding this when
	// it stopped", which a bare boolean cannot.
	ClaimedBy      string     `json:"claimed_by"`
	ClaimedAt      time.Time  `json:"claimed_at"`
	LeaseExpiresAt time.Time  `json:"lease_expires_at"`
	Status         string     `json:"status"`
	CompletedAt    *time.Time `json:"completed_at,omitempty"`
	FailureReason  string     `json:"failure_reason,omitempty"`
	// IdempotencyKey is what the enqueued run carries, so the run store's own
	// per-workspace uniqueness refuses a duplicate submission even if two
	// instances somehow both reached enqueue (criterion 3).
	IdempotencyKey string `json:"idempotency_key"`
}

const schema = `
CREATE TABLE IF NOT EXISTS agent_schedules (
    id               TEXT NOT NULL,
    workspace_id     TEXT NOT NULL,
    agent_id         TEXT NOT NULL,
    agent_version    TEXT NOT NULL DEFAULT '',
    version_policy   TEXT NOT NULL DEFAULT 'latest',
    cron             TEXT NOT NULL DEFAULT '',
    timezone         TEXT NOT NULL DEFAULT 'UTC',
    next_fire_at     DATETIME,
    last_fire_at     DATETIME,
    enabled          INTEGER NOT NULL DEFAULT 1,
    disabled_reason  TEXT NOT NULL DEFAULT '',
    misfire_policy   TEXT NOT NULL DEFAULT 'skip',
    catch_up_limit   INTEGER NOT NULL DEFAULT 3,
    created_by       TEXT NOT NULL DEFAULT '',
    created_at       DATETIME NOT NULL,
    updated_at       DATETIME NOT NULL,
    PRIMARY KEY (workspace_id, id)
);
CREATE INDEX IF NOT EXISTS idx_schedules_ws_enabled ON agent_schedules(workspace_id, enabled, next_fire_at);
-- One schedule per agent per workspace. Composite, so two workspaces may each
-- have a "daily-report" agent on its own schedule — the collision that made
-- the in-memory map wrong.
CREATE UNIQUE INDEX IF NOT EXISTS idx_schedules_ws_agent ON agent_schedules(workspace_id, agent_id);

CREATE TABLE IF NOT EXISTS schedule_occurrences (
    workspace_id     TEXT NOT NULL,
    schedule_id      TEXT NOT NULL,
    occurrence_key   TEXT NOT NULL,
    scheduled_at     DATETIME NOT NULL,
    claimed_by       TEXT NOT NULL DEFAULT '',
    claimed_at       DATETIME NOT NULL,
    lease_expires_at DATETIME NOT NULL,
    status           TEXT NOT NULL,
    completed_at     DATETIME,
    failure_reason   TEXT NOT NULL DEFAULT '',
    idempotency_key  TEXT NOT NULL DEFAULT '',
    -- THIS is exactly-once. The primary key is the claim: whoever inserts a
    -- (workspace, schedule, occurrence) row fires it, and there is exactly one
    -- of those inserts however many instances try.
    PRIMARY KEY (workspace_id, schedule_id, occurrence_key)
);
CREATE INDEX IF NOT EXISTS idx_occurrences_ws_sched ON schedule_occurrences(workspace_id, schedule_id, scheduled_at DESC);
`

// Store is the SQLite-backed schedule record.
type Store struct{ db *sql.DB }

// Open creates or opens the schedule store at path.
func Open(path string) (*Store, error) {
	// Claim reads then writes conditionally. Under BEGIN DEFERRED two
	// instances racing for the same occurrence both take a read lock and then
	// both try to upgrade, and SQLite fails the loser with "database is
	// locked" instead of letting it wait — turning "you lost the race" into
	// "the store is broken", which a scheduler would reasonably retry.
	opts := sqlitex.DefaultOptions()
	opts.ImmediateTx = true
	db, err := sqlitex.Open(path, opts)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("schedules: schema: %w", err)
	}
	if err := sqlitex.RecordSchemaVersion(db, "schedules", 1); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// Close releases the database.
func (s *Store) Close() error { return s.db.Close() }

const scheduleColumns = `SELECT id, workspace_id, agent_id, agent_version, version_policy,
    cron, timezone, next_fire_at, last_fire_at, enabled, disabled_reason,
    misfire_policy, catch_up_limit, created_by, created_at, updated_at`

// Upsert creates or updates one workspace's schedule for an agent.
func (s *Store) Upsert(ctx context.Context, sched Schedule) (Schedule, error) {
	sched.WorkspaceID = wsroot.Normalize(sched.WorkspaceID)
	sched.AgentID = strings.TrimSpace(sched.AgentID)
	sched.ID = strings.TrimSpace(sched.ID)
	if sched.AgentID == "" {
		return Schedule{}, errors.New("schedules: an agent id is required")
	}
	if sched.ID == "" {
		sched.ID = sched.AgentID
	}
	if strings.TrimSpace(sched.Timezone) == "" {
		sched.Timezone = "UTC"
	}
	if _, err := time.LoadLocation(sched.Timezone); err != nil {
		// A zone the host cannot resolve must not silently become UTC: the
		// schedule would fire at a different hour than the creator chose, and
		// nothing anywhere would say so.
		return Schedule{}, fmt.Errorf("schedules: unknown timezone %q: %w", sched.Timezone, err)
	}
	if sched.MisfirePolicy != MisfireCatchUp {
		sched.MisfirePolicy = MisfireSkip
	}
	if sched.CatchUpLimit <= 0 {
		sched.CatchUpLimit = DefaultCatchUpLimit
	}
	if strings.TrimSpace(sched.VersionPolicy) == "" {
		sched.VersionPolicy = "latest"
	}
	now := time.Now().UTC()
	sched.UpdatedAt = now
	if sched.CreatedAt.IsZero() {
		sched.CreatedAt = now
	}

	if _, err := s.db.ExecContext(ctx, `
INSERT INTO agent_schedules (id, workspace_id, agent_id, agent_version, version_policy,
    cron, timezone, next_fire_at, last_fire_at, enabled, disabled_reason,
    misfire_policy, catch_up_limit, created_by, created_at, updated_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(workspace_id, agent_id) DO UPDATE SET
    agent_version = excluded.agent_version,
    version_policy = excluded.version_policy,
    cron = excluded.cron,
    timezone = excluded.timezone,
    next_fire_at = excluded.next_fire_at,
    enabled = excluded.enabled,
    disabled_reason = excluded.disabled_reason,
    misfire_policy = excluded.misfire_policy,
    catch_up_limit = excluded.catch_up_limit,
    updated_at = excluded.updated_at`,
		sched.ID, sched.WorkspaceID, sched.AgentID, sched.AgentVersion, sched.VersionPolicy,
		sched.Cron, sched.Timezone, sched.NextFireAt, sched.LastFireAt, sched.Enabled, sched.DisabledReason,
		sched.MisfirePolicy, sched.CatchUpLimit, sched.CreatedBy, sched.CreatedAt, sched.UpdatedAt,
	); err != nil {
		return Schedule{}, fmt.Errorf("schedules: upsert: %w", err)
	}
	return s.GetByAgent(ctx, sched.WorkspaceID, sched.AgentID)
}

// GetByAgent returns one workspace's schedule for an agent.
func (s *Store) GetByAgent(ctx context.Context, workspaceID, agentID string) (Schedule, error) {
	row := s.db.QueryRowContext(ctx,
		scheduleColumns+` FROM agent_schedules WHERE workspace_id = ? AND agent_id = ?`,
		wsroot.Normalize(workspaceID), strings.TrimSpace(agentID))
	sched, err := scanSchedule(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Schedule{}, ErrNotFound
	}
	return sched, err
}

// ListEnabled returns one workspace's active schedules.
func (s *Store) ListEnabled(ctx context.Context, workspaceID string) ([]Schedule, error) {
	return s.list(ctx, `WHERE workspace_id = ? AND enabled = 1 ORDER BY agent_id`, wsroot.Normalize(workspaceID))
}

// ListAllEnabledAcrossWorkspaces returns every workspace's active schedules.
//
// Deployment-wide and named so it cannot be reached by accident: a scheduler
// tick has no request and therefore no tenant. Each returned schedule carries
// its own WorkspaceID, so the caller fires per schedule rather than inheriting
// one workspace for all of them — the bug the single `scheduler.principal`
// made structural.
func (s *Store) ListAllEnabledAcrossWorkspaces(ctx context.Context) ([]Schedule, error) {
	return s.list(ctx, `WHERE enabled = 1 ORDER BY workspace_id, agent_id`)
}

func (s *Store) list(ctx context.Context, where string, args ...any) ([]Schedule, error) {
	rows, err := s.db.QueryContext(ctx, scheduleColumns+` FROM agent_schedules `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Schedule{}
	for rows.Next() {
		sched, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sched)
	}
	return out, rows.Err()
}

// Disable stops future executions and records why (criterion 5).
//
// The reason is not decoration. "Disabling an agent, removing permission, or
// exhausting budget prevents future executions" is only half a requirement:
// the other half is that an operator looking at a schedule that stopped can
// tell which of those happened without reading logs from the moment it did.
func (s *Store) Disable(ctx context.Context, workspaceID, agentID, reason string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE agent_schedules SET enabled = 0, disabled_reason = ?, updated_at = ?
          WHERE workspace_id = ? AND agent_id = ?`,
		strings.TrimSpace(reason), time.Now().UTC(), wsroot.Normalize(workspaceID), strings.TrimSpace(agentID))
	if err != nil {
		return err
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		return ErrNotFound
	}
	return nil
}

func scanSchedule(row interface{ Scan(...any) error }) (Schedule, error) {
	var sched Schedule
	var next, last sql.NullTime
	if err := row.Scan(&sched.ID, &sched.WorkspaceID, &sched.AgentID, &sched.AgentVersion, &sched.VersionPolicy,
		&sched.Cron, &sched.Timezone, &next, &last, &sched.Enabled, &sched.DisabledReason,
		&sched.MisfirePolicy, &sched.CatchUpLimit, &sched.CreatedBy, &sched.CreatedAt, &sched.UpdatedAt); err != nil {
		return Schedule{}, err
	}
	if next.Valid {
		sched.NextFireAt = &next.Time
	}
	if last.Valid {
		sched.LastFireAt = &last.Time
	}
	return sched, nil
}
