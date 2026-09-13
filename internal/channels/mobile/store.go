package mobile

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/soulacy/soulacy/internal/sqlitex"
	"github.com/soulacy/soulacy/pkg/message"
)

const schema = `
CREATE TABLE IF NOT EXISTS mobile_deliveries (
  workspace_id TEXT NOT NULL,
  id TEXT NOT NULL,
  destination TEXT NOT NULL,
  agent_id TEXT NOT NULL,
  session_id TEXT NOT NULL,
  title TEXT NOT NULL,
  body TEXT NOT NULL,
  parts_json TEXT NOT NULL DEFAULT '[]',
  metadata_json TEXT NOT NULL DEFAULT '{}',
  created_at TEXT NOT NULL,
  read_at TEXT,
  PRIMARY KEY (workspace_id, id)
);
CREATE INDEX IF NOT EXISTS idx_mobile_deliveries_workspace_created
  ON mobile_deliveries(workspace_id, created_at DESC, id DESC);
CREATE TABLE IF NOT EXISTS mobile_devices (
  workspace_id TEXT NOT NULL,
  id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  name TEXT NOT NULL,
  push_token TEXT NOT NULL DEFAULT '',
  push_environment TEXT NOT NULL DEFAULT 'production',
  bundle_id TEXT NOT NULL DEFAULT 'dev.soulacy.ios',
  notifications_enabled INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (workspace_id, id)
);
CREATE INDEX IF NOT EXISTS idx_mobile_devices_workspace_user
  ON mobile_devices(workspace_id, user_id);
CREATE TABLE IF NOT EXISTS mobile_delivery_receipts (
  workspace_id TEXT NOT NULL,
  delivery_id TEXT NOT NULL,
  device_id TEXT NOT NULL,
  read_at TEXT NOT NULL,
  PRIMARY KEY (workspace_id, delivery_id, device_id)
);
`

