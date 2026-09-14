package mobile

// A node command is claimed once before the phone performs an action. A lost
// response or phone restart never requeues a claimed command: its outcome is
// uncertain until the original device supplies its saved result.
import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
)

const nodeTimeLayout = "2006-01-02T15:04:05.000000000Z07:00"

const nodeSchema = `
CREATE TABLE IF NOT EXISTS mobile_nodes (
 workspace_id TEXT NOT NULL, id TEXT NOT NULL, user_id TEXT NOT NULL,
 registration TEXT NOT NULL, updated_at TEXT NOT NULL,
 PRIMARY KEY (workspace_id,id)
);
CREATE TABLE IF NOT EXISTS mobile_node_commands (
 workspace_id TEXT NOT NULL, id TEXT NOT NULL, device_id TEXT NOT NULL,
 user_id TEXT NOT NULL, command TEXT NOT NULL, params TEXT NOT NULL,
 status TEXT NOT NULL, result TEXT, error TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL, expires_at TEXT NOT NULL, completed_at TEXT,
 PRIMARY KEY (workspace_id,id)
);
CREATE INDEX IF NOT EXISTS idx_mobile_node_pending
 ON mobile_node_commands(workspace_id,device_id,status,created_at);
`

var ErrNodeConflict = errors.New("mobile node command has already been claimed or settled")
var ErrNodeInvalid = errors.New("invalid mobile node request")

// SupportedNodeCommands is the protocol allowlist, not a grant. The device must
// also advertise a capability and check its native permission before executing.
var SupportedNodeCommands = []string{
	"device.info", "location.current", "contacts.search", "calendar.events",
	"reminders.list", "motion.current", "system.notify", "camera.capture",
	"photos.pick", "canvas.present", "canvas.snapshot",
	// Slice 2: signals only a phone has. Both are read-only and, like every
	// other command, run only when the phone advertised the capability.
	"health.summary", "focus.status",
}

type Node struct {
	DeviceID       string            `json:"device_id"`
	Name           string            `json:"name"`
	Platform       string            `json:"platform"`
	Model          string            `json:"model"`
	OSVersion      string            `json:"os_version"`
	AppVersion     string            `json:"app_version"`
	Capabilities   []string          `json:"capabilities"`
	Permissions    map[string]string `json:"permissions"`
	ForegroundOnly bool              `json:"foreground_only"`
	UpdatedAt      time.Time         `json:"updated_at"`
}

type NodeCommand struct {
	ID          string          `json:"id"`
	DeviceID    string          `json:"device_id"`
	Command     string          `json:"command"`
	Params      json.RawMessage `json:"params"`
	Status      string          `json:"status"`
	Result      json.RawMessage `json:"result,omitempty"`
	Error       string          `json:"error,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
	ExpiresAt   time.Time       `json:"expires_at"`
	CompletedAt *time.Time      `json:"completed_at,omitempty"`
}

func (s *Store) RegisterNode(ctx context.Context, workspace, user string, n Node) error {
	if strings.TrimSpace(user) == "" || strings.TrimSpace(n.DeviceID) == "" || len(n.DeviceID) > 128 || len(n.Name) > 200 || len(n.Capabilities) > 32 {
		return fmt.Errorf("%w: device identity is required", ErrNodeInvalid)
	}
	if n.Platform != "ios" && n.Platform != "watchos" {
		return fmt.Errorf("%w: unsupported platform", ErrNodeInvalid)
	}
	for _, capability := range n.Capabilities {
		if !slices.Contains(SupportedNodeCommands, capability) {
			return fmt.Errorf("%w: unsupported command %q", ErrNodeInvalid, capability)
		}
	}
	n.ForegroundOnly = true
	n.UpdatedAt = time.Now().UTC()
	raw, err := json.Marshal(n)
	if err != nil {
		return err
	}
	if len(raw) > 16384 {
		return fmt.Errorf("%w: registration is too large", ErrNodeInvalid)
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO mobile_nodes(workspace_id,id,user_id,registration,updated_at)
 VALUES(?,?,?,?,?) ON CONFLICT(workspace_id,id) DO UPDATE SET registration=excluded.registration,updated_at=excluded.updated_at
 WHERE mobile_nodes.user_id=excluded.user_id`, normalizeWorkspaceID(workspace), n.DeviceID, user, string(raw), n.UpdatedAt.Format(nodeTimeLayout))
	if err != nil {
		return err
	}
	count, err := res.RowsAffected()
	if err == nil && count == 0 {
		return ErrDeviceOwnership
	}
	return err
}

