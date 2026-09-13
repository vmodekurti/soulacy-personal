package autopilot

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/soulacy/soulacy/internal/redact"
)

type proofPayload struct {
	ID              string           `json:"id"`
	Subject         string           `json:"subject"`
	MissionID       string           `json:"mission_id,omitempty"`
	RunID           string           `json:"run_id"`
	AgentID         string           `json:"agent_id"`
	SessionID       string           `json:"session_id"`
	Outcome         ProofOutcome     `json:"outcome"`
	Simulation      bool             `json:"simulation"`
	Verification    CheckStatus      `json:"verification"`
	RevisionHash    string           `json:"revision_hash,omitempty"`
	Checks          []CheckResult    `json:"checks"`
	Evidence        []Evidence       `json:"evidence"`
	Tools           []ToolUse        `json:"tools"`
	ExternalChanges []ExternalChange `json:"external_changes"`
	CostUSD         *float64         `json:"cost_usd,omitempty"`
	DurationMS      *int64           `json:"duration_ms,omitempty"`
	CompletedAt     string           `json:"completed_at"`
}

func (p ProofRecord) payload() proofPayload {
	return proofPayload{
		ID: p.ID, Subject: p.Subject, MissionID: p.MissionID, RunID: p.RunID,
		AgentID: p.AgentID, SessionID: p.SessionID, Outcome: p.Outcome,
		Simulation:   p.Simulation,
		Verification: p.Verification, RevisionHash: p.RevisionHash,
		Checks: p.Checks, Evidence: p.Evidence, Tools: p.Tools,
		ExternalChanges: p.ExternalChanges, CostUSD: p.CostUSD,
		DurationMS: p.DurationMS, CompletedAt: timeString(p.CompletedAt),
	}
}

func proofFromPayload(p proofPayload, hash string) (ProofRecord, error) {
	completed, err := parseTime(p.CompletedAt)
	if err != nil {
		return ProofRecord{}, err
	}
	return ProofRecord{
		ID: p.ID, Subject: p.Subject, MissionID: p.MissionID, RunID: p.RunID,
		AgentID: p.AgentID, SessionID: p.SessionID, Outcome: p.Outcome,
		Simulation:   p.Simulation,
		Verification: p.Verification, RevisionHash: p.RevisionHash,
		Checks: p.Checks, Evidence: p.Evidence, Tools: p.Tools,
		ExternalChanges: p.ExternalChanges, CostUSD: p.CostUSD,
		DurationMS: p.DurationMS, CompletedAt: completed, ProofHash: hash,
	}, nil
}

func hashProofPayload(payload proofPayload) (string, []byte, error) {
	canonical, err := json.Marshal(payload)
	if err != nil {
		return "", nil, fmt.Errorf("autopilot: marshal proof: %w", err)
	}
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:]), canonical, nil
}

// VerifyProof recomputes the proof hash over all immutable payload fields.
func VerifyProof(proof ProofRecord) error {
	want, _, err := hashProofPayload(proof.payload())
	if err != nil {
		return err
	}
	if subtle.ConstantTimeCompare([]byte(want), []byte(proof.ProofHash)) != 1 {
		return fmt.Errorf("%w: proof %s", ErrIntegrity, proof.ID)
	}
	return nil
}

