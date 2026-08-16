// Package runs is the durable record of an agent run (MU-020).
//
// Before this, a run existed only as a message in an in-memory inbox and a
// goroutine processing it. That is enough for a chat request whose caller
// waits on the response, and it is not enough for anything else: a gateway
// restart loses every queued and running job, a client that disconnects has no
// way back to its own run, and a retried submission starts the work twice.
//
// The record is what makes a run addressable. It carries the tenant, the exact
// agent version, the principal who asked, the policy in force when it was
// admitted, and the budget it reserved — because every one of those is a fact
// about the moment of admission that cannot be reconstructed afterwards. An
// agent edited an hour later must not change what a completed run is understood
// to have done.
package runs

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

// Status values. The set is closed: a status outside it is a bug, not a new
// feature, and the transition table below is the only place it may change.
const (
	StatusQueued  = "queued"
	StatusRunning = "running"
	StatusPaused  = "paused"
	// StatusCancelling is "somebody asked, the worker has not stopped yet"
	// (MU-027 criteria 3 and 5).
	//
	// Cancelling a RUNNING run used to write "cancelled" directly, which is a
	// terminal state — so the API answered "cancelled" while the work was
	// still executing and its side effects were still landing. The handler's
	// own comment warned about exactly this: "cancelled" and "asked to cancel"
	// are different promises, and a caller told the wrong one stops watching
	// too early. Nothing made the worker observe the flag either, so the run
	// ran to completion and only its OUTCOME was discarded.
	//
	// Non-terminal on purpose: it is a request, and the run has not stopped
	// until the worker says so.
	StatusCancelling = "cancelling"
	StatusSucceeded  = "succeeded"
	StatusFailed     = "failed"
	StatusCancelled  = "cancelled"
)

var (
	// ErrNotFound is returned for an unknown run, and for a run belonging to
	// another workspace. The two are deliberately indistinguishable: telling a
	// caller that a run exists but is not theirs turns run IDs into an
	// enumeration oracle (product invariant 8).
	ErrNotFound = errors.New("runs: not found")
	// ErrInvalidTransition reports a state change the machine does not allow.
	ErrInvalidTransition = errors.New("runs: invalid state transition")
	// ErrTerminal reports an attempt to move a run that has already finished.
	ErrTerminal = errors.New("runs: run has already reached a terminal state")
)

// transitions is the whole state machine, in one table.
//
// Written as data rather than as scattered if-statements because the property
// that matters — "a finished run never runs again" — is checkable by reading
// one map. A cancelled run resurrected into running would execute work a person
// explicitly stopped.
var transitions = map[string]map[string]bool{
	StatusQueued: {
		StatusRunning: true, StatusCancelled: true, StatusFailed: true,
	},
	StatusRunning: {
		StatusPaused: true, StatusSucceeded: true, StatusFailed: true,
		StatusCancelling: true,
		// StatusCancelled is deliberately NOT reachable directly from running.
		// A running run has a worker mid-operation; declaring it finished
		// before that worker has stopped is the lie this state machine exists
		// to prevent.
	},
	StatusPaused: {
		StatusRunning: true, StatusCancelling: true, StatusFailed: true,
	},
	StatusCancelling: {
		// Only the worker closes this out. Succeeded is absent: a run that was
		// asked to stop and then finished its work anyway did not succeed in
		// any sense the person who cancelled it would accept.
		StatusCancelled: true, StatusFailed: true,
	},
	// Terminal. Empty maps rather than absent keys, so "unknown status" and
	// "finished" are distinguishable when something goes wrong.
	StatusSucceeded: {},
	StatusFailed:    {},
	StatusCancelled: {},
}

// Terminal reports whether a status is final.
func Terminal(status string) bool {
	next, known := transitions[status]
	return known && len(next) == 0
}

// CanTransition reports whether from → to is allowed.
func CanTransition(from, to string) bool { return transitions[from][to] }

// CancelRequested reports whether a worker has been asked to stop.
//
// The signal a running worker polls between bounded operations. Terminal
// states count: a run cancelled while queued, or failed by a recovery sweep
// under it, is equally a reason to stop — the worker's job is to notice that
// finishing is pointless, not to distinguish why.
func CancelRequested(status string) bool {
	return status == StatusCancelling || status == StatusCancelled || Terminal(status)
}

