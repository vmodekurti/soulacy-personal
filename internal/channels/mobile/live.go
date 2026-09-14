package mobile

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Live Activities put a running agent on the lock screen and in the Dynamic
// Island: what it is doing, how many steps it has taken, and whether it is
// waiting on the user. The gateway never renders anything; it pushes a small
// content state and the phone draws it.
//
// Two kinds of APNs token are involved. A *push-to-start* token belongs to the
// phone and lets the gateway begin an activity while the app is closed. Once
// an activity exists, the phone reports an *update* token for that activity,
// which the gateway uses for every later update and for the end.

// Live Activity stages, mirrored by the iOS content state.
const (
	LiveStageRunning = "running"
	LiveStageWaiting = "waiting"
	LiveStageDone    = "done"
	LiveStageFailed  = "failed"
)

// LiveAttributes is the static half of an activity, sent once with the start.
type LiveAttributes struct {
	RunKey    string `json:"run_key"`
	AgentID   string `json:"agent_id"`
	AgentName string `json:"agent_name"`
	SessionID string `json:"session_id"`
	StartedAt int64  `json:"started_at"`
}

// LiveState is the dynamic half, sent with every start, update and end.
type LiveState struct {
	Stage     string `json:"stage"`
	Detail    string `json:"detail"`
	Steps     int    `json:"steps"`
	NeedsYou  bool   `json:"needs_you"`
	CallID    string `json:"call_id,omitempty"`
	UpdatedAt int64  `json:"updated_at"`
}

// LiveActivity is one phone's registration of the update token for a run.
type LiveActivity struct {
	RunKey      string
	DeviceID    string
	UserID      string
	Token       string
	Environment string
	BundleID    string
	UpdatedAt   time.Time
}

const liveSchema = `
CREATE TABLE IF NOT EXISTS mobile_live_activities (
  workspace_id TEXT NOT NULL,
  run_key TEXT NOT NULL,
  device_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  token TEXT NOT NULL,
  environment TEXT NOT NULL DEFAULT 'production',
  bundle_id TEXT NOT NULL DEFAULT 'dev.soulacy.ios',
  updated_at TEXT NOT NULL,
  PRIMARY KEY (workspace_id, run_key, device_id)
);
`