func (s *Store) Node(ctx context.Context, workspace, user, deviceID string) (Node, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT registration FROM mobile_nodes WHERE workspace_id=? AND user_id=? AND id=?`, normalizeWorkspaceID(workspace), user, deviceID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return Node{}, ErrDeviceOwnership
	}
	if err != nil {
		return Node{}, err
	}
	var n Node
	err = json.Unmarshal([]byte(raw), &n)
	return n, err
}

func (s *Store) Nodes(ctx context.Context, workspace, user string) ([]Node, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT registration FROM mobile_nodes WHERE workspace_id=? AND user_id=? ORDER BY updated_at DESC LIMIT 100`, normalizeWorkspaceID(workspace), user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Node{}
	for rows.Next() {
		var raw string
		var n Node
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &n); err != nil {
			return nil, err
		}
		result = append(result, n)
	}
	return result, rows.Err()
}

func (s *Store) EnqueueNodeCommand(ctx context.Context, workspace, user string, command NodeCommand) (NodeCommand, error) {
	n, err := s.Node(ctx, workspace, user, command.DeviceID)
	if err != nil {
		return NodeCommand{}, err
	}
	if !slices.Contains(SupportedNodeCommands, command.Command) || !slices.Contains(n.Capabilities, command.Command) {
		return NodeCommand{}, fmt.Errorf("%w: device has not enabled %q", ErrNodeInvalid, command.Command)
	}
	if command.ID == "" {
		command.ID = uuid.NewString()
	}
	if len(command.ID) > 128 {
		return NodeCommand{}, ErrNodeInvalid
	}
	if len(command.Params) == 0 {
		command.Params = json.RawMessage(`{}`)
	}
	var params map[string]any
	if len(command.Params) > 65536 || json.Unmarshal(command.Params, &params) != nil || params == nil {
		return NodeCommand{}, fmt.Errorf("%w: params must be an object up to 64 KiB", ErrNodeInvalid)
	}
	if command.Command == "canvas.present" {
		if err := ValidateCanvasParams(params); err != nil {
			return NodeCommand{}, fmt.Errorf("%w: %v", ErrNodeInvalid, err)
		}
	}
	command.Params, _ = json.Marshal(params)
	command.CreatedAt = time.Now().UTC()
	if command.ExpiresAt.IsZero() {
		command.ExpiresAt = command.CreatedAt.Add(5 * time.Minute)
	}
	if command.ExpiresAt.Before(command.CreatedAt) || command.ExpiresAt.After(command.CreatedAt.Add(15*time.Minute)) {
		return NodeCommand{}, fmt.Errorf("%w: expiry must be within 15 minutes", ErrNodeInvalid)
	}
	command.Status = "queued"
	res, err := s.db.ExecContext(ctx, `INSERT INTO mobile_node_commands(workspace_id,id,device_id,user_id,command,params,status,created_at,expires_at)
 SELECT ?,?,?,?,?,?,'queued',?,? WHERE (SELECT COUNT(*) FROM mobile_node_commands WHERE workspace_id=? AND device_id=? AND status='queued' AND expires_at>?)<100
 ON CONFLICT(workspace_id,id) DO NOTHING`, normalizeWorkspaceID(workspace), command.ID, command.DeviceID, user, command.Command, string(command.Params), command.CreatedAt.Format(nodeTimeLayout), command.ExpiresAt.UTC().Format(nodeTimeLayout), normalizeWorkspaceID(workspace), command.DeviceID, command.CreatedAt.Format(nodeTimeLayout))
	if err != nil {
		return NodeCommand{}, err
	}
	count, err := res.RowsAffected()
	if err != nil {
		return NodeCommand{}, err
	}
	if count == 0 {
		previous, err := s.NodeCommand(ctx, workspace, user, command.DeviceID, command.ID)
		if err != nil {
			return NodeCommand{}, ErrNodeConflict
		}
		if previous.Command != command.Command || string(previous.Params) != string(command.Params) {
			return NodeCommand{}, ErrNodeConflict
		}
		return previous, nil
	}
	return command, nil
}