// RequestCancel asks a run to stop, choosing the right target for its state.
//
// A QUEUED run has no worker, so it is cancelled outright — there is nothing
// to wait for. A RUNNING or PAUSED one goes to `cancelling`, because saying
// "cancelled" while a worker is mid-operation is a promise the system has not
// kept. The caller is told which happened, so a client knows whether to stop
// watching or keep following.
func (s *Store) RequestCancel(ctx context.Context, workspaceID, id, reason string) (Run, error) {
	current, err := s.Get(ctx, wsroot.Normalize(workspaceID), id)
	if err != nil {
		return Run{}, err
	}
	target := StatusCancelling
	if current.Status == StatusQueued {
		target = StatusCancelled
	}
	if current.Status == StatusCancelling {
		// Already asked. Idempotent rather than an error: a client retrying a
		// cancel it did not see acknowledged should not be told it failed.
		return current, nil
	}
	return s.Transition(ctx, workspaceID, id, target, TransitionOptions{FailureReason: reason})
}

// Run is one durable agent run.
type Run struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	AgentID     string `json:"agent_id"`
	// AgentVersion pins what actually ran. An agent edited later must not
	// change the meaning of a finished run: "which prompt produced this" has
	// to stay answerable after the prompt changed.
	AgentVersion string `json:"agent_version"`
	SessionID    string `json:"session_id,omitempty"`

	// Subject, PrincipalKind and CredentialID identify who asked. The
	// credential is recorded separately from the subject because a person and
	// the token acting for them are different facts: revoking one should not
	// make the run unattributable to the other.
	Subject       string `json:"subject,omitempty"`
	PrincipalKind string `json:"principal_kind,omitempty"`
	CredentialID  string `json:"credential_id,omitempty"`

	// IdempotencyKey is client-chosen and unique per workspace. Per workspace,
	// not globally: two tenants picking "daily-report" is ordinary, and a
	// global constraint would hand the second one the first one's run.
	IdempotencyKey string `json:"idempotency_key,omitempty"`

	// PolicySnapshot is the authorization state at admission, serialized. A
	// long run outlives the grants that admitted it, and an audit asking "was
	// this allowed?" means allowed *then*.
	PolicySnapshot json.RawMessage `json:"policy_snapshot,omitempty"`
	// ReservationID ties the run to the budget it reserved, so reconciliation
	// can find the reservation from the run and vice versa.
	ReservationID string `json:"reservation_id,omitempty"`

	// Attempt counts worker claims, MaxAttempts bounds them, and SideEffectAt
	// records the first outside-visible call the run made (MU-021 criterion
	// 6). SideEffectAt is the fact that decides retry safety: a run that has
	// not yet acted can be re-queued after a worker dies, and one that has
	// cannot be, because "retry" would mean sending the second email.
	// ExternalMicros accumulates time spent waiting on somebody else's system
	// — LLM providers and tool subprocesses — so processing latency can be
	// reported without it (MU-027 criterion 6). Microseconds because a fast
	// tool call rounds to zero milliseconds and a run makes many of them.
	ExternalMicros int64 `json:"external_micros,omitempty"`

	Attempt        int        `json:"attempt"`
	MaxAttempts    int        `json:"max_attempts"`
	SideEffectAt   *time.Time `json:"side_effect_at,omitempty"`
	SideEffectTool string     `json:"side_effect_tool,omitempty"`

	Status string `json:"status"`
	// Cursor is the event-stream position a client resumes from. Returned at
	// submission so a caller that disconnects immediately still knows where
	// its own run's events begin.
	Cursor        string          `json:"cursor"`
	Payload       json.RawMessage `json:"payload,omitempty"`
	Result        string          `json:"result,omitempty"`
	FailureReason string          `json:"failure_reason,omitempty"`

	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	StartedAt *time.Time `json:"started_at,omitempty"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
}

const schema = `
CREATE TABLE IF NOT EXISTS agent_runs (
    id               TEXT NOT NULL,
    workspace_id     TEXT NOT NULL,
    agent_id         TEXT NOT NULL,
    agent_version    TEXT NOT NULL DEFAULT '',
    session_id       TEXT NOT NULL DEFAULT '',
    subject          TEXT NOT NULL DEFAULT '',
    principal_kind   TEXT NOT NULL DEFAULT '',
    credential_id    TEXT NOT NULL DEFAULT '',
    idempotency_key  TEXT NOT NULL DEFAULT '',
    policy_snapshot  TEXT NOT NULL DEFAULT '',
    reservation_id   TEXT NOT NULL DEFAULT '',
    status           TEXT NOT NULL,
    cursor           TEXT NOT NULL DEFAULT '',
    payload          TEXT NOT NULL DEFAULT '',
    result           TEXT NOT NULL DEFAULT '',
    failure_reason   TEXT NOT NULL DEFAULT '',
    created_at       DATETIME NOT NULL,
    updated_at       DATETIME NOT NULL,
    started_at       DATETIME,
    ended_at         DATETIME,
    -- MU-021 criterion 6. attempt counts how many times a worker has claimed
    -- this run; max_attempts bounds it. side_effect_at is set the first time
    -- the run makes an outside-visible call, and is what separates a run a
    -- lost worker may safely re-queue from one that has already acted.
    external_micros  INTEGER NOT NULL DEFAULT 0,
    attempt          INTEGER NOT NULL DEFAULT 0,
    max_attempts     INTEGER NOT NULL DEFAULT 3,
    side_effect_at   DATETIME,
    side_effect_tool TEXT NOT NULL DEFAULT '',
    -- Composite, not a bare id primary key. internal/ownership/catalog.go has
    -- declared this table CompositeUniqueness since MU-020, and the schema did
    -- not implement it: a bare id primary key made run IDs globally unique, so
    -- one workspace submitting an ID another workspace already used got a
    -- constraint violation — a weak enumeration oracle over other tenants' run
    -- IDs (product invariant 8), and a claim in the machine-checked source of
    -- truth that the storage did not back.
    PRIMARY KEY (workspace_id, id)
);
CREATE INDEX IF NOT EXISTS idx_runs_ws_created ON agent_runs(workspace_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_runs_ws_status  ON agent_runs(workspace_id, status, created_at DESC);
-- Idempotency is scoped to the workspace. A partial index so the many rows
-- with no key do not collide with each other.
CREATE UNIQUE INDEX IF NOT EXISTS idx_runs_idempotency
    ON agent_runs(workspace_id, idempotency_key) WHERE idempotency_key <> '';
`

// Store is the SQLite-backed run record.
type Store struct{ db *sql.DB }

// Open creates or opens the run store at path.
func Open(path string) (*Store, error) {
	// Submit reads an existing run and inserts when absent, inside one
	// transaction. See sqlitex.Options.ImmediateTx: under BEGIN DEFERRED two
	// concurrent retries of the same idempotency key would both take a read
	// lock and then both try to upgrade, and SQLite fails the loser with
	// "database is locked" instead of letting it wait.
	opts := sqlitex.DefaultOptions()
	opts.ImmediateTx = true
	db, err := sqlitex.Open(path, opts)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("runs: schema: %w", err)
	}
	if err := migrateRunPrimaryKey(db); err != nil {
		db.Close()
		return nil, err
	}
	if err := migrateRetryColumns(db); err != nil {
		db.Close()
		return nil, err
	}
	if err := sqlitex.RecordSchemaVersion(db, "runs", 2); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// DefaultMaxAttempts bounds how many workers may claim one run. Three is not a
// tuning choice so much as a shape: one original attempt plus two recoveries
// covers a rolling restart and a single crash, and anything unbounded turns a
// run that reliably kills its worker into a machine for killing workers.
const DefaultMaxAttempts = 3

// migrateRetryColumns adds the MU-021 retry-safety columns to a store created
// before them. CREATE TABLE IF NOT EXISTS silently does nothing for an
// existing table, so a v1 database would otherwise keep running against a
// schema that has no idea whether a run has acted — and the recovery sweep
// would read that absence as "safe to retry".
//
// Each ALTER is attempted independently and a duplicate-column error is the
// success case, so the migration is idempotent without needing to read
// PRAGMA table_info and reason about it.
// migrateRunPrimaryKey rebuilds agent_runs when it still carries the bare
// `id TEXT PRIMARY KEY` from before MU-027.
//
// SQLite cannot alter a primary key, so this is the copy-and-rename dance. The
// OLD table is renamed aside rather than the new one being given a temporary
// name, so `agent_runs` stays the only name ever CREATEd — the ownership
// catalog's discovery scan treats every CREATE TABLE as a durable store to
// classify, and a scratch table would show up as one.
func migrateRunPrimaryKey(db *sql.DB) error {
	var ddl string
	err := db.QueryRow(
		`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'agent_runs'`).Scan(&ddl)
	if err != nil || !strings.Contains(ddl, "id               TEXT PRIMARY KEY") {
		// Absent, already migrated, or created fresh from the current schema.
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, stmt := range []string{
		`ALTER TABLE agent_runs RENAME TO agent_runs_pre_composite`,
		strings.Replace(schema, "IF NOT EXISTS agent_runs", "agent_runs", 1),
		`INSERT INTO agent_runs SELECT * FROM agent_runs_pre_composite`,
		`DROP TABLE agent_runs_pre_composite`,
	} {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("runs: migrate primary key: %w", err)
		}
	}
	return tx.Commit()
}

func migrateRetryColumns(db *sql.DB) error {
	for _, stmt := range []string{
		`ALTER TABLE agent_runs ADD COLUMN external_micros INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE agent_runs ADD COLUMN attempt INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE agent_runs ADD COLUMN max_attempts INTEGER NOT NULL DEFAULT 3`,
		`ALTER TABLE agent_runs ADD COLUMN side_effect_at DATETIME`,
		`ALTER TABLE agent_runs ADD COLUMN side_effect_tool TEXT NOT NULL DEFAULT ''`,
	} {
		if _, err := db.Exec(stmt); err != nil && !strings.Contains(err.Error(), "duplicate column name") {
			return fmt.Errorf("runs: migrate: %w", err)
		}
	}
	return nil
}

// Close releases the database.
func (s *Store) Close() error { return s.db.Close() }

// Submit records a new run, or returns the existing one when the caller
// repeats an idempotency key.
//
// Returning the existing run rather than an error is the point: a client that
// retries because it never saw the response must end up with one run and its
// ID, not with a conflict it has to interpret. `replayed` tells the caller
// which happened, so the API can answer 202 for a new run and 200 for a
// replay without guessing.
func (s *Store) Submit(ctx context.Context, run Run) (stored Run, replayed bool, err error) {
	run.WorkspaceID = wsroot.Normalize(run.WorkspaceID)
	run.AgentID = strings.TrimSpace(run.AgentID)
	run.IdempotencyKey = strings.TrimSpace(run.IdempotencyKey)
	if run.ID = strings.TrimSpace(run.ID); run.ID == "" {
		return Run{}, false, errors.New("runs: a run id is required")
	}
	if run.AgentID == "" {
		return Run{}, false, errors.New("runs: an agent id is required")
	}
	if run.Status == "" {
		run.Status = StatusQueued
	}
	if run.MaxAttempts <= 0 {
		run.MaxAttempts = DefaultMaxAttempts
	}
	if _, known := transitions[run.Status]; !known {
		return Run{}, false, fmt.Errorf("runs: unknown status %q", run.Status)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Run{}, false, err
	}
	defer tx.Rollback() //nolint:errcheck

	if run.IdempotencyKey != "" {
		existing, lookupErr := scanRun(tx.QueryRowContext(ctx,
			selectColumns+` FROM agent_runs WHERE workspace_id = ? AND idempotency_key = ?`,
			run.WorkspaceID, run.IdempotencyKey))
		switch {
		case lookupErr == nil:
			if err := tx.Commit(); err != nil {
				return Run{}, false, err
			}
			return existing, true, nil
		case !errors.Is(lookupErr, sql.ErrNoRows):
			return Run{}, false, lookupErr
		}
	}

	now := time.Now().UTC()
	run.CreatedAt, run.UpdatedAt = now, now
	if run.Cursor == "" {
		run.Cursor = run.ID
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO agent_runs (id, workspace_id, agent_id, agent_version, session_id, subject,
    principal_kind, credential_id, idempotency_key, policy_snapshot, reservation_id,
    status, cursor, payload, result, failure_reason, created_at, updated_at, started_at, ended_at,
    max_attempts)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		run.ID, run.WorkspaceID, run.AgentID, run.AgentVersion, run.SessionID, run.Subject,
		run.PrincipalKind, run.CredentialID, run.IdempotencyKey, string(run.PolicySnapshot), run.ReservationID,
		run.Status, run.Cursor, string(run.Payload), run.Result, run.FailureReason,
		run.CreatedAt, run.UpdatedAt, run.StartedAt, run.EndedAt, run.MaxAttempts,
	); err != nil {
		return Run{}, false, fmt.Errorf("runs: insert: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Run{}, false, err
	}
	return run, false, nil
}

const selectColumns = `SELECT id, workspace_id, agent_id, agent_version, session_id, subject,
    principal_kind, credential_id, idempotency_key, policy_snapshot, reservation_id,
    status, cursor, payload, result, failure_reason, created_at, updated_at, started_at, ended_at,
    attempt, max_attempts, side_effect_at, side_effect_tool, external_micros`

// Get returns one run within a workspace.
func (s *Store) Get(ctx context.Context, workspaceID, id string) (Run, error) {
	run, err := scanRun(s.db.QueryRowContext(ctx,
		selectColumns+` FROM agent_runs WHERE workspace_id = ? AND id = ?`,
		wsroot.Normalize(workspaceID), strings.TrimSpace(id)))
	if errors.Is(err, sql.ErrNoRows) {
		return Run{}, ErrNotFound
	}
	return run, err
}

// List returns a workspace's runs, newest first. An empty status means all.
func (s *Store) List(ctx context.Context, workspaceID, status string, limit int) ([]Run, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	query := selectColumns + ` FROM agent_runs WHERE workspace_id = ?`
	args := []any{wsroot.Normalize(workspaceID)}
	if status = strings.TrimSpace(status); status != "" {
		query += ` AND status = ?`
		args = append(args, status)
	}
	query += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, rows.Err()
}

// Transition moves a run to a new status, validating the change.
//
// The check and the write are one statement, not a read followed by a write:
// two workers racing to finish the same run would otherwise both read
// "running" and both write a terminal state, and the second would overwrite
// the first's result.
func (s *Store) Transition(ctx context.Context, workspaceID, id, to string, opts TransitionOptions) (Run, error) {
	workspaceID = wsroot.Normalize(workspaceID)
	id = strings.TrimSpace(id)
	if _, known := transitions[to]; !known {
		return Run{}, fmt.Errorf("runs: unknown status %q", to)
	}

	current, err := s.Get(ctx, workspaceID, id)
	if err != nil {
		return Run{}, err
	}
	if Terminal(current.Status) {
		return Run{}, fmt.Errorf("%w: %s is %s", ErrTerminal, id, current.Status)
	}
	if !CanTransition(current.Status, to) {
		return Run{}, fmt.Errorf("%w: %s → %s", ErrInvalidTransition, current.Status, to)
	}

	now := time.Now().UTC()
	sets := []string{"status = ?", "updated_at = ?"}
	args := []any{to, now}
	if to == StatusRunning {
		// MU-021 criterion 6: attempt counts worker CLAIMS, so it is
		// incremented where the claim happens rather than where a recovery
		// re-queues. A run that dies before any worker claims it has used
		// nothing, and a run that kills three workers has used three — which
		// is the number the bound is about.
		sets = append(sets, "attempt = attempt + 1")
		if current.StartedAt == nil {
			sets = append(sets, "started_at = ?")
			args = append(args, now)
		}
	}
	if Terminal(to) {
		sets = append(sets, "ended_at = ?")
		args = append(args, now)
	}
	if opts.Result != "" {
		sets = append(sets, "result = ?")
		args = append(args, opts.Result)
	}
	if opts.FailureReason != "" {
		sets = append(sets, "failure_reason = ?")
		args = append(args, opts.FailureReason)
	}
	if opts.Cursor != "" {
		sets = append(sets, "cursor = ?")
		args = append(args, opts.Cursor)
	}
	// The WHERE clause repeats the status we read. That is what makes this
	// safe under concurrency: a racing transition changes the status, this
	// UPDATE matches nothing, and the loser is told rather than silently
	// overwriting the winner.
	args = append(args, workspaceID, id, current.Status)
	res, err := s.db.ExecContext(ctx,
		`UPDATE agent_runs SET `+strings.Join(sets, ", ")+
			` WHERE workspace_id = ? AND id = ? AND status = ?`, args...)
	if err != nil {
		return Run{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Run{}, fmt.Errorf("%w: %s changed underneath this transition", ErrInvalidTransition, id)
	}
	return s.Get(ctx, workspaceID, id)
}

// TransitionOptions carries the fields a status change may also set.
type TransitionOptions struct {
	Result        string
	FailureReason string
	Cursor        string
}

// Recover returns runs left mid-flight by a previous process, across every
// workspace.
//
// Deployment-wide on purpose and named so it cannot be reached by accident: a
// restart sweep has no request and therefore no tenant. Every returned run
// carries its own WorkspaceID, so the resumer acts per run rather than
// inheriting one workspace's context for all of them.
func (s *Store) RecoverAcrossWorkspaces(ctx context.Context) ([]Run, error) {
	rows, err := s.db.QueryContext(ctx,
		selectColumns+` FROM agent_runs WHERE status IN (?, ?, ?) ORDER BY created_at ASC`,
		StatusQueued, StatusRunning, StatusPaused)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, rows.Err()
}

type scanner interface{ Scan(dest ...any) error }

func scanRun(row scanner) (Run, error) {
	var run Run
	var policy, payload string
	var started, ended sql.NullTime
	var sideEffect sql.NullTime
	if err := row.Scan(&run.ID, &run.WorkspaceID, &run.AgentID, &run.AgentVersion, &run.SessionID,
		&run.Subject, &run.PrincipalKind, &run.CredentialID, &run.IdempotencyKey, &policy,
		&run.ReservationID, &run.Status, &run.Cursor, &payload, &run.Result, &run.FailureReason,
		&run.CreatedAt, &run.UpdatedAt, &started, &ended,
		&run.Attempt, &run.MaxAttempts, &sideEffect, &run.SideEffectTool, &run.ExternalMicros); err != nil {
		return Run{}, err
	}
	if sideEffect.Valid {
		run.SideEffectAt = &sideEffect.Time
	}
	if policy != "" {
		run.PolicySnapshot = json.RawMessage(policy)
	}
	if payload != "" {
		run.Payload = json.RawMessage(payload)
	}
	if started.Valid {
		run.StartedAt = &started.Time
	}
	if ended.Valid {
		run.EndedAt = &ended.Time
	}
	return run, nil
}

// Principal reconstructs the identity a run was admitted under.
//
// Execution uses the *recorded* principal rather than whatever context the
// worker happens to hold. A durable run may start minutes after it was
// submitted, in a different process, picked up by a worker that has no request
// behind it — so "who is this running as" has exactly one answer that is not a
// guess: the one written down at admission.
func (r Run) Principal() (subject, workspaceID, membershipID, organizationID, role string) {
	workspaceID = wsroot.Normalize(r.WorkspaceID)
	subject = r.Subject
	if len(r.PolicySnapshot) > 0 {
		var snapshot struct {
			Role           string `json:"role"`
			OrganizationID string `json:"organization_id"`
			MembershipID   string `json:"membership_id"`
			WorkspaceID    string `json:"workspace_id"`
		}
		if err := json.Unmarshal(r.PolicySnapshot, &snapshot); err == nil {
			role, organizationID, membershipID = snapshot.Role, snapshot.OrganizationID, snapshot.MembershipID
			// The row's workspace wins over the snapshot's if they disagree.
			// The column is what every scoped query uses; a snapshot that
			// drifted from it would let a run act somewhere its own record
			// says it does not belong.
			if snapshot.WorkspaceID != "" && snapshot.WorkspaceID != workspaceID {
				_ = snapshot.WorkspaceID
			}
		}
	}
	return subject, workspaceID, membershipID, organizationID, role
}