// ensureLiveColumns adds the push-to-start token column to phones registered
// before Live Activities existed. SQLite has no ADD COLUMN IF NOT EXISTS.
func (s *Store) ensureLiveColumns() error {
	rows, err := s.db.Query(`PRAGMA table_info(mobile_devices)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return err
		}
		if name == "live_start_token" {
			return rows.Err()
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = s.db.Exec(`ALTER TABLE mobile_devices ADD COLUMN live_start_token TEXT NOT NULL DEFAULT ''`)
	return err
}

// SetLiveStartToken records the phone's push-to-start token. It is called
// from device registration, so ownership was already checked there.
func (s *Store) SetLiveStartToken(ctx context.Context, workspaceID, userID, deviceID, token string) error {
	token = strings.ToLower(strings.TrimSpace(token))
	res, err := s.db.ExecContext(ctx, `UPDATE mobile_devices SET live_start_token=?, updated_at=? WHERE workspace_id=? AND id=? AND user_id=?`,
		token, time.Now().UTC().Format(time.RFC3339Nano), normalizeWorkspaceID(workspaceID), deviceID, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrDeviceOwnership
	}
	return nil
}

// LiveStartDevices lists phones that can have an activity started remotely.
func (s *Store) LiveStartDevices(ctx context.Context, workspaceID, destination string) ([]Device, error) {
	workspaceID, destination = normalizeWorkspaceID(workspaceID), normalizeDestination(destination)
	query := `SELECT id,user_id,name,push_environment,bundle_id,live_start_token FROM mobile_devices
    WHERE workspace_id=? AND notifications_enabled=1 AND live_start_token<>''`
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
		if err := rows.Scan(&d.ID, &d.UserID, &d.Name, &d.PushEnvironment, &d.BundleID, &d.LiveStartToken); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// UpsertLiveActivity stores the update token a phone reported for a run.
func (s *Store) UpsertLiveActivity(ctx context.Context, workspaceID string, a LiveActivity) error {
	if strings.TrimSpace(a.RunKey) == "" || strings.TrimSpace(a.DeviceID) == "" || strings.TrimSpace(a.UserID) == "" {
		return errors.New("live activity requires run key, device and user")
	}
	if err := s.requireDeviceOwner(ctx, workspaceID, a.DeviceID, a.UserID); err != nil {
		return err
	}
	if a.Environment != "development" {
		a.Environment = "production"
	}
	if a.BundleID == "" {
		a.BundleID = "dev.soulacy.ios"
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO mobile_live_activities
    (workspace_id,run_key,device_id,user_id,token,environment,bundle_id,updated_at) VALUES(?,?,?,?,?,?,?,?)
    ON CONFLICT(workspace_id,run_key,device_id) DO UPDATE SET token=excluded.token, environment=excluded.environment,
      bundle_id=excluded.bundle_id, updated_at=excluded.updated_at`,
		normalizeWorkspaceID(workspaceID), a.RunKey, a.DeviceID, a.UserID, strings.ToLower(strings.TrimSpace(a.Token)),
		a.Environment, a.BundleID, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

// LiveActivities returns every phone currently showing the run.
func (s *Store) LiveActivities(ctx context.Context, workspaceID, runKey string) ([]LiveActivity, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT run_key,device_id,user_id,token,environment,bundle_id,updated_at
    FROM mobile_live_activities WHERE workspace_id=? AND run_key=?`, normalizeWorkspaceID(workspaceID), runKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LiveActivity
	for rows.Next() {
		var a LiveActivity
		var updated string
		if err := rows.Scan(&a.RunKey, &a.DeviceID, &a.UserID, &a.Token, &a.Environment, &a.BundleID, &updated); err != nil {
			return nil, err
		}
		a.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
		out = append(out, a)
	}
	return out, rows.Err()
}

// DeleteLiveActivities forgets a finished run's tokens.
func (s *Store) DeleteLiveActivities(ctx context.Context, workspaceID, runKey string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM mobile_live_activities WHERE workspace_id=? AND run_key=?`, normalizeWorkspaceID(workspaceID), runKey)
	return err
}

// PruneLiveActivities drops registrations older than maxAge so a run that
// never reported completion cannot pin a row forever.
func (s *Store) PruneLiveActivities(ctx context.Context, workspaceID string, maxAge time.Duration) error {
	cutoff := time.Now().UTC().Add(-maxAge).Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `DELETE FROM mobile_live_activities WHERE workspace_id=? AND updated_at<?`, normalizeWorkspaceID(workspaceID), cutoff)
	return err
}

// livePush is one Live Activity push, either start (push-to-start token) or
// update/end (activity token). Relays receive it verbatim at /v1/live.
type livePush struct {
	Token       string         `json:"token"`
	Environment string         `json:"environment"`
	BundleID    string         `json:"bundle_id"`
	Event       string         `json:"event"` // start | update | end
	Priority    int            `json:"priority"`
	Payload     map[string]any `json:"payload"`
}

// StartLiveActivity begins an activity on every phone of destination that
// has a push-to-start token. Phones already showing the run are skipped, so
// calling it twice is harmless.
func (a *Adapter) StartLiveActivity(ctx context.Context, workspaceID, destination string, attrs LiveAttributes, state LiveState, alert Notification) (int, error) {
	if a == nil || a.store == nil {
		return 0, errors.New("mobile delivery store is unavailable")
	}
	devices, err := a.store.LiveStartDevices(ctx, workspaceID, destination)
	if err != nil {
		return 0, err
	}
	showing, _ := a.store.LiveActivities(ctx, workspaceID, attrs.RunKey)
	already := map[string]bool{}
	for _, s := range showing {
		already[s.DeviceID] = true
	}
	if state.UpdatedAt == 0 {
		state.UpdatedAt = time.Now().Unix()
	}
	var firstErr error
	started := 0
	for _, d := range devices {
		if already[d.ID] {
			continue
		}
		aps := map[string]any{
			"timestamp":       state.UpdatedAt,
			"event":           "start",
			"content-state":   state,
			"attributes-type": "SoulacyRunAttributes",
			"attributes":      attrs,
		}
		if alert.Title != "" {
			aps["alert"] = map[string]string{"title": alert.Title, "body": alert.Body}
		}
		p := livePush{Token: d.LiveStartToken, Environment: d.PushEnvironment, BundleID: d.BundleID, Event: "start", Priority: 10,
			Payload: map[string]any{"aps": aps}}
		if err := a.deliverLive(ctx, p); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		started++
	}
	return started, firstErr
}

