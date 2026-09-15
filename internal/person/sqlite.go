package person

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/soulacy/soulacy/internal/sqlitex"
)

// SQLiteStore is the durable person model. One row per (owner, section, key)
// plus an append-only change feed, in the same shape adaptive memory uses so
// the phone's sync code is the same on both.
type SQLiteStore struct {
	db  *sql.DB
	now func() time.Time
}

// OpenSQLite opens (or creates) the model database at path. 0600 because it
// holds the most personal data in the system.
func OpenSQLite(path string) (*SQLiteStore, error) {
	if !filepath.IsAbs(path) || strings.ContainsRune(path, 0) {
		return nil, fmt.Errorf("person: path must be absolute")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	_ = f.Chmod(0600)
	if err := f.Close(); err != nil {
		return nil, err
	}
	opts := sqlitex.DefaultOptions()
	opts.MaxOpenConns, opts.MaxIdleConns = 1, 1
	dsn := sqlitex.DSN((&url.URL{Scheme: "file", Path: path}).String(), opts) + "&_txlock=immediate"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if _, err := sqlitex.MigrateSchema(db, "person_model", []sqlitex.SchemaMigration{
		{Version: 1, SQL: `
CREATE TABLE person_entries(
  owner TEXT NOT NULL,
  section TEXT NOT NULL,
  key TEXT NOT NULL,
  summary TEXT NOT NULL,
  value TEXT,
  source TEXT NOT NULL,
  confidence REAL NOT NULL,
  observed_at INTEGER NOT NULL,
  expires_at INTEGER,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY (owner, section, key)
);
CREATE INDEX person_entries_owner ON person_entries(owner, section);
CREATE TABLE person_changes(
  cursor INTEGER PRIMARY KEY AUTOINCREMENT,
  owner TEXT NOT NULL,
  section TEXT NOT NULL,
  key TEXT NOT NULL,
  deleted INTEGER NOT NULL,
  at INTEGER NOT NULL
);
CREATE INDEX person_changes_owner ON person_changes(owner, cursor);
`},
	}); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("person: migrate: %w", err)
	}
	return &SQLiteStore{db: db, now: time.Now}, nil
}

func (s *SQLiteStore) Close() error { return s.db.Close() }

// clock is overridable in tests.
func (s *SQLiteStore) clock() time.Time {
	if s.now == nil {
		return time.Now().UTC()
	}
	return s.now().UTC()
}

func (s *SQLiteStore) Put(ctx context.Context, entry Entry) (PutResult, error) {
	results, err := s.PutAll(ctx, []Entry{entry})
	if err != nil {
		return PutResult{}, err
	}
	return results[0], nil
}

