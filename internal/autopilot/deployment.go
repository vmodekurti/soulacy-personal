package autopilot

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/soulacy/soulacy/pkg/agent"
)

// CanonicalDefinition validates and compacts definition JSON without changing
// object-key order. json.Marshal(definition) therefore hashes identically in
// the runtime and deployment store. Whitespace-only differences are removed.
func CanonicalDefinition(raw json.RawMessage) (json.RawMessage, string, error) {
	if len(raw) == 0 || !json.Valid(raw) {
		return nil, "", fmt.Errorf("%w: definition_json must be valid JSON", ErrInvalid)
	}
	var identity struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &identity); err != nil || strings.TrimSpace(identity.ID) == "" {
		return nil, "", fmt.Errorf("%w: definition_json must be an agent object with id", ErrInvalid)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		return nil, "", fmt.Errorf("%w: compact definition_json: %v", ErrInvalid, err)
	}
	canonical := append(json.RawMessage(nil), compact.Bytes()...)
	sum := sha256.Sum256(canonical)
	return canonical, "sha256:" + hex.EncodeToString(sum[:]), nil
}

func validChannel(channel DeploymentChannel) bool {
	switch channel {
	case ChannelDraft, ChannelSimulation, ChannelCanary, ChannelStable:
		return true
	default:
		return false
	}
}

func sourceChannel(channel DeploymentChannel) DeploymentChannel {
	switch channel {
	case ChannelSimulation:
		return ChannelDraft
	case ChannelCanary:
		return ChannelSimulation
	case ChannelStable:
		return ChannelCanary
	default:
		return ""
	}
}

// CreateDeploymentVersion appends an immutable definition snapshot and points
// the draft channel at it. An identical revision cannot be registered twice
// for the same subject and agent.
func (s *Store) CreateDeploymentVersion(ctx context.Context, input DeploymentVersionInput) (DeploymentVersion, error) {
	input.Subject = strings.TrimSpace(input.Subject)
	input.AgentID = strings.TrimSpace(input.AgentID)
	input.Version = strings.TrimSpace(input.Version)
	if !validSubject(input.Subject) || input.AgentID == "" || input.Version == "" {
		return DeploymentVersion{}, fmt.Errorf("%w: subject, agent_id, and version are required", ErrInvalid)
	}
	if len(input.AgentID) > 512 || len(input.Version) > 256 || len(input.DefinitionJSON) > 2*1024*1024 {
		return DeploymentVersion{}, fmt.Errorf("%w: deployment metadata or definition is too large", ErrInvalid)
	}
	definition, revision, err := CanonicalDefinition(input.DefinitionJSON)
	if err != nil {
		return DeploymentVersion{}, err
	}
	var def agent.Definition
	if err := json.Unmarshal(definition, &def); err != nil {
		return DeploymentVersion{}, fmt.Errorf("%w: decode agent definition: %v", ErrInvalid, err)
	}
	if def.ID != input.AgentID {
		return DeploymentVersion{}, fmt.Errorf("%w: definition id %q does not match agent_id %q", ErrInvalid, def.ID, input.AgentID)
	}
	if err := ValidateMission(def.Mission); err != nil {
		return DeploymentVersion{}, err
	}
	id := strings.TrimSpace(input.ID)
	if id == "" {
		id = uuid.NewString()
	}
	now := s.timestamp()
	version := DeploymentVersion{
		ID: id, Subject: input.Subject, AgentID: input.AgentID, Version: input.Version,
		RevisionHash: revision, DefinitionJSON: definition, CreatedAt: now,
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return DeploymentVersion{}, err
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `
		INSERT INTO autopilot_deployment_versions
		(id, subject, agent_id, version, revision_hash, definition_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, version.ID, version.Subject, version.AgentID,
		version.Version, version.RevisionHash, []byte(version.DefinitionJSON), timeString(now))
	if err != nil {
		if isConstraintError(err) {
			return DeploymentVersion{}, fmt.Errorf("%w: deployment id or revision already exists", ErrConflict)
		}
		return DeploymentVersion{}, err
	}
	previous, err := currentChannelVersion(ctx, tx, version.Subject, version.AgentID, ChannelDraft)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return DeploymentVersion{}, err
	}
	gatesJSON, _ := json.Marshal(PromotionGates{})
	_, err = tx.ExecContext(ctx, `
		INSERT INTO autopilot_deployment_channels
		(subject, agent_id, channel, current_version_id, previous_version_id, traffic_percent, gates_json, updated_at)
		VALUES (?, ?, ?, ?, ?, 100, ?, ?)
		ON CONFLICT(subject, agent_id, channel) DO UPDATE SET
		 previous_version_id = current_version_id,
		 current_version_id = excluded.current_version_id,
		 traffic_percent = 100,
		 gates_json = excluded.gates_json,
		 updated_at = excluded.updated_at`,
		version.Subject, version.AgentID, ChannelDraft, version.ID, previous, gatesJSON, timeString(now))
	if err != nil {
		return DeploymentVersion{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO autopilot_deployment_controls(subject, agent_id, frozen, updated_at)
		VALUES (?, ?, 0, ?) ON CONFLICT(subject, agent_id) DO NOTHING`,
		version.Subject, version.AgentID, timeString(now)); err != nil {
		return DeploymentVersion{}, err
	}
	if err := insertDeploymentEvent(ctx, tx, version.Subject, version.AgentID, ChannelDraft,
		"create", previous, version.ID, "", now); err != nil {
		return DeploymentVersion{}, err
	}
	if err := tx.Commit(); err != nil {
		return DeploymentVersion{}, err
	}
	return version, nil
}

