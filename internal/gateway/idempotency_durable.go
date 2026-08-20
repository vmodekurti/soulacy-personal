package gateway

import (
	"context"
	"database/sql"
	"errors"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/soulacy/soulacy/internal/apiversion"
	"github.com/soulacy/soulacy/internal/sqlitex"
	"github.com/soulacy/soulacy/internal/workspacepurge"
	"github.com/soulacy/soulacy/internal/wsroot"
)

// idempotency_durable.go — the replay cache has to outlive one process.
//
// THE BUG. `Idempotency-Key` promises that retrying a mutation does not
// perform it twice. The store keeping that promise was a map in one gateway's
// memory, which makes the promise true for exactly as long as one process
// lives and one process serves the client.
//
// Two ways it broke, both silent. A restart between the original request and
// the retry emptied the map, so the retry executed the mutation a second time
// — and the retry is most likely precisely when the process just restarted,
// because that is what made the client retry. And in scale mode a retry that
// lands on another replica finds an empty map for the same reason, which is
// the sixth entry in config.ScaleReplicationBlockers.
//
// Nothing errors in either case. The mutation simply happens twice: two runs
// submitted, two agents created, two of whatever the route does.
//
// WHY SQLITE RATHER THAN THE MAP PLUS A TTL. Because the property needed is
// "exactly one caller reserves this key", and that is a uniqueness constraint.
// INSERT ... ON CONFLICT DO NOTHING gives it atomically, across processes on
// one host and — when the file is on shared storage or the backend is Postgres
// — across hosts. A map plus coordination would be reimplementing a unique
// index badly.

// IdempotencyCache is the durable backing store. Exported only so the wiring
// can open it and register its Close; every use goes through the middleware.
type IdempotencyCache struct {
	db  *sql.DB
	now func() time.Time
}

const idempotencySchema = `
CREATE TABLE IF NOT EXISTS idempotency_records(
	key          TEXT PRIMARY KEY,
	workspace_id TEXT NOT NULL DEFAULT '',
	request_hash TEXT NOT NULL,
	request_id   TEXT NOT NULL DEFAULT '',
	status       INTEGER NOT NULL DEFAULT 0,
	content_type TEXT NOT NULL DEFAULT '',
	body         BLOB,
	in_flight    INTEGER NOT NULL DEFAULT 1,
	stored_at    TIMESTAMP NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_idempotency_workspace ON idempotency_records(workspace_id);`

// OpenIdempotencyCache creates or opens the durable replay cache.
//
// The KEY is already a hash of (workspace, method, route, client key), so it
// is the primary key and one tenant's key cannot collide with another's by
// construction rather than by a filter. workspace_id is stored ALONGSIDE it —
// redundantly, on purpose — because a deleted workspace's records have to be
// removable, and a hash cannot be searched by tenant.
func OpenIdempotencyCache(path string) (*IdempotencyCache, error) {
	db, err := sqlitex.Open(path, sqlitex.DefaultOptions())
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(idempotencySchema); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &IdempotencyCache{db: db, now: time.Now}, nil
}

func (d *IdempotencyCache) Close() error {
	if d == nil || d.db == nil {
		return nil
	}
	return d.db.Close()
}

// begin reserves a key, or reports the completed response to replay.
//
// One transaction, and the reservation is an INSERT that can fail on the
// primary key. Checking for an existing row and then inserting would be two
// statements with a window between them, and the window is exactly the
// concurrent-duplicate case this exists to refuse.
func (d *IdempotencyCache) begin(key, workspaceID, requestHash, requestID string) (*idempotencyRecord, bool, error) {
	if d == nil || d.db == nil {
		return nil, false, errors.New("idempotency: store is unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), vaultReadTimeout)
	defer cancel()

	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = tx.Rollback() }()

	now := d.now().UTC()
	// An expired record is deleted first, in the same transaction, so the
	// insert below can take its place. Doing it as a separate call would let
	// two retries both see the expiry and both believe they reserved the key.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM idempotency_records WHERE key = ? AND stored_at <= ?`,
		key, now.Add(-idempotencyTTL)); err != nil {
		return nil, false, err
	}

	res, err := tx.ExecContext(ctx, `INSERT INTO idempotency_records(
		key, workspace_id, request_hash, request_id, in_flight, stored_at)
		VALUES(?,?,?,?,1,?) ON CONFLICT(key) DO NOTHING`,
		key, wsroot.Normalize(workspaceID), requestHash, requestID, now)
	if err != nil {
		return nil, false, err
	}
	if affected, _ := res.RowsAffected(); affected == 1 {
		return nil, false, tx.Commit()
	}

	var record idempotencyRecord
	var inFlight int
	var body []byte
	err = tx.QueryRowContext(ctx, `SELECT request_hash, request_id, status, content_type, body,
		in_flight FROM idempotency_records WHERE key = ?`, key).Scan(
		&record.requestHash, &record.requestID, &record.status, &record.contentType, &body, &inFlight)
	if errors.Is(err, sql.ErrNoRows) {
		// The row vanished between the insert failing and this read, which
		// means another caller completed and something evicted it. Treat it as
		// unreserved rather than as an error: re-executing is what would have
		// happened had the retry arrived a moment later.
		return nil, false, tx.Commit()
	}
	if err != nil {
		return nil, false, err
	}
	if record.requestHash != requestHash {
		return nil, false, &apiversion.IncompatibleError{
			Code:    apiversion.CodeIdempotencyReus,
			Message: "this idempotency key was already used with a different request body",
			Remedy:  "use a fresh idempotency key for a different request",
		}
	}
	if inFlight == 1 {
		return nil, false, &apiversion.IncompatibleError{
			Code:    apiversion.CodeIdempotencyBusy,
			Message: "an identical request with this idempotency key is still in progress",
			Remedy:  "wait for the original request to complete, then retry",
		}
	}
	record.body = body
	return &record, true, tx.Commit()
}

// complete stores a successful response for replay, or drops the reservation.
//
// Only 2xx is retained, the same rule the in-memory store follows: caching a
// 500 would turn a transient failure into a permanent one for the life of the
// key.
func (d *IdempotencyCache) complete(key string, status int, contentType string, body []byte) {
	if d == nil || d.db == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), vaultReadTimeout)
	defer cancel()
	if status < 200 || status >= 300 {
		_, _ = d.db.ExecContext(ctx, `DELETE FROM idempotency_records WHERE key = ?`, key)
		return
	}
	_, _ = d.db.ExecContext(ctx, `UPDATE idempotency_records
		SET status = ?, content_type = ?, body = ?, in_flight = 0, stored_at = ?
		WHERE key = ?`, status, contentType, body, d.now().UTC(), key)
}

// abandon releases a reservation whose handler never produced a response.
func (d *IdempotencyCache) abandon(key string) {
	if d == nil || d.db == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), vaultReadTimeout)
	defer cancel()
	_, _ = d.db.ExecContext(ctx,
		`DELETE FROM idempotency_records WHERE key = ? AND in_flight = 1`, key)
}

// PurgeWorkspace removes a deleted workspace's replay records.
func (d *IdempotencyCache) PurgeWorkspace(ctx context.Context, workspaceID string) (workspacepurge.Removed, error) {
	if d == nil || d.db == nil {
		return workspacepurge.Removed{}, nil
	}
	res, err := d.db.ExecContext(ctx,
		`DELETE FROM idempotency_records WHERE workspace_id = ?`, wsroot.Normalize(workspaceID))
	if err != nil {
		return workspacepurge.Removed{}, err
	}
	rows, _ := res.RowsAffected()
	return workspacepurge.Removed{Rows: rows}, nil
}
