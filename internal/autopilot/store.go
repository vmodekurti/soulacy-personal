package autopilot

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mattn/go-sqlite3"

	"github.com/soulacy/soulacy/internal/sqlitex"
)

const schemaComponent = "autopilot"

var schemaMigrations = []sqlitex.SchemaMigration{
	{Version: 1, SQL: `
CREATE TABLE autopilot_run_claims (
    subject         TEXT NOT NULL,
    run_id          TEXT NOT NULL,
    agent_id        TEXT NOT NULL,
    status          TEXT NOT NULL,
    proof_id        TEXT NOT NULL DEFAULT '',
    started_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL,
    PRIMARY KEY(subject, run_id)
);
CREATE INDEX idx_autopilot_claims_subject_status
    ON autopilot_run_claims(subject, status, updated_at DESC);

CREATE TABLE autopilot_proofs (
    id              TEXT NOT NULL,
    subject         TEXT NOT NULL,
    mission_id      TEXT NOT NULL DEFAULT '',
    run_id          TEXT NOT NULL,
    agent_id        TEXT NOT NULL,
    session_id      TEXT NOT NULL DEFAULT '',
    revision_hash   TEXT NOT NULL DEFAULT '',
    outcome         TEXT NOT NULL,
    verification    TEXT NOT NULL,
	 simulation       INTEGER NOT NULL DEFAULT 0,
    completed_at    TEXT NOT NULL,
    payload_json    BLOB NOT NULL,
    proof_hash      TEXT NOT NULL,
	PRIMARY KEY(subject, id),
    UNIQUE(subject, agent_id, run_id)
);
CREATE INDEX idx_autopilot_proofs_subject_time
    ON autopilot_proofs(subject, completed_at DESC);
CREATE INDEX idx_autopilot_proofs_agent_revision
    ON autopilot_proofs(subject, agent_id, revision_hash, completed_at DESC);

CREATE TABLE autopilot_reliability (
    subject                     TEXT NOT NULL,
    agent_id                    TEXT NOT NULL,
    revision_hash               TEXT NOT NULL DEFAULT '',
    sample_count                INTEGER NOT NULL DEFAULT 0,
    success_count               INTEGER NOT NULL DEFAULT 0,
    verified_count              INTEGER NOT NULL DEFAULT 0,
    verification_failed_count   INTEGER NOT NULL DEFAULT 0,
    verification_unknown_count  INTEGER NOT NULL DEFAULT 0,
    updated_at                  TEXT NOT NULL,
    PRIMARY KEY(subject, agent_id, revision_hash)
);
`},
	{Version: 2, SQL: `
CREATE TABLE autopilot_learning_proposals (
    id               TEXT NOT NULL,
    subject          TEXT NOT NULL,
    agent_id         TEXT NOT NULL,
    source_proof_id  TEXT NOT NULL,
    status           TEXT NOT NULL,
    created_at       TEXT NOT NULL,
    reviewed_at      TEXT,
    payload_json     BLOB NOT NULL,
	PRIMARY KEY(subject, id),
	FOREIGN KEY(subject, source_proof_id) REFERENCES autopilot_proofs(subject, id)
);
CREATE INDEX idx_autopilot_proposals_subject_status
    ON autopilot_learning_proposals(subject, status, created_at DESC);
`},
	{Version: 3, SQL: `
CREATE TABLE autopilot_deployment_versions (
    id               TEXT PRIMARY KEY,
    subject          TEXT NOT NULL,
    agent_id         TEXT NOT NULL,
    version          TEXT NOT NULL,
    revision_hash    TEXT NOT NULL,
    definition_json  BLOB NOT NULL,
    created_at       TEXT NOT NULL,
    UNIQUE(subject, agent_id, revision_hash)
);
CREATE INDEX idx_autopilot_deploy_versions_agent
    ON autopilot_deployment_versions(subject, agent_id, created_at DESC);

CREATE TABLE autopilot_deployment_channels (
    subject              TEXT NOT NULL,
    agent_id             TEXT NOT NULL,
    channel              TEXT NOT NULL,
    current_version_id   TEXT NOT NULL,
    previous_version_id  TEXT NOT NULL DEFAULT '',
    traffic_percent      INTEGER NOT NULL DEFAULT 100,
    gates_json           BLOB NOT NULL,
    updated_at           TEXT NOT NULL,
    PRIMARY KEY(subject, agent_id, channel),
    FOREIGN KEY(current_version_id) REFERENCES autopilot_deployment_versions(id)
);

CREATE TABLE autopilot_deployment_controls (
    subject        TEXT NOT NULL,
    agent_id       TEXT NOT NULL,
    frozen         INTEGER NOT NULL DEFAULT 0,
    freeze_reason  TEXT NOT NULL DEFAULT '',
    updated_at     TEXT NOT NULL,
    PRIMARY KEY(subject, agent_id)
);

CREATE TABLE autopilot_deployment_events (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    subject          TEXT NOT NULL,
    agent_id         TEXT NOT NULL,
    channel          TEXT NOT NULL,
    action           TEXT NOT NULL,
    from_version_id  TEXT NOT NULL DEFAULT '',
    to_version_id    TEXT NOT NULL DEFAULT '',
    reason           TEXT NOT NULL DEFAULT '',
    created_at       TEXT NOT NULL
);
CREATE INDEX idx_autopilot_deploy_events_agent
    ON autopilot_deployment_events(subject, agent_id, created_at DESC, id DESC);
`},
	{Version: 4, SQL: `
CREATE TABLE autopilot_goals (
    id                TEXT PRIMARY KEY,
    subject           TEXT NOT NULL,
    title             TEXT NOT NULL,
    objective         TEXT NOT NULL,
    status            TEXT NOT NULL,
    max_cost_usd      REAL NOT NULL,
    max_duration_ms   INTEGER NOT NULL,
    cost_usd          REAL NOT NULL DEFAULT 0,
	reserved_cost_usd REAL NOT NULL DEFAULT 0,
    duration_ms       INTEGER NOT NULL DEFAULT 0,
    created_at        TEXT NOT NULL,
    updated_at        TEXT NOT NULL,
    started_at        TEXT,
    finished_at       TEXT
);
CREATE INDEX idx_autopilot_goals_subject_status
    ON autopilot_goals(subject, status, updated_at DESC);

CREATE TABLE autopilot_goal_tasks (
    id                TEXT NOT NULL,
    goal_id           TEXT NOT NULL,
    title             TEXT NOT NULL,
    prompt            TEXT NOT NULL,
    agent_id          TEXT NOT NULL,
    status            TEXT NOT NULL,
    depends_on_json   BLOB NOT NULL,
    max_cost_usd      REAL NOT NULL,
    max_duration_ms   INTEGER NOT NULL,
    run_id            TEXT NOT NULL DEFAULT '',
    proof_id          TEXT NOT NULL DEFAULT '',
	result            TEXT NOT NULL DEFAULT '',
    error             TEXT NOT NULL DEFAULT '',
    cost_usd          REAL,
    duration_ms       INTEGER,
    started_at        TEXT,
    finished_at       TEXT,
	PRIMARY KEY(goal_id, id),
    FOREIGN KEY(goal_id) REFERENCES autopilot_goals(id) ON DELETE CASCADE
);
CREATE INDEX idx_autopilot_goal_tasks_goal_status
    ON autopilot_goal_tasks(goal_id, status);
`},
	{Version: 5, SQL: `
ALTER TABLE autopilot_goals ADD COLUMN error TEXT NOT NULL DEFAULT '';
`},
}