func (s *Store) GetDeploymentVersion(ctx context.Context, subject, id string) (DeploymentVersion, error) {
	if !validSubject(subject) || strings.TrimSpace(id) == "" {
		return DeploymentVersion{}, fmt.Errorf("%w: subject and deployment version id are required", ErrInvalid)
	}
	return scanDeploymentVersion(s.db.QueryRowContext(ctx, `
		SELECT id, subject, agent_id, version, revision_hash, definition_json, created_at
		FROM autopilot_deployment_versions WHERE subject = ? AND id = ?`, subject, id))
}

func (s *Store) ListDeploymentVersions(ctx context.Context, subject, agentID string, limit int) ([]DeploymentVersion, error) {
	if !validSubject(subject) {
		return nil, fmt.Errorf("%w: subject is required", ErrInvalid)
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	query := `
		SELECT id, subject, agent_id, version, revision_hash, definition_json, created_at
		FROM autopilot_deployment_versions WHERE subject = ?`
	args := []any{subject}
	if strings.TrimSpace(agentID) != "" {
		query += ` AND agent_id = ?`
		args = append(args, strings.TrimSpace(agentID))
	}
	query += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]DeploymentVersion, 0)
	for rows.Next() {
		version, err := scanDeploymentVersion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, version)
	}
	return out, rows.Err()
}

func scanDeploymentVersion(row rowScanner) (DeploymentVersion, error) {
	var version DeploymentVersion
	var definition []byte
	var created string
	if err := row.Scan(&version.ID, &version.Subject, &version.AgentID, &version.Version,
		&version.RevisionHash, &definition, &created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return DeploymentVersion{}, ErrNotFound
		}
		return DeploymentVersion{}, err
	}
	var err error
	version.DefinitionJSON = append(json.RawMessage(nil), definition...)
	version.CreatedAt, err = parseTime(created)
	return version, err
}