const nodeCommandColumns = `id,device_id,command,params,status,result,error,created_at,expires_at,completed_at`

func scanNodeCommand(row interface{ Scan(...any) error }) (NodeCommand, error) {
	var command NodeCommand
	var params, created, expires string
	var result, completed sql.NullString
	if err := row.Scan(&command.ID, &command.DeviceID, &command.Command, &params, &command.Status, &result, &command.Error, &created, &expires, &completed); err != nil {
		return command, err
	}
	command.Params = json.RawMessage(params)
	if result.Valid {
		command.Result = json.RawMessage(result.String)
	}
	var err error
	command.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return command, err
	}
	command.ExpiresAt, err = time.Parse(time.RFC3339Nano, expires)
	if err != nil {
		return command, err
	}
	if completed.Valid {
		t, err := time.Parse(time.RFC3339Nano, completed.String)
		if err != nil {
			return command, err
		}
		command.CompletedAt = &t
	}
	if command.Status == "queued" && time.Now().After(command.ExpiresAt) {
		command.Status = "expired"
	}
	return command, nil
}

func (s *Store) NodeCommand(ctx context.Context, workspace, user, device, id string) (NodeCommand, error) {
	return scanNodeCommand(s.db.QueryRowContext(ctx, `SELECT `+nodeCommandColumns+` FROM mobile_node_commands WHERE workspace_id=? AND user_id=? AND device_id=? AND id=?`, normalizeWorkspaceID(workspace), user, device, id))
}

func (s *Store) PendingNodeCommands(ctx context.Context, workspace, user, device string) ([]NodeCommand, error) {
	if _, err := s.Node(ctx, workspace, user, device); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+nodeCommandColumns+` FROM mobile_node_commands WHERE workspace_id=? AND user_id=? AND device_id=? AND status='queued' AND expires_at>? ORDER BY created_at LIMIT 20`, normalizeWorkspaceID(workspace), user, device, time.Now().UTC().Format(nodeTimeLayout))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	commands := []NodeCommand{}
	for rows.Next() {
		command, err := scanNodeCommand(rows)
		if err != nil {
			return nil, err
		}
		commands = append(commands, command)
	}
	return commands, rows.Err()
}

