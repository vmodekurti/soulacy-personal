// Package safeundo implements reviewed, version-conditional changes to named
// external resources. It never infers reversibility from a model's output.
package safeundo

import (
	"encoding/json"
	"errors"
	"time"
)

const (
	MaxBody       = 64 << 10
	MaxActions    = 8
	MaxFields     = 32
	MaxJobBytes   = 2 << 20
	MaxStoreBytes = 128 << 20
	// Shorter configured HTTP deadlines still take precedence. The protocol
	// ceiling bounds a single serialized multi-resource interaction.
	MaxOperationDuration = 45 * time.Second
)

var (
	ErrInvalid     = errors.New("safe undo: invalid request")
	ErrNotFound    = errors.New("safe undo: not found")
	ErrConflict    = errors.New("safe undo: state changed; review a fresh preview")
	ErrUnavailable = errors.New("safe undo: resource unavailable")
	ErrPermission  = errors.New("safe undo: current permission required")
	ErrUncertain   = errors.New("safe undo: result unknown; inspect the resource before reconciling")
	ErrLimit       = errors.New("safe undo: storage or operation limit reached")
)

type Config struct {
	Resources []Resource `mapstructure:"resources"`
}

// URLs and credentials are operator configuration, never tool arguments.
// Only existing resources are supported. Creating/deleting resources and
// side effects such as notifications, charges, and sends are not reversible.
type Resource struct {
	ID                string   `mapstructure:"id" json:"id"`
	AgentID           string   `mapstructure:"agent_id" json:"agent_id"`
	Name              string   `mapstructure:"name" json:"name"`
	Kind              string   `mapstructure:"kind" json:"kind"`
	URL               string   `mapstructure:"url" json:"-"`
	TokenEnv          string   `mapstructure:"token_env" json:"-"`
	Fields            []string `mapstructure:"fields" json:"fields"`
	AllowPrivateHost  bool     `mapstructure:"allow_private_host" json:"-"`
	AllowLoopbackHTTP bool     `mapstructure:"allow_loopback_http" json:"-"`
	// ConditionalWrites must be explicitly attested by the operator after
	// verifying this endpoint enforces strong If-Match and has no other effects.
	ConditionalWrites bool `mapstructure:"conditional_writes" json:"-"`
}

type ChangeInput struct {
	ResourceID string                     `json:"resource_id"`
	Text       *string                    `json:"text,omitempty"`
	Fields     map[string]json.RawMessage `json:"fields,omitempty"`
	Remove     []string                   `json:"remove,omitempty"`
}

type PrepareRequest struct {
	Title   string        `json:"title"`
	Changes []ChangeInput `json:"changes"`
}

type Value struct {
	Exists bool            `json:"exists"`
	JSON   json.RawMessage `json:"json,omitempty"`
	// Display is lossless JSON text for clients. Public views omit JSON so a
	// JavaScript/Swift decoder cannot silently round large numbers in a review.
	Display string `json:"display,omitempty"`
}

type FieldChange struct {
	Name   string `json:"name"`
	Before Value  `json:"before"`
	After  Value  `json:"after"`
}

type Action struct {
	ResourceID string        `json:"resource_id"`
	Name       string        `json:"name"`
	Kind       string        `json:"kind"`
	Status     string        `json:"status"` // pending, applied, undone, running, needs_review
	Fields     []FieldChange `json:"fields"`
	BeforeText *string       `json:"before_text,omitempty"`
	AfterText  *string       `json:"after_text,omitempty"`
	Reconciled bool          `json:"reconciled"`
	// These fields are persisted privately; the HTTP view omits them.
	Binding     string `json:"binding,omitempty"`
	Version     string `json:"version,omitempty"`
	Media       string `json:"media,omitempty"`
	PriorStatus string `json:"prior_status,omitempty"`
	Order       int64  `json:"order,omitempty"`
}

type Job struct {
	ID          string    `json:"id"`
	AgentID     string    `json:"agent_id"`
	RunID       string    `json:"run_id,omitempty"`
	Title       string    `json:"title"`
	Status      string    `json:"status"`
	Notice      string    `json:"notice,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	Actions     []Action  `json:"actions"`
	Revision    int64     `json:"revision"`
	UndoStarted bool      `json:"undo_started"`
	Owner       string    `json:"owner,omitempty"`
	Plan        *Plan     `json:"plan,omitempty"`
	LastToken   string    `json:"last_token,omitempty"`
	Attempts    int       `json:"attempts,omitempty"`
}

type PlanItem struct {
	Index   int    `json:"index"`
	Version string `json:"version"`
	// Reconciliation records an operator-confirmed observation, not proof
	// that a request whose acknowledgement was lost actually ran.
	ObservedStatus string `json:"observed_status,omitempty"`
}

type Plan struct {
	Token     string     `json:"token"`
	Direction string     `json:"direction"` // apply, undo, reconcile
	ExpiresAt time.Time  `json:"expires_at"`
	Items     []PlanItem `json:"items"`
}

type Review struct {
	Job       Job          `json:"job"`
	Token     string       `json:"token"`
	Direction string       `json:"direction"`
	ExpiresAt time.Time    `json:"expires_at"`
	Warning   string       `json:"warning"`
	Steps     []ReviewStep `json:"steps"`
}

type ReviewStep struct {
	Index  int    `json:"index"`
	Result string `json:"result"`
}

// Lists never include private before/after content. Fetch one receipt to review it.
type JobSummary struct {
	ID          string    `json:"id"`
	AgentID     string    `json:"agent_id"`
	Title       string    `json:"title"`
	Status      string    `json:"status"`
	UpdatedAt   time.Time `json:"updated_at"`
	ActionCount int       `json:"action_count"`
}

func (j Job) Public() Job {
	j.Owner, j.LastToken, j.Plan = "", "", nil
	j.Actions = append([]Action(nil), j.Actions...)
	for i := range j.Actions {
		j.Actions[i].Binding, j.Actions[i].Version, j.Actions[i].PriorStatus, j.Actions[i].Order = "", "", "", 0
		j.Actions[i].Media = ""
		j.Actions[i].Fields = append([]FieldChange{}, j.Actions[i].Fields...)
		for k := range j.Actions[i].Fields {
			f := &j.Actions[i].Fields[k]
			f.Before.Display, f.After.Display = string(f.Before.JSON), string(f.After.JSON)
			f.Before.JSON, f.After.JSON = nil, nil
		}
	}
	return j
}