func (s *Store) DeploymentStatus(ctx context.Context, subject, agentID string) (DeploymentStatus, error) {
	if !validSubject(subject) || strings.TrimSpace(agentID) == "" {
		return DeploymentStatus{}, fmt.Errorf("%w: subject and agent_id are required", ErrInvalid)
	}
	status := DeploymentStatus{Subject: subject, AgentID: agentID, Channels: []DeploymentChannelState{}}
	var frozen int
	err := s.db.QueryRowContext(ctx, `
		SELECT frozen, freeze_reason FROM autopilot_deployment_controls
		WHERE subject = ? AND agent_id = ?`, subject, agentID).Scan(&frozen, &status.FreezeReason)
	if errors.Is(err, sql.ErrNoRows) {
		var exists int
		if scanErr := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM autopilot_deployment_versions WHERE subject = ? AND agent_id = ?`,
			subject, agentID).Scan(&exists); scanErr != nil {
			return DeploymentStatus{}, scanErr
		}
		if exists == 0 {
			return DeploymentStatus{}, ErrNotFound
		}
	} else if err != nil {
		return DeploymentStatus{}, err
	}
	status.Frozen = frozen != 0
	rows, err := s.db.QueryContext(ctx, `
		SELECT channel, current_version_id, previous_version_id, traffic_percent, gates_json, updated_at
		FROM autopilot_deployment_channels WHERE subject = ? AND agent_id = ?
		ORDER BY CASE channel WHEN 'draft' THEN 1 WHEN 'simulation' THEN 2 WHEN 'canary' THEN 3 ELSE 4 END`,
		subject, agentID)
	if err != nil {
		return DeploymentStatus{}, err
	}
	defer rows.Close()
	for rows.Next() {
		channel, err := scanDeploymentChannel(rows)
		if err != nil {
			return DeploymentStatus{}, err
		}
		status.Channels = append(status.Channels, channel)
	}
	return status, rows.Err()
}

func (s *Store) ListDeploymentStatuses(ctx context.Context, subject string) ([]DeploymentStatus, error) {
	if !validSubject(subject) {
		return nil, fmt.Errorf("%w: subject is required", ErrInvalid)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT agent_id FROM autopilot_deployment_controls WHERE subject = ?
		UNION SELECT agent_id FROM autopilot_deployment_versions WHERE subject = ?
		ORDER BY agent_id`, subject, subject)
	if err != nil {
		return nil, err
	}
	var agents []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		agents = append(agents, id)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	out := make([]DeploymentStatus, 0, len(agents))
	for _, agentID := range agents {
		status, err := s.DeploymentStatus(ctx, subject, agentID)
		if err != nil {
			return nil, err
		}
		out = append(out, status)
	}
	return out, nil
}

func scanDeploymentChannel(row rowScanner) (DeploymentChannelState, error) {
	var state DeploymentChannelState
	var gates []byte
	var updated string
	if err := row.Scan(&state.Channel, &state.CurrentVersionID, &state.PreviousVersionID,
		&state.TrafficPercent, &gates, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return DeploymentChannelState{}, ErrNotFound
		}
		return DeploymentChannelState{}, err
	}
	if err := json.Unmarshal(gates, &state.Gates); err != nil {
		return DeploymentChannelState{}, fmt.Errorf("autopilot: decode promotion gates: %w", err)
	}
	var err error
	state.UpdatedAt, err = parseTime(updated)
	return state, err
}

