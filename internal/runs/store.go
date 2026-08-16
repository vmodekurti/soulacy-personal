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
	StatusQueued    = "queued"
	StatusRunning   = "running"
	StatusPaused    = "paused"
	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"
	StatusCancelled = "cancelled"
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
		StatusPaused: true, StatusSucceeded: true, StatusFailed: true, StatusCancelled: true,
	},
	StatusPaused: {
		StatusRunning: true, StatusCancelled: true, StatusFailed: true,
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
    id               TEXT PRIMARY KEY,
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
    ended_at         DATETIME
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
	if err := sqlitex.RecordSchemaVersion(db, "runs", 1); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
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
    status, cursor, payload, result, failure_reason, created_at, updated_at, started_at, ended_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		run.ID, run.WorkspaceID, run.AgentID, run.AgentVersion, run.SessionID, run.Subject,
		run.PrincipalKind, run.CredentialID, run.IdempotencyKey, string(run.PolicySnapshot), run.ReservationID,
		run.Status, run.Cursor, string(run.Payload), run.Result, run.FailureReason,
		run.CreatedAt, run.UpdatedAt, run.StartedAt, run.EndedAt,
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
    status, cursor, payload, result, failure_reason, created_at, updated_at, started_at, ended_at`

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
	if to == StatusRunning && current.StartedAt == nil {
		sets = append(sets, "started_at = ?")
		args = append(args, now)
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
	if err := row.Scan(&run.ID, &run.WorkspaceID, &run.AgentID, &run.AgentVersion, &run.SessionID,
		&run.Subject, &run.PrincipalKind, &run.CredentialID, &run.IdempotencyKey, &policy,
		&run.ReservationID, &run.Status, &run.Cursor, &payload, &run.Result, &run.FailureReason,
		&run.CreatedAt, &run.UpdatedAt, &started, &ended); err != nil {
		return Run{}, err
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
