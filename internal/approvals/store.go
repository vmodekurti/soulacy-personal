// Package approvals is the durable record of a tool call paused for a human
// decision (MU-022).
//
// Before this, an approval existed only as a map entry and a channel in the
// gateway process. That is enough for "the browser tab that started the run
// answers a dialog", and it is not enough for anything a team does:
//
//   - A restart lost every pending approval. The record vanished, the run's
//     channel vanished with it, and nobody was told: the approvals page simply
//     stopped listing something a person had been asked to decide.
//   - There was no workspace on the record at all. Listing and deciding were
//     gated on `admin`, computed as "role is owner or admin" with no tenant in
//     it — so an admin of ANY workspace could read every other workspace's
//     pending tool-call ARGUMENTS and approve them. Approval arguments are the
//     most sensitive payload the system holds by construction: they are the
//     things somebody thought were dangerous enough to stop.
//   - Eligibility was decided once, when the request was made. A member
//     removed from the workspace an hour ago could still answer.
//
// The record is what makes an approval a fact rather than a session artifact.
// It carries the workspace, the run, the tool, the redacted arguments, who
// asked, what permission answering it requires, when it expires, and — after
// the fact — who decided and when. Every one of those is a fact about the
// moment of the request that cannot be reconstructed afterwards.
package approvals

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/soulacy/soulacy/internal/sqlitex"
	"github.com/soulacy/soulacy/internal/workspacepurge"
	"github.com/soulacy/soulacy/internal/wsroot"
)

// Status values. The set is closed.
const (
	StatusPending = "pending"
	// StatusApproved and StatusDenied are a human's answer.
	StatusApproved = "approved"
	StatusDenied   = "denied"
	// StatusExpired is the clock's answer. Distinguished from denied because
	// "nobody looked at it in time" and "somebody said no" are different
	// facts, and an audit that conflates them cannot tell whether a control
	// was exercised or merely present.
	StatusExpired = "expired"
	// StatusInvalidated is the system's answer: the question stopped being
	// answerable. The run was cancelled, the requester lost their membership,
	// or policy changed under it.
	StatusInvalidated = "invalidated"
)

var (
	// ErrNotFound covers an unknown approval AND one belonging to another
	// workspace. Indistinguishable on purpose: telling a caller that an
	// approval exists but is not theirs turns approval IDs into an
	// enumeration oracle over other tenants' paused actions (invariant 8).
	ErrNotFound = errors.New("approvals: not found")
	// ErrNotPending reports a second decision on an approval that already has
	// one. This is what makes a decision single-use.
	ErrNotPending = errors.New("approvals: already decided")
	// ErrNotEligible reports a decider who is not currently permitted.
	ErrNotEligible = errors.New("approvals: not an eligible approver")
	// ErrFingerprintMismatch reports an execution whose tool or arguments are
	// not the ones that were approved.
	ErrFingerprintMismatch = errors.New("approvals: approved call does not match the call being executed")
)

// DefaultTTL bounds how long a paused tool call waits.
//
// An approval with no expiry is not "patient", it is a permanently open
// authorization: whoever finds it in six months can still release an action
// whose context nobody remembers. Fifteen minutes matches the attention span
// of the interaction that produced it — somebody is watching a run.
const DefaultTTL = 15 * time.Minute