// PromoteDeployment moves an immutable version through the ordered
// draft→simulation→canary→stable channels after evaluating explicit gates.
func (s *Store) PromoteDeployment(ctx context.Context, request PromotionRequest) (DeploymentChannelState, GateEvaluation, error) {
	request.Subject = strings.TrimSpace(request.Subject)
	request.AgentID = strings.TrimSpace(request.AgentID)
	request.VersionID = strings.TrimSpace(request.VersionID)
	if !validSubject(request.Subject) || request.AgentID == "" || request.VersionID == "" {
		return DeploymentChannelState{}, GateEvaluation{}, fmt.Errorf("%w: subject, agent_id, and version_id are required", ErrInvalid)
	}
	if !validChannel(request.ToChannel) || request.ToChannel == ChannelDraft {
		return DeploymentChannelState{}, GateEvaluation{}, fmt.Errorf("%w: promotion target must be simulation, canary, or stable", ErrInvalid)
	}
	if err := validatePromotionGates(request.Gates); err != nil {
		return DeploymentChannelState{}, GateEvaluation{}, err
	}
	if request.ToChannel == ChannelCanary {
		if request.TrafficPercent == 0 {
			request.TrafficPercent = 10
		}
		if request.TrafficPercent < 1 || request.TrafficPercent > 99 {
			return DeploymentChannelState{}, GateEvaluation{}, fmt.Errorf("%w: canary traffic_percent must be between 1 and 99", ErrInvalid)
		}
	} else {
		request.TrafficPercent = 100
	}
	version, err := s.GetDeploymentVersion(ctx, request.Subject, request.VersionID)
	if err != nil {
		return DeploymentChannelState{}, GateEvaluation{}, err
	}
	if version.AgentID != request.AgentID {
		return DeploymentChannelState{}, GateEvaluation{}, ErrNotFound
	}
	evaluation, err := s.evaluateGates(ctx, request.Subject, request.AgentID, version.RevisionHash, request.Gates)
	if err != nil {
		return DeploymentChannelState{}, GateEvaluation{}, err
	}
	if !evaluation.Passed {
		return DeploymentChannelState{}, evaluation, fmt.Errorf("%w: promotion gates did not pass", ErrConflict)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return DeploymentChannelState{}, evaluation, err
	}
	defer func() { _ = tx.Rollback() }()
	var frozen int
	if err := tx.QueryRowContext(ctx, `SELECT frozen FROM autopilot_deployment_controls WHERE subject = ? AND agent_id = ?`,
		request.Subject, request.AgentID).Scan(&frozen); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return DeploymentChannelState{}, evaluation, err
	}
	if frozen != 0 {
		return DeploymentChannelState{}, evaluation, ErrFrozen
	}
	fromChannel := sourceChannel(request.ToChannel)
	fromVersion, err := currentChannelVersion(ctx, tx, request.Subject, request.AgentID, fromChannel)
	if err != nil {
		return DeploymentChannelState{}, evaluation, fmt.Errorf("%w: version is not active in %s", ErrConflict, fromChannel)
	}
	if fromVersion != request.VersionID {
		return DeploymentChannelState{}, evaluation, fmt.Errorf("%w: %s points to a different version", ErrConflict, fromChannel)
	}
	previous, err := currentChannelVersion(ctx, tx, request.Subject, request.AgentID, request.ToChannel)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return DeploymentChannelState{}, evaluation, err
	}
	if previous == request.VersionID {
		state, err := scanDeploymentChannel(tx.QueryRowContext(ctx, `
			SELECT channel, current_version_id, previous_version_id, traffic_percent, gates_json, updated_at
			FROM autopilot_deployment_channels WHERE subject = ? AND agent_id = ? AND channel = ?`,
			request.Subject, request.AgentID, request.ToChannel))
		return state, evaluation, err
	}
	gatesJSON, err := json.Marshal(request.Gates)
	if err != nil {
		return DeploymentChannelState{}, evaluation, err
	}
	now := s.timestamp()
	_, err = tx.ExecContext(ctx, `
		INSERT INTO autopilot_deployment_channels
		(subject, agent_id, channel, current_version_id, previous_version_id, traffic_percent, gates_json, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(subject, agent_id, channel) DO UPDATE SET
		 previous_version_id = current_version_id,
		 current_version_id = excluded.current_version_id,
		 traffic_percent = excluded.traffic_percent,
		 gates_json = excluded.gates_json,
		 updated_at = excluded.updated_at`,
		request.Subject, request.AgentID, request.ToChannel, request.VersionID, previous,
		request.TrafficPercent, gatesJSON, timeString(now))
	if err != nil {
		return DeploymentChannelState{}, evaluation, err
	}
	if err := insertDeploymentEvent(ctx, tx, request.Subject, request.AgentID, request.ToChannel,
		"promote", previous, request.VersionID, strings.Join(evaluation.Reasons, "; "), now); err != nil {
		return DeploymentChannelState{}, evaluation, err
	}
	state, err := scanDeploymentChannel(tx.QueryRowContext(ctx, `
		SELECT channel, current_version_id, previous_version_id, traffic_percent, gates_json, updated_at
		FROM autopilot_deployment_channels WHERE subject = ? AND agent_id = ? AND channel = ?`,
		request.Subject, request.AgentID, request.ToChannel))
	if err != nil {
		return DeploymentChannelState{}, evaluation, err
	}
	if err := tx.Commit(); err != nil {
		return DeploymentChannelState{}, evaluation, err
	}
	return state, evaluation, nil
}

func validatePromotionGates(gates PromotionGates) error {
	if gates.MinSamples < 0 {
		return fmt.Errorf("%w: min_samples cannot be negative", ErrInvalid)
	}
	for name, rate := range map[string]*float64{
		"min_success_rate": gates.MinSuccessRate, "min_verification_rate": gates.MinVerificationRate,
	} {
		if rate != nil && (*rate < 0 || *rate > 1 || math.IsNaN(*rate) || math.IsInf(*rate, 0)) {
			return fmt.Errorf("%w: %s must be finite and between 0 and 1", ErrInvalid, name)
		}
	}
	return nil
}