func (s *SQLiteStore) PutAll(ctx context.Context, entries []Entry) ([]PutResult, error) {
	now := s.clock()
	prepared := make([]Entry, 0, len(entries))
	for _, entry := range entries {
		entry = entry.Normalize(now)
		if err := entry.Validate(); err != nil {
			return nil, err
		}
		prepared = append(prepared, entry)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	results := make([]PutResult, 0, len(prepared))
	for _, entry := range prepared {
		existing, found, err := readEntry(ctx, tx, entry.Owner, entry.Section, entry.Key)
		if err != nil {
			return nil, err
		}
		if found && !Outranks(entry, existing) {
			results = append(results, PutResult{
				Entry:   existing,
				Applied: false,
				Reason:  refusalReason(entry, existing),
			})
			continue
		}
		if found {
			entry.CreatedAt = existing.CreatedAt
		} else {
			entry.CreatedAt = now
			var count int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM person_entries WHERE owner = ?`, entry.Owner).Scan(&count); err != nil {
				return nil, err
			}
			if count >= MaxEntriesPerOwn {
				return nil, fmt.Errorf("%w: the model is full (%d entries)", ErrInvalidEntry, MaxEntriesPerOwn)
			}
		}
		entry.UpdatedAt = now
		value, err := marshalValue(entry.Value)
		if err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO person_entries(owner, section, key, summary, value, source, confidence, observed_at, expires_at, created_at, updated_at)
VALUES(?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(owner, section, key) DO UPDATE SET
  summary = excluded.summary, value = excluded.value, source = excluded.source,
  confidence = excluded.confidence, observed_at = excluded.observed_at,
  expires_at = excluded.expires_at, updated_at = excluded.updated_at`,
			entry.Owner, string(entry.Section), entry.Key, entry.Summary, value, entry.Source,
			entry.Confidence, entry.ObservedAt.UnixMilli(), expiryMillis(entry.ExpiresAt),
			entry.CreatedAt.UnixMilli(), entry.UpdatedAt.UnixMilli()); err != nil {
			return nil, err
		}
		if err := recordChange(ctx, tx, entry.Owner, entry.Section, entry.Key, false, now); err != nil {
			return nil, err
		}
		results = append(results, PutResult{Entry: entry, Applied: true})
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return results, nil
}

func refusalReason(incoming, existing Entry) string {
	if incoming.Origin() < existing.Origin() {
		switch existing.Origin() {
		case OriginManual:
			return "you set this yourself, so " + incoming.Source + " did not change it"
		default:
			return "held by " + existing.Source + ", which outranks " + incoming.Source
		}
	}
	return "a newer observation from " + existing.Source + " is already stored"
}

func (s *SQLiteStore) Get(ctx context.Context, owner string, section Section, key string) (Entry, error) {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return Entry{}, ErrInvalidOwner
	}
	entry, found, err := readEntry(ctx, s.db, owner, section, strings.ToLower(strings.TrimSpace(key)))
	if err != nil {
		return Entry{}, err
	}
	if !found {
		return Entry{}, ErrNotFound
	}
	return entry, nil
}

func (s *SQLiteStore) List(ctx context.Context, owner string, query Query) ([]Entry, error) {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return nil, ErrInvalidOwner
	}
	sql := `SELECT owner, section, key, summary, value, source, confidence, observed_at, expires_at, created_at, updated_at
	        FROM person_entries WHERE owner = ?`
	args := []any{owner}
	if len(query.Sections) > 0 {
		placeholders := make([]string, 0, len(query.Sections))
		for _, section := range query.Sections {
			placeholders = append(placeholders, "?")
			args = append(args, string(section))
		}
		sql += " AND section IN (" + strings.Join(placeholders, ",") + ")"
	}
	if len(query.Keys) > 0 {
		placeholders := make([]string, 0, len(query.Keys))
		for _, key := range query.Keys {
			placeholders = append(placeholders, "?")
			args = append(args, strings.ToLower(strings.TrimSpace(key)))
		}
		sql += " AND key IN (" + strings.Join(placeholders, ",") + ")"
	}
	sql += " ORDER BY section, key"
	if query.Limit > 0 {
		sql += fmt.Sprintf(" LIMIT %d", query.Limit)
	}
	rows, err := s.db.QueryContext(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	now := s.clock()
	var entries []Entry
	for rows.Next() {
		entry, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		if !query.IncludeExpired && entry.Expired(now) {
			continue
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func (s *SQLiteStore) Delete(ctx context.Context, owner string, section Section, key string) error {
	owner, key = strings.TrimSpace(owner), strings.ToLower(strings.TrimSpace(key))
	if owner == "" {
		return ErrInvalidOwner
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `DELETE FROM person_entries WHERE owner = ? AND section = ? AND key = ?`, owner, string(section), key)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ErrNotFound
	}
	if err := recordChange(ctx, tx, owner, section, key, true, s.clock()); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLiteStore) Purge(ctx context.Context, owner string, sections ...Section) (int, error) {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return 0, ErrInvalidOwner
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	args := []any{owner}
	where := "owner = ?"
	if len(sections) > 0 {
		placeholders := make([]string, 0, len(sections))
		for _, section := range sections {
			placeholders = append(placeholders, "?")
			args = append(args, string(section))
		}
		where += " AND section IN (" + strings.Join(placeholders, ",") + ")"
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM person_entries WHERE `+where, args...)
	if err != nil {
		return 0, err
	}
	removed, _ := result.RowsAffected()
	// The feed is dropped with the entries: a client that comes back with an
	// old cursor is told to reset rather than replaying tombstones forever.
	if _, err := tx.ExecContext(ctx, `DELETE FROM person_changes WHERE `+where, args...); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return int(removed), nil
}

func (s *SQLiteStore) Changes(ctx context.Context, owner string, since int64, limit int) (Changes, error) {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return Changes{}, ErrInvalidOwner
	}
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	var highest sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT MAX(cursor) FROM person_changes WHERE owner = ?`, owner).Scan(&highest); err != nil {
		return Changes{}, err
	}
	page := Changes{NextCursor: since}
	if since > 0 && (!highest.Valid || since > highest.Int64) {
		// The client knows about changes we no longer have: purged.
		page.Reset = true
		page.NextCursor = highest.Int64
		return page, nil
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT c.cursor, c.section, c.key, c.deleted,
       e.owner, e.section, e.key, e.summary, e.value, e.source, e.confidence,
       e.observed_at, e.expires_at, e.created_at, e.updated_at
FROM person_changes c
LEFT JOIN person_entries e ON e.owner = c.owner AND e.section = c.section AND e.key = c.key
WHERE c.owner = ? AND c.cursor > ?
ORDER BY c.cursor LIMIT ?`, owner, since, limit+1)
	if err != nil {
		return Changes{}, err
	}
	defer func() { _ = rows.Close() }()
	now := s.clock()
	for rows.Next() {
		var (
			change  Change
			deleted int
			entry   Entry
			present nullableEntry
		)
		if err := rows.Scan(&change.Cursor, &change.Section, &change.Key, &deleted,
			&present.owner, &present.section, &present.key, &present.summary, &present.value,
			&present.source, &present.confidence, &present.observedAt, &present.expiresAt,
			&present.createdAt, &present.updatedAt); err != nil {
			return Changes{}, err
		}
		change.Deleted = deleted == 1
		if !change.Deleted && present.owner.Valid {
			entry = present.entry()
			// An expired entry reaches a client as a tombstone: it must stop
			// showing "commuting" just because nothing has overwritten it.
			if entry.Expired(now) {
				change.Deleted = true
			} else {
				change.Entry = &entry
			}
		} else {
			change.Deleted = true
		}
		page.Changes = append(page.Changes, change)
	}
	if err := rows.Err(); err != nil {
		return Changes{}, err
	}
	if len(page.Changes) > limit {
		page.Changes = page.Changes[:limit]
		page.HasMore = true
	}
	if n := len(page.Changes); n > 0 {
		page.NextCursor = page.Changes[n-1].Cursor
	}
	return page, nil
}

// --- row helpers -----------------------------------------------------------

type rowScanner interface {
	Scan(dest ...any) error
}

type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

type nullableEntry struct {
	owner, section, key, summary, source sql.NullString
	value                                sql.NullString
	confidence                           sql.NullFloat64
	observedAt, expiresAt                sql.NullInt64
	createdAt, updatedAt                 sql.NullInt64
}

func (n nullableEntry) entry() Entry {
	entry := Entry{
		Owner:      n.owner.String,
		Section:    Section(n.section.String),
		Key:        n.key.String,
		Summary:    n.summary.String,
		Source:     n.source.String,
		Confidence: float32(n.confidence.Float64),
		ObservedAt: time.UnixMilli(n.observedAt.Int64).UTC(),
		CreatedAt:  time.UnixMilli(n.createdAt.Int64).UTC(),
		UpdatedAt:  time.UnixMilli(n.updatedAt.Int64).UTC(),
	}
	if n.value.Valid && n.value.String != "" {
		_ = json.Unmarshal([]byte(n.value.String), &entry.Value)
	}
	if n.expiresAt.Valid {
		expires := time.UnixMilli(n.expiresAt.Int64).UTC()
		entry.ExpiresAt = &expires
	}
	return entry
}

func readEntry(ctx context.Context, q querier, owner string, section Section, key string) (Entry, bool, error) {
	row := q.QueryRowContext(ctx, `
SELECT owner, section, key, summary, value, source, confidence, observed_at, expires_at, created_at, updated_at
FROM person_entries WHERE owner = ? AND section = ? AND key = ?`, owner, string(section), key)
	entry, err := scanEntry(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Entry{}, false, nil
	}
	if err != nil {
		return Entry{}, false, err
	}
	return entry, true, nil
}

func scanEntry(row rowScanner) (Entry, error) {
	var (
		entry      Entry
		section    string
		value      sql.NullString
		observedAt int64
		expiresAt  sql.NullInt64
		createdAt  int64
		updatedAt  int64
	)
	if err := row.Scan(&entry.Owner, &section, &entry.Key, &entry.Summary, &value, &entry.Source,
		&entry.Confidence, &observedAt, &expiresAt, &createdAt, &updatedAt); err != nil {
		return Entry{}, err
	}
	entry.Section = Section(section)
	if value.Valid && value.String != "" {
		_ = json.Unmarshal([]byte(value.String), &entry.Value)
	}
	entry.ObservedAt = time.UnixMilli(observedAt).UTC()
	if expiresAt.Valid {
		expires := time.UnixMilli(expiresAt.Int64).UTC()
		entry.ExpiresAt = &expires
	}
	entry.CreatedAt = time.UnixMilli(createdAt).UTC()
	entry.UpdatedAt = time.UnixMilli(updatedAt).UTC()
	return entry, nil
}

func recordChange(ctx context.Context, tx *sql.Tx, owner string, section Section, key string, deleted bool, now time.Time) error {
	flag := 0
	if deleted {
		flag = 1
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO person_changes(owner, section, key, deleted, at) VALUES(?,?,?,?,?)`,
		owner, string(section), key, flag, now.UnixMilli())
	return err
}

func marshalValue(value map[string]any) (any, error) {
	if len(value) == 0 {
		return nil, nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("%w: value is not JSON: %v", ErrInvalidEntry, err)
	}
	return string(encoded), nil
}

func expiryMillis(at *time.Time) any {
	if at == nil {
		return nil
	}
	return at.UnixMilli()
}