// Approval is one paused tool call.
type Approval struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	// RunID and SessionID tie the approval to what it is blocking. A decision
	// about a run that no longer exists is not a decision anybody wants
	// honoured, which is why Invalidate keys on the run.
	RunID     string `json:"run_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	AgentID   string `json:"agent_id,omitempty"`

	Tool string `json:"tool"`
	// Args are REDACTED for display. The full arguments live only in the
	// process that is blocked on the decision; what an approver needs is
	// enough to judge, not enough to exfiltrate. See Redact.
	Args   map[string]any `json:"args,omitempty"`
	Reason string         `json:"reason,omitempty"`
	// Fingerprint binds the decision to the exact call. An approval that
	// merely names a tool authorizes every future use of it; this authorizes
	// one, and VerifyFingerprint is what enforces that at execution.
	Fingerprint string `json:"fingerprint"`

	// RequesterSubject is who was running when the call paused.
	RequesterSubject string `json:"requester_subject,omitempty"`
	// RequiredResource and RequiredAction are the permission answering this
	// demands, stored rather than derived at decision time so an approval
	// records what it asked for even after the policy that produced it
	// changes.
	RequiredResource string `json:"required_resource"`
	RequiredAction   string `json:"required_action"`

	Status string `json:"status"`
	// DecidedBy is the ACTOR, not the requester. An audit asking "who released
	// this" must not be answerable with "the person it was taken from".
	DecidedBy      string     `json:"decided_by,omitempty"`
	DecidedAt      *time.Time `json:"decided_at,omitempty"`
	DecisionReason string     `json:"decision_reason,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Pending reports whether this approval is still awaiting a decision, taking
// the clock into account.
//
// Expiry is evaluated on READ as well as being swept, because the sweep runs
// on a timer and the question "may this still be answered" is asked between
// ticks. A record that says pending and is past its expiry must not be
// answerable just because nothing has got round to relabelling it.
func (a Approval) Pending(now time.Time) bool {
	return a.Status == StatusPending && now.Before(a.ExpiresAt)
}

const schema = `
CREATE TABLE IF NOT EXISTS tool_approvals (
    id                 TEXT PRIMARY KEY,
    workspace_id       TEXT NOT NULL,
    run_id             TEXT NOT NULL DEFAULT '',
    session_id         TEXT NOT NULL DEFAULT '',
    agent_id           TEXT NOT NULL DEFAULT '',
    tool               TEXT NOT NULL,
    args_redacted      TEXT NOT NULL DEFAULT '',
    reason             TEXT NOT NULL DEFAULT '',
    fingerprint        TEXT NOT NULL,
    requester_subject  TEXT NOT NULL DEFAULT '',
    required_resource  TEXT NOT NULL DEFAULT '',
    required_action    TEXT NOT NULL DEFAULT '',
    status             TEXT NOT NULL,
    decided_by         TEXT NOT NULL DEFAULT '',
    decided_at         DATETIME,
    decision_reason    TEXT NOT NULL DEFAULT '',
    created_at         DATETIME NOT NULL,
    updated_at         DATETIME NOT NULL,
    expires_at         DATETIME NOT NULL
);
-- Workspace first in every index. A query that can only be answered by
-- scanning one tenant's rows cannot accidentally serve another's.
CREATE INDEX IF NOT EXISTS idx_approvals_ws_status  ON tool_approvals(workspace_id, status, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_approvals_ws_run     ON tool_approvals(workspace_id, run_id);
CREATE INDEX IF NOT EXISTS idx_approvals_ws_subject ON tool_approvals(workspace_id, requester_subject, created_at DESC);
`

// Store is the SQLite-backed approval record.
type Store struct{ db *sql.DB }

// Open creates or opens the approval store at path.
func Open(path string) (*Store, error) {
	// Decide reads the current status and then writes conditionally on it.
	// Under BEGIN DEFERRED two concurrent approvers both take a read lock and
	// then both try to upgrade, and SQLite fails the loser with "database is
	// locked" rather than letting it wait — turning "the second approver is
	// told it was already decided" into "the second approver sees an error".
	opts := sqlitex.DefaultOptions()
	opts.ImmediateTx = true
	db, err := sqlitex.Open(path, opts)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("approvals: schema: %w", err)
	}
	if err := sqlitex.RecordSchemaVersion(db, "approvals", 1); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// Close releases the database.
func (s *Store) Close() error { return s.db.Close() }