func (s *Store) evaluateGates(ctx context.Context, subject, agentID, revision string, gates PromotionGates) (GateEvaluation, error) {
	reliability, err := s.Reliability(ctx, subject, agentID, revision)
	if err != nil {
		return GateEvaluation{}, err
	}
	result := GateEvaluation{Passed: true, Reasons: []string{}, Reliability: reliability}
	if reliability.SampleCount < gates.MinSamples {
		result.Passed = false
		result.Reasons = append(result.Reasons, fmt.Sprintf("sample_count %d is below %d", reliability.SampleCount, gates.MinSamples))
	}
	if gates.MinSuccessRate != nil && (reliability.SuccessRate == nil || *reliability.SuccessRate < *gates.MinSuccessRate) {
		result.Passed = false
		result.Reasons = append(result.Reasons, "success rate is unknown or below the gate")
	}
	if gates.MinVerificationRate != nil && (reliability.VerificationRate == nil || *reliability.VerificationRate < *gates.MinVerificationRate) {
		result.Passed = false
		result.Reasons = append(result.Reasons, "verification rate is unknown or below the gate")
	}
	if result.Passed {
		result.Reasons = append(result.Reasons, "all promotion gates passed")
	}
	return result, nil
}

// RollbackDeployment atomically swaps a channel back to its previous immutable
// version. Rollback remains available while frozen for emergency recovery.
func (s *Store) RollbackDeployment(ctx context.Context, request RollbackRequest) (DeploymentChannelState, error) {
	request.Subject = strings.TrimSpace(request.Subject)
	request.AgentID = strings.TrimSpace(request.AgentID)
	if !validSubject(request.Subject) || request.AgentID == "" || !validChannel(request.Channel) {
		return DeploymentChannelState{}, fmt.Errorf("%w: subject, agent_id, and valid channel are required", ErrInvalid)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return DeploymentChannelState{}, err
	}
	defer func() { _ = tx.Rollback() }()
	state, err := scanDeploymentChannel(tx.QueryRowContext(ctx, `
		SELECT channel, current_version_id, previous_version_id, traffic_percent, gates_json, updated_at
		FROM autopilot_deployment_channels WHERE subject = ? AND agent_id = ? AND channel = ?`,
		request.Subject, request.AgentID, request.Channel))
	if err != nil {
		return DeploymentChannelState{}, err
	}
	if request.ExpectedVersionID != "" && state.CurrentVersionID != request.ExpectedVersionID {
		return DeploymentChannelState{}, fmt.Errorf("%w: selected version is no longer current; refresh before rolling back", ErrConflict)
	}
	if state.PreviousVersionID == "" {
		return DeploymentChannelState{}, fmt.Errorf("%w: channel has no previous version", ErrConflict)
	}
	now := s.timestamp()
	_, err = tx.ExecContext(ctx, `
		UPDATE autopilot_deployment_channels
		SET current_version_id = ?, previous_version_id = ?, updated_at = ?
		WHERE subject = ? AND agent_id = ? AND channel = ?`,
		state.PreviousVersionID, state.CurrentVersionID, timeString(now),
		request.Subject, request.AgentID, request.Channel)
	if err != nil {
		return DeploymentChannelState{}, err
	}
	if err := insertDeploymentEvent(ctx, tx, request.Subject, request.AgentID, request.Channel,
		"rollback", state.CurrentVersionID, state.PreviousVersionID, strings.TrimSpace(request.Reason), now); err != nil {
		return DeploymentChannelState{}, err
	}
	state.CurrentVersionID, state.PreviousVersionID = state.PreviousVersionID, state.CurrentVersionID
	state.UpdatedAt = now
	if err := tx.Commit(); err != nil {
		return DeploymentChannelState{}, err
	}
	return state, nil
}