// UpdateLiveActivity pushes a new content state to every phone showing the
// run. Waiting states are high priority so the lock screen changes at once;
// routine progress is low priority and may be coalesced by Apple.
func (a *Adapter) UpdateLiveActivity(ctx context.Context, workspaceID, runKey string, state LiveState, alert *Notification) error {
	return a.pushLive(ctx, workspaceID, runKey, "update", state, alert, 0)
}

// EndLiveActivity pushes the final state and lets the phone dismiss the
// activity a few minutes later, then forgets the tokens.
func (a *Adapter) EndLiveActivity(ctx context.Context, workspaceID, runKey string, state LiveState, alert *Notification) error {
	err := a.pushLive(ctx, workspaceID, runKey, "end", state, alert, 5*time.Minute)
	if a != nil && a.store != nil {
		_ = a.store.DeleteLiveActivities(ctx, workspaceID, runKey)
	}
	return err
}

func (a *Adapter) pushLive(ctx context.Context, workspaceID, runKey, event string, state LiveState, alert *Notification, dismissAfter time.Duration) error {
	if a == nil || a.store == nil {
		return errors.New("mobile delivery store is unavailable")
	}
	targets, err := a.store.LiveActivities(ctx, workspaceID, runKey)
	if err != nil {
		return err
	}
	if state.UpdatedAt == 0 {
		state.UpdatedAt = time.Now().Unix()
	}
	priority := 5
	if event == "end" || state.NeedsYou || alert != nil {
		priority = 10
	}
	var firstErr error
	for _, t := range targets {
		aps := map[string]any{"timestamp": state.UpdatedAt, "event": event, "content-state": state}
		if alert != nil {
			aps["alert"] = map[string]string{"title": alert.Title, "body": alert.Body}
		}
		if dismissAfter > 0 {
			aps["dismissal-date"] = time.Now().Add(dismissAfter).Unix()
		}
		p := livePush{Token: t.Token, Environment: t.Environment, BundleID: t.BundleID, Event: event, Priority: priority,
			Payload: map[string]any{"aps": aps}}
		if err := a.deliverLive(ctx, p); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// deliverLive sends one Live Activity push over APNs when configured,
// otherwise through the relay at /v1/live.
func (a *Adapter) deliverLive(ctx context.Context, p livePush) error {
	if a.apns != nil {
		return a.apns.pushLive(ctx, p)
	}
	if a.relayURL == "" {
		return errors.New("no push transport configured")
	}
	payload, _ := json.Marshal(p)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.relayURL+"/v1/live", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if a.relayToken != "" {
		req.Header.Set("Authorization", "Bearer "+a.relayToken)
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("push relay returned %d", resp.StatusCode)
	}
	return nil
}

// ReassignUser moves everything a user owns in the mobile store to another
// user: devices, nodes, queued commands, live activities and deliveries
// addressed to them. Used once at startup to hand rows created under a
// legacy companion key id to the owner that key now authenticates as.
func (s *Store) ReassignUser(ctx context.Context, workspaceID, from, to string) (int64, error) {
	from, to = strings.TrimSpace(from), strings.TrimSpace(to)
	if from == "" || to == "" || from == to {
		return 0, nil
	}
	ws := normalizeWorkspaceID(workspaceID)
	var total int64
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`UPDATE mobile_devices SET user_id=? WHERE workspace_id=? AND user_id=?`, []any{to, ws, from}},
		{`UPDATE mobile_nodes SET user_id=? WHERE workspace_id=? AND user_id=?`, []any{to, ws, from}},
		{`UPDATE mobile_node_commands SET user_id=? WHERE workspace_id=? AND user_id=?`, []any{to, ws, from}},
		{`UPDATE mobile_live_activities SET user_id=? WHERE workspace_id=? AND user_id=?`, []any{to, ws, from}},
		{`UPDATE mobile_deliveries SET destination=? WHERE workspace_id=? AND destination=?`, []any{"user:" + to, ws, "user:" + from}},
	} {
		res, err := s.db.ExecContext(ctx, q.sql, q.args...)
		if err != nil {
			return total, err
		}
		n, _ := res.RowsAffected()
		total += n
	}
	return total, nil
}