type Store struct {
	db  *sql.DB
	now func() time.Time
}

// NewStore opens or creates an Autopilot SQLite database and applies every
// additive schema migration transactionally.
func NewStore(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("%w: database path is required", ErrInvalid)
	}
	opts := sqlitex.DefaultOptions()
	opts.ForeignKeys = true
	db, err := sqlitex.Open(path, opts)
	if err != nil {
		return nil, fmt.Errorf("autopilot: open database: %w", err)
	}
	if _, err := sqlitex.MigrateSchema(db, schemaComponent, schemaMigrations); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("autopilot: migrate database: %w", err)
	}
	store := &Store{db: db, now: func() time.Time { return time.Now().UTC() }}
	// A running claim surviving a process restart has unknown external effects.
	// Quarantine it for explicit operator recovery; never silently replay it.
	now := timeString(store.timestamp())
	if _, err := db.Exec(`UPDATE autopilot_run_claims SET status = ?, updated_at = ? WHERE status = ?`,
		RunClaimUncertain, now, RunClaimClaimed); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("autopilot: quarantine interrupted runs: %w", err)
	}
	if err := quarantineInterruptedGoalTasks(db, now); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("autopilot: quarantine interrupted goal tasks: %w", err)
	}
	return store, nil
}

func quarantineInterruptedGoalTasks(db *sql.DB, now string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`
		UPDATE autopilot_goals SET updated_at = ?
		WHERE id IN (SELECT goal_id FROM autopilot_goal_tasks WHERE status = ?)`,
		now, GoalTaskRunning); err != nil {
		return err
	}
	if _, err := tx.Exec(`
		UPDATE autopilot_goal_tasks
		SET status = ?, error = ?, finished_at = NULL
		WHERE status = ?`, GoalTaskBlocked,
		"execution was interrupted; external effects are unknown and require operator resolution",
		GoalTaskRunning); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) timestamp() time.Time {
	if s != nil && s.now != nil {
		return s.now().UTC().Round(0)
	}
	return time.Now().UTC().Round(0)
}

// fixedTimeLayout keeps lexical SQLite ordering identical to chronological
// ordering. RFC3339Nano omits trailing fractional zeros, which can mis-order
// timestamps that fall in the same second when stored as TEXT.
const fixedTimeLayout = "2006-01-02T15:04:05.000000000Z07:00"

func timeString(t time.Time) string { return t.UTC().Format(fixedTimeLayout) }

func parseTime(v string) (time.Time, error) {
	t, err := time.Parse(fixedTimeLayout, v)
	if err != nil {
		return time.Time{}, fmt.Errorf("autopilot: parse timestamp %q: %w", v, err)
	}
	return t.UTC(), nil
}

func stringPtrTime(v sql.NullString) (*time.Time, error) {
	if !v.Valid || v.String == "" {
		return nil, nil
	}
	t, err := parseTime(v.String)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func validSubject(subject string) bool {
	trimmed := strings.TrimSpace(subject)
	return subject == trimmed && trimmed != "" && len(trimmed) <= 512
}

func isConstraintError(err error) bool {
	var sqliteErr sqlite3.Error
	return errors.As(err, &sqliteErr) && sqliteErr.Code == sqlite3.ErrConstraint
}