// SaveProof atomically writes one immutable proof and updates both overall and
// revision-scoped reliability counters. A run can have at most one proof for a
// subject/agent pair; retries must use a new run ID.
func (s *Store) SaveProof(ctx context.Context, input ProofInput) (ProofRecord, error) {
	proof, payload, err := s.prepareProof(input)
	if err != nil {
		return ProofRecord{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ProofRecord{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var claimAgent string
	var claimStatus RunClaimStatus
	err = tx.QueryRowContext(ctx, `
		SELECT agent_id, status FROM autopilot_run_claims WHERE subject = ? AND run_id = ?`,
		proof.Subject, proof.RunID).Scan(&claimAgent, &claimStatus)
	claimWasPresent := true
	if errors.Is(err, sql.ErrNoRows) {
		claimWasPresent = false
		// Importers may save an already-completed proof without executing a run.
		// Runtime callers still use ClaimRun before effects for replay safety.
		_, err = tx.ExecContext(ctx, `
			INSERT INTO autopilot_run_claims
			(subject, run_id, agent_id, status, proof_id, started_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, proof.Subject, proof.RunID, proof.AgentID,
			RunClaimFinalized, proof.ID, timeString(proof.CompletedAt), timeString(proof.CompletedAt))
		if err != nil {
			return ProofRecord{}, err
		}
	} else if err != nil {
		return ProofRecord{}, err
	} else {
		if claimAgent != proof.AgentID {
			return ProofRecord{}, fmt.Errorf("%w: run claim belongs to another agent", ErrConflict)
		}
		if claimStatus != RunClaimClaimed {
			return ProofRecord{}, fmt.Errorf("%w: run claim is %s", ErrConflict, claimStatus)
		}
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO autopilot_proofs
		(id, subject, mission_id, run_id, agent_id, session_id, revision_hash,
		 outcome, verification, simulation, completed_at, payload_json, proof_hash)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		proof.ID, proof.Subject, proof.MissionID, proof.RunID, proof.AgentID,
		proof.SessionID, proof.RevisionHash, proof.Outcome, proof.Verification, proof.Simulation,
		timeString(proof.CompletedAt), payload, proof.ProofHash)
	if err != nil {
		if isConstraintError(err) {
			return ProofRecord{}, fmt.Errorf("%w: a proof already exists for run %q", ErrConflict, proof.RunID)
		}
		return ProofRecord{}, err
	}
	if !proof.Simulation {
		if err := updateReliability(ctx, tx, proof, ""); err != nil {
			return ProofRecord{}, err
		}
		if proof.RevisionHash != "" {
			if err := updateReliability(ctx, tx, proof, proof.RevisionHash); err != nil {
				return ProofRecord{}, err
			}
		}
	}
	if claimWasPresent {
		res, err := tx.ExecContext(ctx, `
			UPDATE autopilot_run_claims SET status = ?, proof_id = ?, updated_at = ?
			WHERE subject = ? AND run_id = ? AND status = ?`,
			RunClaimFinalized, proof.ID, timeString(proof.CompletedAt),
			proof.Subject, proof.RunID, RunClaimClaimed)
		if err != nil {
			return ProofRecord{}, err
		}
		rows, err := res.RowsAffected()
		if err != nil {
			return ProofRecord{}, err
		}
		if rows != 1 {
			return ProofRecord{}, fmt.Errorf("%w: run claim changed concurrently", ErrConflict)
		}
	}
	if err := tx.Commit(); err != nil {
		return ProofRecord{}, err
	}
	return proof, nil
}

func (s *Store) prepareProof(input ProofInput) (ProofRecord, []byte, error) {
	input.Subject = strings.TrimSpace(input.Subject)
	input.AgentID = strings.TrimSpace(input.AgentID)
	input.RunID = strings.TrimSpace(input.RunID)
	if !validSubject(input.Subject) || input.AgentID == "" || input.RunID == "" {
		return ProofRecord{}, nil, fmt.Errorf("%w: subject, agent_id, and run_id are required", ErrInvalid)
	}
	if len(input.Subject) > 512 || len(input.AgentID) > 512 || len(input.RunID) > 1024 ||
		len(input.SessionID) > 1024 || len(input.MissionID) > 512 || len(input.RevisionHash) > 512 {
		return ProofRecord{}, nil, fmt.Errorf("%w: proof identifiers are too large", ErrInvalid)
	}
	if len(input.Checks) > 512 || len(input.Evidence) > 256 || len(input.Tools) > 1024 || len(input.ExternalChanges) > 256 {
		return ProofRecord{}, nil, fmt.Errorf("%w: proof collection exceeds its bounded size", ErrInvalid)
	}
	switch input.Outcome {
	case ProofSucceeded, ProofFailed, ProofCancelled:
	default:
		return ProofRecord{}, nil, fmt.Errorf("%w: unknown proof outcome %q", ErrInvalid, input.Outcome)
	}
	if input.CostUSD != nil && (*input.CostUSD < 0 || *input.CostUSD > maxMicrosCostUSD ||
		math.IsNaN(*input.CostUSD) || math.IsInf(*input.CostUSD, 0)) {
		return ProofRecord{}, nil, fmt.Errorf("%w: cost_usd must be finite and non-negative", ErrInvalid)
	}
	if input.DurationMS != nil && *input.DurationMS < 0 {
		return ProofRecord{}, nil, fmt.Errorf("%w: duration_ms cannot be negative", ErrInvalid)
	}
	for i, check := range input.Checks {
		if strings.TrimSpace(check.ID) == "" || strings.TrimSpace(string(check.Type)) == "" {
			return ProofRecord{}, nil, fmt.Errorf("%w: checks[%d] requires id and type", ErrInvalid, i)
		}
		if check.Status != CheckPass && check.Status != CheckFail && check.Status != CheckUnknown {
			return ProofRecord{}, nil, fmt.Errorf("%w: checks[%d] has unknown status %q", ErrInvalid, i, check.Status)
		}
		if len(check.ID) > 512 || len(check.Type) > 512 {
			return ProofRecord{}, nil, fmt.Errorf("%w: checks[%d] identifier is too large", ErrInvalid, i)
		}
	}
	completed := input.CompletedAt.UTC().Round(0)
	if input.CompletedAt.IsZero() {
		completed = s.timestamp()
	}
	id := strings.TrimSpace(input.ID)
	if id == "" {
		id = uuid.NewString()
	}
	if len(id) > 512 {
		return ProofRecord{}, nil, fmt.Errorf("%w: proof id is too large", ErrInvalid)
	}
	checks := append([]CheckResult(nil), input.Checks...)
	evidence := append([]Evidence(nil), input.Evidence...)
	tools := append([]ToolUse(nil), input.Tools...)
	changes := append([]ExternalChange(nil), input.ExternalChanges...)
	if checks == nil {
		checks = []CheckResult{}
	}
	if evidence == nil {
		evidence = []Evidence{}
	}
	if tools == nil {
		tools = []ToolUse{}
	}
	if changes == nil {
		changes = []ExternalChange{}
	}
	for i := range checks {
		checks[i].Description = boundedReceiptText(checks[i].Description, 2048)
		checks[i].Expected = boundedReceiptText(checks[i].Expected, 2048)
		checks[i].Actual = boundedReceiptText(checks[i].Actual, 2048)
		checks[i].Detail = boundedReceiptText(checks[i].Detail, 2048)
	}
	for i := range evidence {
		if evidence[i].ID == "" {
			evidence[i].ID = uuid.NewString()
		}
		if evidence[i].CreatedAt.IsZero() {
			evidence[i].CreatedAt = completed
		} else {
			evidence[i].CreatedAt = evidence[i].CreatedAt.UTC().Round(0)
		}
		evidence[i].Title = boundedReceiptText(evidence[i].Title, 512)
		evidence[i].URI = boundedReceiptText(evidence[i].URI, 2048)
		evidence[i].Summary = boundedReceiptText(evidence[i].Summary, 4096)
		evidence[i].ContentHash = boundedReceiptText(evidence[i].ContentHash, 512)
	}
	for i := range tools {
		tools[i].Name = boundedReceiptText(tools[i].Name, 512)
		tools[i].CallID = boundedReceiptText(tools[i].CallID, 512)
		tools[i].Status = boundedReceiptText(tools[i].Status, 128)
	}
	for i := range changes {
		if changes[i].ID == "" {
			changes[i].ID = uuid.NewString()
		}
		changes[i].Kind = boundedReceiptText(changes[i].Kind, 256)
		changes[i].Target = boundedReceiptText(changes[i].Target, 2048)
		changes[i].Summary = boundedReceiptText(changes[i].Summary, 4096)
	}
	proof := ProofRecord{
		ID: id, Subject: input.Subject, MissionID: strings.TrimSpace(input.MissionID),
		RunID: input.RunID, AgentID: input.AgentID, SessionID: input.SessionID,
		Outcome: input.Outcome, Simulation: input.Simulation,
		Verification: verificationFromChecks(checks),
		RevisionHash: input.RevisionHash, Checks: checks, Evidence: evidence,
		Tools: tools, ExternalChanges: changes, CostUSD: input.CostUSD,
		DurationMS: input.DurationMS, CompletedAt: completed,
	}
	hash, payload, err := hashProofPayload(proof.payload())
	if err != nil {
		return ProofRecord{}, nil, err
	}
	proof.ProofHash = hash
	return proof, payload, nil
}

func boundedReceiptText(value string, maxBytes int) string {
	value = redact.Text(value)
	if len(value) <= maxBytes {
		return value
	}
	value = value[:maxBytes]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func updateReliability(ctx context.Context, tx *sql.Tx, proof ProofRecord, revision string) error {
	success, verified, failed, unknown := 0, 0, 0, 0
	if proof.Outcome == ProofSucceeded {
		success = 1
	}
	switch proof.Verification {
	case CheckPass:
		verified = 1
	case CheckFail:
		failed = 1
	default:
		unknown = 1
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO autopilot_reliability
		(subject, agent_id, revision_hash, sample_count, success_count,
		 verified_count, verification_failed_count, verification_unknown_count, updated_at)
		VALUES (?, ?, ?, 1, ?, ?, ?, ?, ?)
		ON CONFLICT(subject, agent_id, revision_hash) DO UPDATE SET
		 sample_count = sample_count + 1,
		 success_count = success_count + excluded.success_count,
		 verified_count = verified_count + excluded.verified_count,
		 verification_failed_count = verification_failed_count + excluded.verification_failed_count,
		 verification_unknown_count = verification_unknown_count + excluded.verification_unknown_count,
		 updated_at = excluded.updated_at`,
		proof.Subject, proof.AgentID, revision, success, verified, failed, unknown,
		timeString(proof.CompletedAt))
	return err
}

// GetProof returns and verifies one subject-owned proof.
func (s *Store) GetProof(ctx context.Context, subject, id string) (ProofRecord, error) {
	if !validSubject(subject) || strings.TrimSpace(id) == "" {
		return ProofRecord{}, fmt.Errorf("%w: subject and proof id are required", ErrInvalid)
	}
	row := s.db.QueryRowContext(ctx,
		`SELECT payload_json, proof_hash FROM autopilot_proofs WHERE subject = ? AND id = ?`,
		subject, id)
	proof, err := scanProof(row)
	if err != nil {
		return ProofRecord{}, err
	}
	if proof.Subject != subject || proof.ID != id {
		return ProofRecord{}, fmt.Errorf("%w: proof index does not match payload", ErrIntegrity)
	}
	return proof, nil
}

// ListProofs returns newest proofs first and verifies every returned record.
func (s *Store) ListProofs(ctx context.Context, subject string, filter ProofFilter) ([]ProofRecord, error) {
	if !validSubject(subject) {
		return nil, fmt.Errorf("%w: subject is required", ErrInvalid)
	}
	if filter.Outcome != "" && filter.Outcome != ProofSucceeded && filter.Outcome != ProofFailed && filter.Outcome != ProofCancelled {
		return nil, fmt.Errorf("%w: unknown proof outcome %q", ErrInvalid, filter.Outcome)
	}
	if filter.Verification != "" && filter.Verification != CheckPass && filter.Verification != CheckFail && filter.Verification != CheckUnknown {
		return nil, fmt.Errorf("%w: unknown verification status %q", ErrInvalid, filter.Verification)
	}
	q := `SELECT payload_json, proof_hash FROM autopilot_proofs WHERE subject = ?`
	args := []any{subject}
	if filter.AgentID != "" {
		q += ` AND agent_id = ?`
		args = append(args, filter.AgentID)
	}
	if filter.RunID != "" {
		q += ` AND run_id = ?`
		args = append(args, filter.RunID)
	}
	if filter.RevisionHash != "" {
		q += ` AND revision_hash = ?`
		args = append(args, filter.RevisionHash)
	}
	if filter.Outcome != "" {
		q += ` AND outcome = ?`
		args = append(args, filter.Outcome)
	}
	if filter.Verification != "" {
		q += ` AND verification = ?`
		args = append(args, filter.Verification)
	}
	if filter.Simulation != nil {
		q += ` AND simulation = ?`
		args = append(args, *filter.Simulation)
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	q += ` ORDER BY completed_at DESC, id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]ProofRecord, 0)
	for rows.Next() {
		proof, err := scanProof(rows)
		if err != nil {
			return nil, err
		}
		if proof.Subject != subject || (filter.AgentID != "" && proof.AgentID != filter.AgentID) ||
			(filter.RunID != "" && proof.RunID != filter.RunID) ||
			(filter.RevisionHash != "" && proof.RevisionHash != filter.RevisionHash) ||
			(filter.Outcome != "" && proof.Outcome != filter.Outcome) ||
			(filter.Verification != "" && proof.Verification != filter.Verification) ||
			(filter.Simulation != nil && proof.Simulation != *filter.Simulation) {
			return nil, fmt.Errorf("%w: proof index does not match payload", ErrIntegrity)
		}
		out = append(out, proof)
	}
	return out, rows.Err()
}

type rowScanner interface{ Scan(...any) error }

func scanProof(row rowScanner) (ProofRecord, error) {
	var payloadJSON []byte
	var hash string
	if err := row.Scan(&payloadJSON, &hash); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ProofRecord{}, ErrNotFound
		}
		return ProofRecord{}, err
	}
	var payload proofPayload
	if err := json.Unmarshal(payloadJSON, &payload); err != nil {
		return ProofRecord{}, fmt.Errorf("%w: invalid stored payload: %v", ErrIntegrity, err)
	}
	proof, err := proofFromPayload(payload, hash)
	if err != nil {
		return ProofRecord{}, fmt.Errorf("%w: %v", ErrIntegrity, err)
	}
	if err := VerifyProof(proof); err != nil {
		return ProofRecord{}, err
	}
	return proof, nil
}

// Reliability returns overall metrics when revisionHash is empty and
// version-scoped metrics otherwise. Score is the product of observed run
// success and known verification rates; it is unknown if either rate is
// unknown, making a lack of verification impossible to display as confidence.
func (s *Store) Reliability(ctx context.Context, subject, agentID, revisionHash string) (ReliabilitySummary, error) {
	if !validSubject(subject) || strings.TrimSpace(agentID) == "" {
		return ReliabilitySummary{}, fmt.Errorf("%w: subject and agent_id are required", ErrInvalid)
	}
	r := ReliabilitySummary{Subject: subject, AgentID: agentID, RevisionHash: revisionHash}
	err := s.db.QueryRowContext(ctx, `
		SELECT sample_count, success_count, verified_count,
		       verification_failed_count, verification_unknown_count
		FROM autopilot_reliability
		WHERE subject = ? AND agent_id = ? AND revision_hash = ?`,
		subject, agentID, revisionHash).Scan(
		&r.SampleCount, &r.SuccessCount, &r.VerifiedCount,
		&r.VerificationFailedCount, &r.VerificationUnknownCount)
	if errors.Is(err, sql.ErrNoRows) {
		return r, nil
	}
	if err != nil {
		return ReliabilitySummary{}, err
	}
	if r.SampleCount > 0 {
		rate := float64(r.SuccessCount) / float64(r.SampleCount)
		r.SuccessRate = &rate
	}
	known := r.VerifiedCount + r.VerificationFailedCount
	if known > 0 {
		rate := float64(r.VerifiedCount) / float64(known)
		r.VerificationRate = &rate
	}
	if r.SuccessRate != nil && r.VerificationRate != nil {
		score := *r.SuccessRate * *r.VerificationRate
		r.Score = &score
	}
	return r, nil
}