func (s *Store) SetDeploymentFrozen(ctx context.Context, subject, agentID string, frozen bool, reason string) (DeploymentStatus, error) {
	subject = strings.TrimSpace(subject)
	agentID = strings.TrimSpace(agentID)
	if !validSubject(subject) || agentID == "" {
		return DeploymentStatus{}, fmt.Errorf("%w: subject and agent_id are required", ErrInvalid)
	}
	now := s.timestamp()
	frozenInt := 0
	action := "unfreeze"
	if frozen {
		frozenInt = 1
		action = "freeze"
	} else {
		reason = ""
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return DeploymentStatus{}, err
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `
		INSERT INTO autopilot_deployment_controls(subject, agent_id, frozen, freeze_reason, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(subject, agent_id) DO UPDATE SET
		 frozen = excluded.frozen, freeze_reason = excluded.freeze_reason, updated_at = excluded.updated_at`,
		subject, agentID, frozenInt, strings.TrimSpace(reason), timeString(now))
	if err != nil {
		return DeploymentStatus{}, err
	}
	if err := insertDeploymentEvent(ctx, tx, subject, agentID, "", action, "", "", strings.TrimSpace(reason), now); err != nil {
		return DeploymentStatus{}, err
	}
	if err := tx.Commit(); err != nil {
		return DeploymentStatus{}, err
	}
	return s.DeploymentStatus(ctx, subject, agentID)
}

// ResolveDefinition selects a canary or stable definition deterministically
// from subject/agent/run. Frozen agents fail before a definition is returned.
func (s *Store) ResolveDefinition(ctx context.Context, subject, agentID, runID string) (DeploymentResolution, error) {
	if !validSubject(subject) || strings.TrimSpace(agentID) == "" || strings.TrimSpace(runID) == "" {
		return DeploymentResolution{}, fmt.Errorf("%w: subject, agent_id, and run_id are required", ErrInvalid)
	}
	var frozen int
	err := s.db.QueryRowContext(ctx, `SELECT frozen FROM autopilot_deployment_controls WHERE subject = ? AND agent_id = ?`,
		subject, agentID).Scan(&frozen)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return DeploymentResolution{}, err
	}
	if frozen != 0 {
		return DeploymentResolution{}, ErrFrozen
	}
	states := make(map[DeploymentChannel]DeploymentChannelState)
	rows, err := s.db.QueryContext(ctx, `
		SELECT channel, current_version_id, previous_version_id, traffic_percent, gates_json, updated_at
		FROM autopilot_deployment_channels
		WHERE subject = ? AND agent_id = ? AND channel IN (?, ?)`,
		subject, agentID, ChannelCanary, ChannelStable)
	if err != nil {
		return DeploymentResolution{}, err
	}
	for rows.Next() {
		state, scanErr := scanDeploymentChannel(rows)
		if scanErr != nil {
			_ = rows.Close()
			return DeploymentResolution{}, scanErr
		}
		states[state.Channel] = state
	}
	if err := rows.Close(); err != nil {
		return DeploymentResolution{}, err
	}
	selected := states[ChannelStable]
	channel := ChannelStable
	if canary, ok := states[ChannelCanary]; ok && deterministicBucket(subject, agentID, runID) < canary.TrafficPercent {
		selected = canary
		channel = ChannelCanary
	}
	if selected.CurrentVersionID == "" {
		return DeploymentResolution{}, ErrNotFound
	}
	version, err := s.GetDeploymentVersion(ctx, subject, selected.CurrentVersionID)
	if err != nil {
		return DeploymentResolution{}, err
	}
	return DeploymentResolution{VersionID: version.ID, RevisionHash: version.RevisionHash,
		Channel: channel, DefinitionJSON: version.DefinitionJSON}, nil
}

func deterministicBucket(subject, agentID, runID string) int {
	sum := sha256.Sum256([]byte(subject + "\x00" + agentID + "\x00" + runID))
	return int(binary.BigEndian.Uint32(sum[:4]) % 100)
}

type sqlQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func currentChannelVersion(ctx context.Context, q sqlQueryer, subject, agentID string, channel DeploymentChannel) (string, error) {
	var id string
	err := q.QueryRowContext(ctx, `
		SELECT current_version_id FROM autopilot_deployment_channels
		WHERE subject = ? AND agent_id = ? AND channel = ?`, subject, agentID, channel).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return id, err
}

func insertDeploymentEvent(ctx context.Context, tx *sql.Tx, subject, agentID string,
	channel DeploymentChannel, action, fromVersion, toVersion, reason string, at time.Time) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO autopilot_deployment_events
		(subject, agent_id, channel, action, from_version_id, to_version_id, reason, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, subject, agentID, channel, action,
		fromVersion, toVersion, reason, timeString(at))
	return err
}
