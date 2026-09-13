package autopilot

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/soulacy/soulacy/internal/redact"
	"github.com/soulacy/soulacy/pkg/agent"
)

func validProposalStatus(status ProposalStatus) bool {
	return status == ProposalPending || status == ProposalAccepted || status == ProposalRejected
}

// CreateProposal records a reviewable candidate derived from a subject-owned
// proof. It never modifies agent rules.
func (s *Store) CreateProposal(ctx context.Context, draft ProposalDraft) (LearningProposal, error) {
	draft.Subject = strings.TrimSpace(draft.Subject)
	draft.AgentID = strings.TrimSpace(draft.AgentID)
	draft.SourceProofID = strings.TrimSpace(draft.SourceProofID)
	draft.FailureSummary = strings.TrimSpace(draft.FailureSummary)
	if !validSubject(draft.Subject) || draft.AgentID == "" || draft.SourceProofID == "" || draft.FailureSummary == "" {
		return LearningProposal{}, fmt.Errorf("%w: subject, agent_id, source_proof_id, and failure_summary are required", ErrInvalid)
	}
	if len(draft.AgentID) > 512 || len(draft.SourceProofID) > 512 ||
		len(draft.FailureSummary) > 4096 || len(draft.RulePatch) > 64*1024 ||
		!utf8.ValidString(draft.FailureSummary) || !utf8.ValidString(draft.RulePatch) {
		return LearningProposal{}, fmt.Errorf("%w: proposal fields are too large or invalid UTF-8", ErrInvalid)
	}
	candidateJSON, err := json.Marshal(draft.CandidateCheck)
	if err != nil || len(candidateJSON) > 16*1024 {
		return LearningProposal{}, fmt.Errorf("%w: candidate check is too large", ErrInvalid)
	}
	if err := ValidateMission(&agent.MissionContract{Acceptance: []agent.MissionCheck{draft.CandidateCheck}}); err != nil {
		return LearningProposal{}, err
	}
	proof, err := s.GetProof(ctx, draft.Subject, draft.SourceProofID)
	if err != nil {
		return LearningProposal{}, err
	}
	if proof.AgentID != draft.AgentID {
		return LearningProposal{}, fmt.Errorf("%w: source proof belongs to another agent", ErrInvalid)
	}
	id := strings.TrimSpace(draft.ID)
	if id == "" {
		id = uuid.NewString()
	}
	now := s.timestamp()
	proposal := LearningProposal{
		ID: id, Subject: draft.Subject, AgentID: draft.AgentID,
		SourceProofID: draft.SourceProofID, FailureSummary: redact.Text(draft.FailureSummary),
		CandidateCheck: draft.CandidateCheck, RulePatch: redact.Text(draft.RulePatch),
		Baseline:  ProposalVerification{Status: CheckUnknown},
		Candidate: ProposalVerification{Status: CheckUnknown},
		Status:    ProposalPending, CreatedAt: now,
	}
	payload, err := json.Marshal(proposal)
	if err != nil {
		return LearningProposal{}, err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO autopilot_learning_proposals
		(id, subject, agent_id, source_proof_id, status, created_at, payload_json)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		proposal.ID, proposal.Subject, proposal.AgentID, proposal.SourceProofID,
		proposal.Status, timeString(proposal.CreatedAt), payload)
	if err != nil {
		if isConstraintError(err) {
			return LearningProposal{}, fmt.Errorf("%w: proposal id %q already exists", ErrConflict, id)
		}
		return LearningProposal{}, err
	}
	return proposal, nil
}

func (s *Store) GetProposal(ctx context.Context, subject, id string) (LearningProposal, error) {
	if !validSubject(subject) || strings.TrimSpace(id) == "" {
		return LearningProposal{}, fmt.Errorf("%w: subject and proposal id are required", ErrInvalid)
	}
	return scanProposal(s.db.QueryRowContext(ctx,
		`SELECT payload_json FROM autopilot_learning_proposals WHERE subject = ? AND id = ?`,
		subject, id))
}

func (s *Store) ListProposals(ctx context.Context, subject string, filter ProposalFilter) ([]LearningProposal, error) {
	if !validSubject(subject) {
		return nil, fmt.Errorf("%w: subject is required", ErrInvalid)
	}
	if filter.Status != "" && !validProposalStatus(filter.Status) {
		return nil, fmt.Errorf("%w: unknown proposal status %q", ErrInvalid, filter.Status)
	}
	q := `SELECT payload_json FROM autopilot_learning_proposals WHERE subject = ?`
	args := []any{subject}
	if filter.AgentID != "" {
		q += ` AND agent_id = ?`
		args = append(args, filter.AgentID)
	}
	if filter.Status != "" {
		q += ` AND status = ?`
		args = append(args, filter.Status)
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	q += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]LearningProposal, 0)
	for rows.Next() {
		proposal, err := scanProposal(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, proposal)
	}
	return out, rows.Err()
}

