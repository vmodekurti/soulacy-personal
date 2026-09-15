package person

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"
)

// Record stores raw signals. Duplicates are cheap and harmless: a phone that
// retries after a dropped connection must not lose the signal, and observers
// take medians, so one extra sample changes nothing.
func (s *SQLiteStore) Record(ctx context.Context, observations []Observation) (int, error) {
	now := s.clock()
	prepared := make([]Observation, 0, len(observations))
	for _, observation := range observations {
		observation = observation.Normalize(now)
		if err := observation.Validate(now); err != nil {
			return 0, err
		}
		prepared = append(prepared, observation)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	for _, observation := range prepared {
		payload, err := marshalValue(observation.Payload)
		if err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO person_observations(owner, kind, at, payload) VALUES(?,?,?,?)`,
			observation.Owner, observation.Kind, observation.At.UnixMilli(), payload); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(prepared), nil
}

func (s *SQLiteStore) Observations(ctx context.Context, owner string, kinds []string, since time.Time) ([]Observation, error) {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return nil, ErrInvalidOwner
	}
	query := `SELECT owner, kind, at, payload FROM person_observations WHERE owner = ? AND at >= ?`
	args := []any{owner, since.UTC().UnixMilli()}
	if len(kinds) > 0 {
		placeholders := make([]string, 0, len(kinds))
		for _, kind := range kinds {
			placeholders = append(placeholders, "?")
			args = append(args, strings.ToLower(strings.TrimSpace(kind)))
		}
		query += " AND kind IN (" + strings.Join(placeholders, ",") + ")"
	}
	query += " ORDER BY at"
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var observations []Observation
	for rows.Next() {
		var (
			observation Observation
			at          int64
			payload     sql.NullString
		)
		if err := rows.Scan(&observation.Owner, &observation.Kind, &at, &payload); err != nil {
			return nil, err
		}
		observation.At = time.UnixMilli(at).UTC()
		if payload.Valid && payload.String != "" {
			_ = json.Unmarshal([]byte(payload.String), &observation.Payload)
		}
		observations = append(observations, observation)
	}
	return observations, rows.Err()
}

func (s *SQLiteStore) PruneObservations(ctx context.Context, before time.Time) (int, error) {
	result, err := s.db.ExecContext(ctx, `DELETE FROM person_observations WHERE at < ?`, before.UTC().UnixMilli())
	if err != nil {
		return 0, err
	}
	removed, _ := result.RowsAffected()
	return int(removed), nil
}

// Senses reports the owner's consent per sense. A sense with no stored
// preference is absent from the map, and absent means off: Soulacy watches
// nothing until asked.
func (s *SQLiteStore) Senses(ctx context.Context, owner string) (map[string]bool, error) {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return nil, ErrInvalidOwner
	}
	rows, err := s.db.QueryContext(ctx, `SELECT sense, enabled FROM person_senses WHERE owner = ?`, owner)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	senses := map[string]bool{}
	for rows.Next() {
		var (
			sense   string
			enabled int
		)
		if err := rows.Scan(&sense, &enabled); err != nil {
			return nil, err
		}
		senses[sense] = enabled == 1
	}
	return senses, rows.Err()
}

func (s *SQLiteStore) SetSense(ctx context.Context, owner, sense string, enabled bool) error {
	owner, sense = strings.TrimSpace(owner), strings.ToLower(strings.TrimSpace(sense))
	if owner == "" {
		return ErrInvalidOwner
	}
	flag := 0
	if enabled {
		flag = 1
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO person_senses(owner, sense, enabled, updated_at) VALUES(?,?,?,?)
ON CONFLICT(owner, sense) DO UPDATE SET enabled = excluded.enabled, updated_at = excluded.updated_at`,
		owner, sense, flag, s.clock().UnixMilli())
	return err
}

// Turning a sense off removes what it concluded, not only what it will
// conclude next: a person who withdraws consent expects the inference gone,
// not merely frozen.
func (s *SQLiteStore) ForgetSense(ctx context.Context, owner, sense string) (int, error) {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return 0, ErrInvalidOwner
	}
	source := SourceSensePrefix + strings.ToLower(strings.TrimSpace(sense))
	rows, err := s.db.QueryContext(ctx, `SELECT section, key FROM person_entries WHERE owner = ? AND source = ?`, owner, source)
	if err != nil {
		return 0, err
	}
	type target struct {
		section Section
		key     string
	}
	var targets []target
	for rows.Next() {
		var t target
		var section string
		if err := rows.Scan(&section, &t.key); err != nil {
			_ = rows.Close()
			return 0, err
		}
		t.section = Section(section)
		targets = append(targets, t)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, err
	}
	_ = rows.Close()
	for _, t := range targets {
		if err := s.Delete(ctx, owner, t.section, t.key); err != nil {
			return 0, err
		}
	}
	// The raw signals go too.
	if kinds := kindsForSense(sense); len(kinds) > 0 {
		placeholders := make([]string, 0, len(kinds))
		args := []any{owner}
		for _, kind := range kinds {
			placeholders = append(placeholders, "?")
			args = append(args, kind)
		}
		if _, err := s.db.ExecContext(ctx,
			`DELETE FROM person_observations WHERE owner = ? AND kind IN (`+strings.Join(placeholders, ",")+`)`, args...); err != nil {
			return 0, err
		}
	}
	return len(targets), nil
}

func kindsForSense(sense string) []string {
	for _, observer := range Observers() {
		if observer.Sense() == strings.ToLower(strings.TrimSpace(sense)) {
			return observer.Kinds()
		}
	}
	return nil
}

var _ ObservationStore = (*SQLiteStore)(nil)
