package autopilot

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// ClaimRun durably claims a subject/run ID before any external effect. A
// duplicate always returns ErrConflict, including finalized and uncertain
// claims, so retries after network uncertainty cannot replay side effects.
func (s *Store) ClaimRun(ctx context.Context, subject, runID, agentID string) error {
	subject = strings.TrimSpace(subject)
	runID = strings.TrimSpace(runID)
	agentID = strings.TrimSpace(agentID)
	if !validSubject(subject) || runID == "" || agentID == "" {
		return fmt.Errorf("%w: subject, run_id, and agent_id are required", ErrInvalid)
	}
	if len(subject) > 512 || len(runID) > 1024 || len(agentID) > 512 {
		return fmt.Errorf("%w: run claim identifiers are too large", ErrInvalid)
	}
	now := timeString(s.timestamp())
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO autopilot_run_claims
		(subject, run_id, agent_id, status, started_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)`, subject, runID, agentID, RunClaimClaimed, now, now)
	if err != nil {
		if isConstraintError(err) {
			return fmt.Errorf("%w: run %q is already claimed", ErrConflict, runID)
		}
		return err
	}
	return nil
}

// ListUnfinishedRunClaims exposes crash-uncertain work for manual recovery.
// It never changes or replays the claims.
func (s *Store) ListUnfinishedRunClaims(ctx context.Context, subject string, limit int) ([]RunClaim, error) {
	if !validSubject(subject) {
		return nil, fmt.Errorf("%w: subject is required", ErrInvalid)
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT subject, run_id, agent_id, status, proof_id, started_at, updated_at
		FROM autopilot_run_claims
		WHERE subject = ? AND status IN (?, ?)
		ORDER BY updated_at DESC, run_id DESC LIMIT ?`,
		subject, RunClaimClaimed, RunClaimUncertain, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]RunClaim, 0)
	for rows.Next() {
		claim, err := scanRunClaim(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, claim)
	}
	return out, rows.Err()
}

func scanRunClaim(row rowScanner) (RunClaim, error) {
	var claim RunClaim
	var started, updated string
	if err := row.Scan(&claim.Subject, &claim.RunID, &claim.AgentID, &claim.Status,
		&claim.ProofID, &started, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return RunClaim{}, ErrNotFound
		}
		return RunClaim{}, err
	}
	var err error
	if claim.StartedAt, err = parseTime(started); err != nil {
		return RunClaim{}, err
	}
	if claim.UpdatedAt, err = parseTime(updated); err != nil {
		return RunClaim{}, err
	}
	return claim, nil
}