func scanProposal(row rowScanner) (LearningProposal, error) {
	var payload []byte
	if err := row.Scan(&payload); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return LearningProposal{}, ErrNotFound
		}
		return LearningProposal{}, err
	}
	var proposal LearningProposal
	if err := json.Unmarshal(payload, &proposal); err != nil {
		return LearningProposal{}, fmt.Errorf("autopilot: decode proposal: %w", err)
	}
	return proposal, nil
}

// SetProposalVerification records the baseline regression and candidate run
// verdicts while the proposal is pending.
func (s *Store) SetProposalVerification(ctx context.Context, subject, id string, baseline, candidate ProposalVerification) (LearningProposal, error) {
	if !validSubject(subject) || strings.TrimSpace(id) == "" {
		return LearningProposal{}, fmt.Errorf("%w: subject and proposal id are required", ErrInvalid)
	}
	if err := validateProposalVerification(baseline); err != nil {
		return LearningProposal{}, fmt.Errorf("%w: baseline: %v", ErrInvalid, err)
	}
	if err := validateProposalVerification(candidate); err != nil {
		return LearningProposal{}, fmt.Errorf("%w: candidate: %v", ErrInvalid, err)
	}
	now := s.timestamp()
	if baseline.VerifiedAt == nil {
		baseline.VerifiedAt = timePointer(now)
	}
	if candidate.VerifiedAt == nil {
		candidate.VerifiedAt = timePointer(now)
	}
	return s.updatePendingProposal(ctx, subject, id, func(p *LearningProposal) error {
		p.Baseline = baseline
		p.Candidate = candidate
		return nil
	})
}

func validateProposalVerification(v ProposalVerification) error {
	if v.Status != CheckPass && v.Status != CheckFail && v.Status != CheckUnknown {
		return fmt.Errorf("unknown status %q", v.Status)
	}
	if len(v.RunID) > 512 || len(v.Detail) > 4096 || !utf8.ValidString(v.Detail) {
		return fmt.Errorf("verification fields are too large or invalid UTF-8")
	}
	return nil
}

// DecideProposal accepts or rejects a candidate. Acceptance requires evidence
// that the baseline reproduces the failure and the candidate passes. This only
// records review state; applying RulePatch is an explicit separate concern.
func (s *Store) DecideProposal(ctx context.Context, subject, id string, decision ProposalStatus, reason string) (LearningProposal, error) {
	if decision != ProposalAccepted && decision != ProposalRejected {
		return LearningProposal{}, fmt.Errorf("%w: decision must be accepted or rejected", ErrInvalid)
	}
	if len(reason) > 4096 || !utf8.ValidString(reason) {
		return LearningProposal{}, fmt.Errorf("%w: decision reason is too large or invalid UTF-8", ErrInvalid)
	}
	return s.updatePendingProposal(ctx, subject, id, func(p *LearningProposal) error {
		if decision == ProposalAccepted && (p.Baseline.Status != CheckFail || p.Candidate.Status != CheckPass) {
			return fmt.Errorf("%w: acceptance requires a failing baseline and passing candidate", ErrInvalid)
		}
		now := s.timestamp()
		p.Status = decision
		p.DecisionReason = redact.Text(strings.TrimSpace(reason))
		p.ReviewedAt = &now
		return nil
	})
}

func (s *Store) updatePendingProposal(ctx context.Context, subject, id string, mutate func(*LearningProposal) error) (LearningProposal, error) {
	if !validSubject(subject) || strings.TrimSpace(id) == "" {
		return LearningProposal{}, fmt.Errorf("%w: subject and proposal id are required", ErrInvalid)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return LearningProposal{}, err
	}
	defer func() { _ = tx.Rollback() }()
	proposal, err := scanProposal(tx.QueryRowContext(ctx,
		`SELECT payload_json FROM autopilot_learning_proposals WHERE subject = ? AND id = ?`,
		subject, id))
	if err != nil {
		return LearningProposal{}, err
	}
	if proposal.Status != ProposalPending {
		return LearningProposal{}, fmt.Errorf("%w: proposal is already %s", ErrConflict, proposal.Status)
	}
	if err := mutate(&proposal); err != nil {
		return LearningProposal{}, err
	}
	payload, err := json.Marshal(proposal)
	if err != nil {
		return LearningProposal{}, err
	}
	var reviewed any
	if proposal.ReviewedAt != nil {
		reviewed = timeString(*proposal.ReviewedAt)
	}
	res, err := tx.ExecContext(ctx, `
		UPDATE autopilot_learning_proposals
		SET status = ?, reviewed_at = ?, payload_json = ?
		WHERE subject = ? AND id = ? AND status = ?`,
		proposal.Status, reviewed, payload, subject, id, ProposalPending)
	if err != nil {
		return LearningProposal{}, err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return LearningProposal{}, err
	}
	if rows != 1 {
		return LearningProposal{}, fmt.Errorf("%w: proposal was changed concurrently", ErrConflict)
	}
	if err := tx.Commit(); err != nil {
		return LearningProposal{}, err
	}
	return proposal, nil
}

func timePointer(t time.Time) *time.Time { return &t }