const selectColumns = `SELECT id, workspace_id, run_id, session_id, agent_id, tool,
    args_redacted, reason, fingerprint, requester_subject, required_resource, required_action,
    status, decided_by, decided_at, decision_reason, created_at, updated_at, expires_at`

// Request records a newly paused tool call.
//
// The caller supplies the FULL arguments; the store persists only the redacted
// form. Redaction happens here rather than at the call site so there is one
// answer to "what does a stored approval contain", and so a new caller cannot
// forget: the durable copy is redacted because this function is the only way
// to create one.
func (s *Store) Request(ctx context.Context, req Approval, fullArgs map[string]any) (Approval, error) {
	req.WorkspaceID = wsroot.Normalize(req.WorkspaceID)
	req.ID = strings.TrimSpace(req.ID)
	req.Tool = strings.TrimSpace(req.Tool)
	if req.ID == "" {
		return Approval{}, errors.New("approvals: an approval id is required")
	}
	if req.Tool == "" {
		return Approval{}, errors.New("approvals: a tool name is required")
	}
	now := time.Now().UTC()
	req.Status = StatusPending
	req.CreatedAt, req.UpdatedAt = now, now
	if req.ExpiresAt.IsZero() {
		req.ExpiresAt = now.Add(DefaultTTL)
	}
	req.Fingerprint = Fingerprint(req.Tool, fullArgs)
	req.Args = Redact(fullArgs)

	encoded, err := json.Marshal(req.Args)
	if err != nil {
		return Approval{}, fmt.Errorf("approvals: encode args: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `
INSERT INTO tool_approvals (id, workspace_id, run_id, session_id, agent_id, tool,
    args_redacted, reason, fingerprint, requester_subject, required_resource, required_action,
    status, decided_by, decided_at, decision_reason, created_at, updated_at, expires_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,'',NULL,'',?,?,?)`,
		req.ID, req.WorkspaceID, req.RunID, req.SessionID, req.AgentID, req.Tool,
		string(encoded), req.Reason, req.Fingerprint, req.RequesterSubject,
		req.RequiredResource, req.RequiredAction, req.Status,
		req.CreatedAt, req.UpdatedAt, req.ExpiresAt,
	); err != nil {
		return Approval{}, fmt.Errorf("approvals: request: %w", err)
	}
	return req, nil
}

// Get returns one approval within a workspace.
func (s *Store) Get(ctx context.Context, workspaceID, id string) (Approval, error) {
	row := s.db.QueryRowContext(ctx,
		selectColumns+` FROM tool_approvals WHERE workspace_id = ? AND id = ?`,
		wsroot.Normalize(workspaceID), strings.TrimSpace(id))
	approval, err := scan(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Approval{}, ErrNotFound
	}
	return approval, err
}

// ListPending returns the approvals a workspace is currently waiting on.
//
// Expired-but-unswept records are filtered out rather than returned with a
// stale status, so a client never renders a decision it cannot make.
func (s *Store) ListPending(ctx context.Context, workspaceID string) ([]Approval, error) {
	rows, err := s.db.QueryContext(ctx,
		selectColumns+` FROM tool_approvals WHERE workspace_id = ? AND status = ? ORDER BY created_at DESC`,
		wsroot.Normalize(workspaceID), StatusPending)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	now := time.Now().UTC()
	out := []Approval{}
	for rows.Next() {
		approval, err := scan(rows)
		if err != nil {
			return nil, err
		}
		if !approval.Pending(now) {
			continue
		}
		out = append(out, approval)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

// ListWorkspace returns the complete approval history for an export. Unlike
// ListPending it intentionally includes decided and expired records: those are
// the audit evidence a workspace export is expected to preserve.
func (s *Store) ListWorkspace(ctx context.Context, workspaceID string) ([]Approval, error) {
	rows, err := s.db.QueryContext(ctx,
		selectColumns+` FROM tool_approvals WHERE workspace_id = ? ORDER BY created_at DESC, id DESC`,
		wsroot.Normalize(workspaceID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Approval{}
	for rows.Next() {
		approval, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, approval)
	}
	return out, rows.Err()
}

// Eligibility is the authority a decider brings, resolved from CURRENT state
// by the caller. The store does not resolve it, because membership and roles
// live in the tenancy and RBAC packages and an approval store that reached
// into them would be a second, divergent copy of who may do what.
type Eligibility struct {
	// Subject is the deciding actor. Recorded as decided_by.
	Subject string
	// WorkspaceID is the workspace the decider's membership is in, resolved
	// now — not read from the request.
	WorkspaceID string
	// Permits reports whether this decider currently holds a permission.
	// A function rather than a role string so the RBAC matrix stays the single
	// definition of what a role means.
	Permits func(resource, action string) bool
}

// Decide records a human's answer.
//
// Single-use and concurrency-safe by construction: the UPDATE repeats
// `status = pending`, so exactly one of two simultaneous approvers changes a
// row and the other is told ErrNotPending rather than silently overwriting the
// first decision. Two approvers racing is not an edge case — it is what a
// notification fanned out to a team produces.
func (s *Store) Decide(ctx context.Context, workspaceID, id string, approved bool, by Eligibility, reason string) (Approval, error) {
	workspaceID = wsroot.Normalize(workspaceID)
	current, err := s.Get(ctx, workspaceID, id)
	if err != nil {
		return Approval{}, err
	}

	// Eligibility is checked against the decider's CURRENT authority, not the
	// authority they had when the approval was created. A member removed from
	// the workspace this morning must not be able to release something that
	// paused last night.
	if err := check(current, by); err != nil {
		return Approval{}, err
	}

	now := time.Now().UTC()
	if !current.Pending(now) {
		if current.Status == StatusPending {
			// Past its expiry but not yet swept. Relabel it so the caller and
			// every later reader see the same thing.
			_, _ = s.db.ExecContext(ctx,
				`UPDATE tool_approvals SET status = ?, updated_at = ? WHERE workspace_id = ? AND id = ? AND status = ?`,
				StatusExpired, now, workspaceID, id, StatusPending)
		}
		return Approval{}, ErrNotPending
	}

	status := StatusDenied
	if approved {
		status = StatusApproved
	}
	if err := s.recordDecision(ctx, workspaceID, id, status, by.Subject, reason, now); err != nil {
		return Approval{}, err
	}
	return s.Get(ctx, workspaceID, id)
}

// recordDecision is the conditional write that makes a decision single-use.
//
// Split out from Decide so the property can be tested WITHOUT the read-then-
// check above it. That check refuses most second decisions, which is exactly
// why it hides this one: with the two together, deleting `AND status = ?`
// changes nothing observable in ordinary use and everything under a genuine
// race — two approvers answering a team notification at the same moment, where
// both pass the read and one silently overwrites the other's answer along with
// their name on it.
func (s *Store) recordDecision(ctx context.Context, workspaceID, id, status, by, reason string, now time.Time) error {
	res, err := s.db.ExecContext(ctx, `
UPDATE tool_approvals
   SET status = ?, decided_by = ?, decided_at = ?, decision_reason = ?, updated_at = ?
 WHERE workspace_id = ? AND id = ? AND status = ?`,
		status, by, now, strings.TrimSpace(reason), now, workspaceID, id, StatusPending)
	if err != nil {
		return err
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		return ErrNotPending
	}
	return nil
}

// Authorize answers "may this actor see or decide this approval, right now".
//
// Exported because the broker's store-less path — a personal deployment with
// no durable record — has to answer the same question about a map entry. Two
// implementations of "may this actor decide" is how one of them ends up
// missing the workspace comparison, which is exactly the bug this package
// exists to fix.
func Authorize(approval Approval, by Eligibility) error { return check(approval, by) }

// check answers "may this actor decide this approval, right now".
func check(approval Approval, by Eligibility) error {
	if strings.TrimSpace(by.Subject) == "" {
		return ErrNotEligible
	}
	// The decider's workspace comes from their resolved membership. Comparing
	// it to the approval's workspace is what stops one tenant's admin
	// answering another's — the hole the in-memory broker had, where `admin`
	// was a role with no tenant in it.
	if wsroot.Normalize(by.WorkspaceID) != approval.WorkspaceID {
		return ErrNotFound
	}
	if by.Permits == nil {
		return ErrNotEligible
	}
	if !by.Permits(approval.RequiredResource, approval.RequiredAction) {
		return ErrNotEligible
	}
	return nil
}

// CanView reports whether an actor may see an approval at all.
//
// The same tenant check as deciding, and deliberately the same permission:
// the arguments of a paused call are, by construction, the details of
// something somebody thought was dangerous. "Read-only visibility into what
// the team is being asked to authorize" is not a lesser privilege.
func CanView(approval Approval, by Eligibility) bool {
	return check(approval, by) == nil
}

// VerifyFingerprint reports whether an approved decision covers the exact call
// about to be executed.
//
// Criterion 5 exists because an approval that names only a tool authorizes
// every future use of it. Between the human clicking approve and the engine
// running the call, the arguments must not have changed — whether through a
// race, a retry that rebuilt them, or a model that re-emitted the call
// differently.
func (a Approval) VerifyFingerprint(tool string, args map[string]any) error {
	if a.Status != StatusApproved {
		return ErrNotPending
	}
	if Fingerprint(tool, args) != a.Fingerprint {
		return ErrFingerprintMismatch
	}
	return nil
}

// Fingerprint is a stable digest of a tool call.
//
// json.Marshal sorts map keys, so the encoding is canonical for any argument
// shape the tool layer produces without needing a bespoke canonicaliser. The
// tool name is length-prefixed rather than concatenated so that a tool named
// "a" with args {"b":1} and a tool named "ab" cannot collide.
func Fingerprint(tool string, args map[string]any) string {
	encoded, err := json.Marshal(args)
	if err != nil {
		// An un-encodable argument set must not produce a digest that
		// something else can match. A per-call error string cannot collide
		// with a real fingerprint, so the call simply never verifies.
		encoded = []byte(fmt.Sprintf("unencodable:%v", err))
	}
	sum := sha256.New()
	fmt.Fprintf(sum, "%d:%s|", len(tool), tool)
	sum.Write(encoded)
	return hex.EncodeToString(sum.Sum(nil))
}

func scan(row interface{ Scan(...any) error }) (Approval, error) {
	var a Approval
	var args string
	var decidedAt sql.NullTime
	if err := row.Scan(&a.ID, &a.WorkspaceID, &a.RunID, &a.SessionID, &a.AgentID, &a.Tool,
		&args, &a.Reason, &a.Fingerprint, &a.RequesterSubject, &a.RequiredResource, &a.RequiredAction,
		&a.Status, &a.DecidedBy, &decidedAt, &a.DecisionReason,
		&a.CreatedAt, &a.UpdatedAt, &a.ExpiresAt); err != nil {
		return Approval{}, err
	}
	if args != "" {
		_ = json.Unmarshal([]byte(args), &a.Args)
	}
	if decidedAt.Valid {
		a.DecidedAt = &decidedAt.Time
	}
	return a, nil
}

// PurgeWorkspace removes every row this store holds for one workspace.
//
// MU-032 criterion 4. The TABLE LIST comes from the ownership catalog rather
// than from a literal here, so a table added to the "approvals" resource is
// purged the day it is classified — one edit, not two. A hand-written list is
// the same second-inventory mistake the exporter avoids, and here the
// consequence of drift is data outliving a deletion somebody was told
// completed.
func (s *Store) PurgeWorkspace(ctx context.Context, workspaceID string) (workspacepurge.Removed, error) {
	return workspacepurge.PurgeCatalogTables(ctx, s.db, "approvals", workspaceID)
}