type Delivery struct {
	ID          string            `json:"id"`
	Destination string            `json:"destination"`
	AgentID     string            `json:"agent_id"`
	SessionID   string            `json:"session_id"`
	Title       string            `json:"title"`
	Body        string            `json:"body"`
	Parts       []message.Part    `json:"parts,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	CreatedAt   time.Time         `json:"created_at"`
	ReadAt      *time.Time        `json:"read_at,omitempty"`
}

type Device struct {
	ID                   string    `json:"id"`
	UserID               string    `json:"user_id"`
	Name                 string    `json:"name"`
	PushToken            string    `json:"push_token,omitempty"`
	PushEnvironment      string    `json:"push_environment"`
	BundleID             string    `json:"bundle_id"`
	NotificationsEnabled bool      `json:"notifications_enabled"`
	CreatedAt            time.Time `json:"created_at"`
	UpdatedAt            time.Time `json:"updated_at"`
}

type Store struct{ db *sql.DB }

func Open(path string) (*Store, error) {
	db, err := sqlitex.Open(path, sqlitex.DefaultOptions())
	if err != nil {
		return nil, fmt.Errorf("mobile delivery: open: %w", err)
	}
	if _, err := db.Exec(schema + nodeSchema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("mobile delivery: schema: %w", err)
	}
	if err := sqlitex.RecordSchemaVersion(db, "mobile_deliveries", 1); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := sqlitex.RecordSchemaVersion(db, "mobile_nodes", 1); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Add(ctx context.Context, workspaceID string, d Delivery) error {
	workspaceID = normalizeWorkspaceID(workspaceID)
	if strings.TrimSpace(d.ID) == "" || strings.TrimSpace(d.AgentID) == "" {
		return errors.New("mobile delivery requires id and agent_id")
	}
	if d.CreatedAt.IsZero() {
		d.CreatedAt = time.Now().UTC()
	}
	parts, _ := json.Marshal(d.Parts)
	metadata, _ := json.Marshal(d.Metadata)
	_, err := s.db.ExecContext(ctx, `INSERT INTO mobile_deliveries
    (workspace_id,id,destination,agent_id,session_id,title,body,parts_json,metadata_json,created_at)
    VALUES(?,?,?,?,?,?,?,?,?,?)
    ON CONFLICT(workspace_id,id) DO NOTHING`, workspaceID, d.ID, normalizeDestination(d.Destination),
		d.AgentID, d.SessionID, d.Title, d.Body, string(parts), string(metadata), d.CreatedAt.UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Store) List(ctx context.Context, workspaceID, deviceID, userID string, limit int) ([]Delivery, error) {
	workspaceID = normalizeWorkspaceID(workspaceID)
	if err := s.requireDeviceOwner(ctx, workspaceID, deviceID, userID); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT d.id,d.destination,d.agent_id,d.session_id,d.title,d.body,d.parts_json,d.metadata_json,d.created_at,
      (SELECT read_at FROM mobile_delivery_receipts r WHERE r.workspace_id=d.workspace_id AND r.delivery_id=d.id AND r.device_id=?)
    FROM mobile_deliveries d WHERE d.workspace_id=? AND
      (destination='all' OR destination=? OR destination=? OR
       (destination NOT LIKE 'device:%' AND destination NOT LIKE 'user:%'))
	ORDER BY created_at DESC,d.id DESC LIMIT ?`, strings.TrimSpace(deviceID), workspaceID, "device:"+strings.TrimSpace(deviceID),
		"user:"+strings.TrimSpace(userID), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Delivery
	for rows.Next() {
		d, err := scanDelivery(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) Get(ctx context.Context, workspaceID, deliveryID, deviceID, userID string) (Delivery, error) {
	if err := s.requireDeviceOwner(ctx, normalizeWorkspaceID(workspaceID), deviceID, userID); err != nil {
		return Delivery{}, err
	}
	row := s.db.QueryRowContext(ctx, `SELECT d.id,d.destination,d.agent_id,d.session_id,d.title,d.body,d.parts_json,d.metadata_json,d.created_at,
      (SELECT read_at FROM mobile_delivery_receipts r WHERE r.workspace_id=d.workspace_id AND r.delivery_id=d.id AND r.device_id=?)
    FROM mobile_deliveries d WHERE d.workspace_id=? AND d.id=? AND
      (destination='all' OR destination=? OR destination=? OR
       (destination NOT LIKE 'device:%' AND destination NOT LIKE 'user:%'))`, strings.TrimSpace(deviceID), normalizeWorkspaceID(workspaceID), deliveryID,
		"device:"+strings.TrimSpace(deviceID), "user:"+strings.TrimSpace(userID))
	return scanDelivery(row)
}

func (s *Store) MarkRead(ctx context.Context, workspaceID, deliveryID, deviceID, userID string) error {
	if strings.TrimSpace(deviceID) == "" {
		return errors.New("device_id is required")
	}
	if err := s.requireDeviceOwner(ctx, normalizeWorkspaceID(workspaceID), deviceID, userID); err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO mobile_delivery_receipts(workspace_id,delivery_id,device_id,read_at)
    SELECT workspace_id,id,?,? FROM mobile_deliveries WHERE workspace_id=? AND id=? AND
      (destination='all' OR destination=? OR destination=? OR
       (destination NOT LIKE 'device:%' AND destination NOT LIKE 'user:%'))
    ON CONFLICT(workspace_id,delivery_id,device_id) DO UPDATE SET read_at=excluded.read_at`, strings.TrimSpace(deviceID),
		time.Now().UTC().Format(time.RFC3339Nano), normalizeWorkspaceID(workspaceID), deliveryID,
		"device:"+strings.TrimSpace(deviceID), "user:"+strings.TrimSpace(userID))
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

type scanner interface{ Scan(...any) error }

func scanDelivery(row scanner) (Delivery, error) {
	var d Delivery
	var parts, metadata, created string
	var read sql.NullString
	if err := row.Scan(&d.ID, &d.Destination, &d.AgentID, &d.SessionID, &d.Title, &d.Body,
		&parts, &metadata, &created, &read); err != nil {
		return Delivery{}, err
	}
	_ = json.Unmarshal([]byte(parts), &d.Parts)
	_ = json.Unmarshal([]byte(metadata), &d.Metadata)
	d.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	if read.Valid {
		v, err := time.Parse(time.RFC3339Nano, read.String)
		if err == nil {
			d.ReadAt = &v
		}
	}
	return d, nil
}

func (s *Store) UpsertDevice(ctx context.Context, workspaceID, userID string, d Device) error {
	now := time.Now().UTC()
	if strings.TrimSpace(d.ID) == "" || strings.TrimSpace(userID) == "" {
		return errors.New("mobile device requires id and authenticated user")
	}
	if d.Name == "" {
		d.Name = "iPhone"
	}
	if d.PushEnvironment != "development" {
		d.PushEnvironment = "production"
	}
	if d.BundleID == "" {
		d.BundleID = "dev.soulacy.ios"
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO mobile_devices
    (workspace_id,id,user_id,name,push_token,push_environment,bundle_id,notifications_enabled,created_at,updated_at)
    VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(workspace_id,id) DO UPDATE SET
      user_id=excluded.user_id,name=excluded.name,push_token=excluded.push_token,
      push_environment=excluded.push_environment,bundle_id=excluded.bundle_id,
      notifications_enabled=excluded.notifications_enabled,updated_at=excluded.updated_at
    WHERE mobile_devices.user_id=excluded.user_id`,
		normalizeWorkspaceID(workspaceID), d.ID, userID, d.Name, d.PushToken, d.PushEnvironment, d.BundleID,
		d.NotificationsEnabled, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ErrDeviceOwnership
	}
	return nil
}

func (s *Store) DeleteDevice(ctx context.Context, workspaceID, userID, deviceID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }() // No-op after commit; preserve the operation's error.
	for _, table := range []string{"mobile_devices", "mobile_nodes"} {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE workspace_id=? AND user_id=? AND id=?`, normalizeWorkspaceID(workspaceID), userID, deviceID); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE mobile_node_commands SET status='expired',error='device registration removed' WHERE workspace_id=? AND user_id=? AND device_id=? AND status='queued'`, normalizeWorkspaceID(workspaceID), userID, deviceID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) requireDeviceOwner(ctx context.Context, workspaceID, deviceID, userID string) error {
	var one int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM mobile_devices WHERE workspace_id=? AND id=? AND user_id=?`,
		workspaceID, strings.TrimSpace(deviceID), strings.TrimSpace(userID)).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrDeviceOwnership
	}
	return err
}

func (s *Store) TargetDevices(ctx context.Context, workspaceID, destination string) ([]Device, error) {
	workspaceID, destination = normalizeWorkspaceID(workspaceID), normalizeDestination(destination)
	query := `SELECT id,user_id,name,push_token,push_environment,bundle_id,notifications_enabled,created_at,updated_at
    FROM mobile_devices WHERE workspace_id=? AND notifications_enabled=1 AND push_token<>''`
	args := []any{workspaceID}
	if strings.HasPrefix(destination, "device:") {
		query += " AND id=?"
		args = append(args, strings.TrimPrefix(destination, "device:"))
	} else if strings.HasPrefix(destination, "user:") {
		query += " AND user_id=?"
		args = append(args, strings.TrimPrefix(destination, "user:"))
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Device
	for rows.Next() {
		var d Device
		var created, updated string
		if err := rows.Scan(&d.ID, &d.UserID, &d.Name, &d.PushToken, &d.PushEnvironment, &d.BundleID,
			&d.NotificationsEnabled, &created, &updated); err != nil {
			return nil, err
		}
		d.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		d.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
		out = append(out, d)
	}
	return out, rows.Err()
}

func normalizeDestination(v string) string {
	v = strings.TrimSpace(v)
	if strings.HasPrefix(v, "device:") || strings.HasPrefix(v, "user:") {
		return v
	}
	// The mobile adapter historically treated any unqualified destination as a
	// broadcast when selecting APNs devices. Store it with the same semantics so
	// a phone that receives the push can also fetch and acknowledge the result.
	return "all"
}

func normalizeWorkspaceID(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return "personal"
	}
	return v
}

var defaultStore struct {
	sync.RWMutex
	store *Store
}

var ErrDeviceOwnership = errors.New("mobile device belongs to another user")

func SetDefaultStore(store *Store) {
	defaultStore.Lock()
	defaultStore.store = store
	defaultStore.Unlock()
}

func DefaultStore() *Store {
	defaultStore.RLock()
	defer defaultStore.RUnlock()
	return defaultStore.store
}