func (s *Store) ClaimNodeCommand(ctx context.Context, workspace, user, device, id string) (NodeCommand, error) {
	if _, err := s.Node(ctx, workspace, user, device); err != nil {
		return NodeCommand{}, err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE mobile_node_commands SET status='running' WHERE workspace_id=? AND user_id=? AND device_id=? AND id=? AND status='queued' AND expires_at>?
 AND EXISTS(SELECT 1 FROM mobile_nodes n, json_each(n.registration,'$.capabilities') cap
 WHERE n.workspace_id=mobile_node_commands.workspace_id AND n.id=mobile_node_commands.device_id AND n.user_id=mobile_node_commands.user_id AND cap.value=mobile_node_commands.command)`, normalizeWorkspaceID(workspace), user, device, id, time.Now().UTC().Format(nodeTimeLayout))
	if err != nil {
		return NodeCommand{}, err
	}
	count, err := res.RowsAffected()
	if err != nil {
		return NodeCommand{}, err
	}
	if count != 1 {
		return NodeCommand{}, ErrNodeConflict
	}
	return s.NodeCommand(ctx, workspace, user, device, id)
}

func (s *Store) FinishNodeCommand(ctx context.Context, workspace, user, device, id, status string, result json.RawMessage, errorText string) (NodeCommand, error) {
	if _, err := s.Node(ctx, workspace, user, device); err != nil {
		return NodeCommand{}, err
	}
	if !slices.Contains([]string{"completed", "failed", "declined", "expired"}, status) || len(result) > 1048576 || len(errorText) > 4096 || (len(result) > 0 && !json.Valid(result)) {
		return NodeCommand{}, ErrNodeInvalid
	}
	if len(result) == 0 {
		result = json.RawMessage(`null`)
	}
	var compact any
	_ = json.Unmarshal(result, &compact)
	result, _ = json.Marshal(compact)
	res, err := s.db.ExecContext(ctx, `UPDATE mobile_node_commands SET status=?,result=?,error=?,completed_at=? WHERE workspace_id=? AND user_id=? AND device_id=? AND id=? AND status='running'`, status, string(result), errorText, time.Now().UTC().Format(nodeTimeLayout), normalizeWorkspaceID(workspace), user, device, id)
	if err != nil {
		return NodeCommand{}, err
	}
	count, err := res.RowsAffected()
	if err != nil {
		return NodeCommand{}, err
	}
	command, err := s.NodeCommand(ctx, workspace, user, device, id)
	if err != nil {
		return NodeCommand{}, err
	}
	if count == 0 && (command.Status != status || string(command.Result) != string(result) || command.Error != errorText) {
		return NodeCommand{}, ErrNodeConflict
	}
	return command, nil
}

// CanvasComponentTypes are the typed building blocks a canvas.present may
// carry in "components". The phone renders them natively; anything else is
// refused here so an agent learns the vocabulary from the error.
var CanvasComponentTypes = []string{"text", "checklist", "form", "chart", "metric"}

// CanvasFieldKinds are the input kinds a form field may use.
var CanvasFieldKinds = []string{"text", "number", "choice", "toggle", "date"}

// ValidateCanvasParams checks the typed-component shape of a canvas.present
// request. Legacy title/body/items documents pass unchanged. Limits keep a
// card readable on a phone and bound the result an agent can ask for.
func ValidateCanvasParams(params map[string]any) error {
	raw, ok := params["components"]
	if !ok || raw == nil {
		return nil
	}
	components, ok := raw.([]any)
	if !ok {
		return errors.New("components must be an array")
	}
	if len(components) > 12 {
		return errors.New("a canvas may carry at most 12 components")
	}
	forms := 0
	for i, c := range components {
		obj, ok := c.(map[string]any)
		if !ok {
			return fmt.Errorf("component %d must be an object", i)
		}
		kind, _ := obj["type"].(string)
		if !slices.Contains(CanvasComponentTypes, kind) {
			return fmt.Errorf("component %d has unknown type %q (use one of %s)", i, kind, strings.Join(CanvasComponentTypes, ", "))
		}
		switch kind {
		case "form":
			forms++
			if forms > 1 {
				return errors.New("a canvas may carry only one form")
			}
			fields, _ := obj["fields"].([]any)
			if len(fields) == 0 || len(fields) > 12 {
				return errors.New("a form needs between 1 and 12 fields")
			}
			for j, f := range fields {
				field, ok := f.(map[string]any)
				if !ok {
					return fmt.Errorf("form field %d must be an object", j)
				}
				name, _ := field["name"].(string)
				if strings.TrimSpace(name) == "" {
					return fmt.Errorf("form field %d needs a name", j)
				}
				if k, _ := field["kind"].(string); k != "" && !slices.Contains(CanvasFieldKinds, k) {
					return fmt.Errorf("form field %q has unknown kind %q", name, k)
				}
				if opts, _ := field["options"].([]any); len(opts) > 20 {
					return fmt.Errorf("form field %q may offer at most 20 options", name)
				}
			}
		case "checklist":
			items, _ := obj["items"].([]any)
			if len(items) == 0 || len(items) > 30 {
				return errors.New("a checklist needs between 1 and 30 items")
			}
		case "chart":
			series, _ := obj["series"].([]any)
			if len(series) == 0 || len(series) > 6 {
				return errors.New("a chart needs between 1 and 6 series")
			}
			for _, sr := range series {
				m, _ := sr.(map[string]any)
				if values, _ := m["values"].([]any); len(values) == 0 || len(values) > 60 {
					return errors.New("each chart series needs between 1 and 60 values")
				}
			}
		}
	}
	return nil
}
