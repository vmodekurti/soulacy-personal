// Package workspacesettings stores configuration that is safe for a workspace
// administrator to change without mutating the deployment's config.yaml.
package workspacesettings

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/soulacy/soulacy/internal/sqlitex"
	"github.com/soulacy/soulacy/internal/wsroot"
)

const (
	SecretNamespace = "workspace-settings"
	SearchAPIKey    = "search.api_key"
)

func ProviderAPIKey(providerID string) string {
	return "llm.providers." + strings.ToLower(strings.TrimSpace(providerID)) + ".api_key"
}

type ProviderModel struct {
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
}

type Studio struct {
	ProviderModel
	Preset          string  `json:"preset,omitempty"`
	BuildUX         string  `json:"build_ux,omitempty"`
	MaxBuildTokens  int     `json:"max_build_tokens,omitempty"`
	MaxBuildCostUSD float64 `json:"max_build_cost_usd,omitempty"`
}

type LLM struct {
	Default   ProviderModel       `json:"default,omitempty"`
	Chat      ProviderModel       `json:"chat,omitempty"`
	Studio    Studio              `json:"studio,omitempty"`
	Reasoner  ProviderModel       `json:"reasoner,omitempty"`
	Providers map[string]Provider `json:"providers,omitempty"`
}

// Provider is the workspace-owned subset of provider configuration. Host
// lifecycle, filesystem and process settings deliberately do not appear here.
type Provider struct {
	BaseURL           string         `json:"base_url,omitempty"`
	Model             string         `json:"model,omitempty"`
	KeepAlive         string         `json:"keep_alive,omitempty"`
	Options           map[string]any `json:"options,omitempty"`
	PromptCaching     bool           `json:"prompt_caching,omitempty"`
	ThinkingBudget    int            `json:"thinking_budget,omitempty"`
	SafetyLevel       string         `json:"safety_level,omitempty"`
	ExtendedThinking  bool           `json:"extended_thinking,omitempty"`
	Organization      string         `json:"organization,omitempty"`
	ParallelToolCalls *bool          `json:"parallel_tool_calls,omitempty"`
}

type Search struct {
	Provider string `json:"provider,omitempty"`
	Timeout  string `json:"timeout,omitempty"`
}

type Pricing struct {
	Input  float64 `json:"input,omitempty"`
	Output float64 `json:"output,omitempty"`
}

type Costs struct {
	AlertThreshold float64            `json:"alert_threshold,omitempty"`
	Pricing        map[string]Pricing `json:"pricing,omitempty"`
}

type Ops struct {
	SLOWindow         string  `json:"slo_window,omitempty"`
	MaxFailureRate    float64 `json:"max_failure_rate,omitempty"`
	MaxIncompleteRate float64 `json:"max_incomplete_rate,omitempty"`
	MaxP95Duration    string  `json:"max_p95_run_duration,omitempty"`
	MinRuns           int     `json:"min_runs_for_signal,omitempty"`
	AlertChannel      string  `json:"alert_channel,omitempty"`
	AlertDestination  string  `json:"alert_destination,omitempty"`
	AlertMinStatus    string  `json:"alert_min_status,omitempty"`
}

type Profile struct {
	Environment string `json:"environment,omitempty"`
	Owner       string `json:"owner,omitempty"`
	Region      string `json:"region,omitempty"`
	Notes       string `json:"notes,omitempty"`
}

type Security struct {
	IntentGate string `json:"intent_gate,omitempty"`
}

type Runtime struct {
	ToolTimeout       string `json:"tool_timeout,omitempty"`
	DefaultMaxTurns   int    `json:"default_max_turns,omitempty"`
	MaxAgentCallDepth int    `json:"max_agent_call_depth,omitempty"`
}

type Settings struct {
	WorkspaceID string    `json:"workspace_id"`
	LLM         LLM       `json:"llm"`
	Search      Search    `json:"search"`
	Costs       Costs     `json:"costs"`
	Ops         Ops       `json:"ops"`
	Profile     Profile   `json:"profile"`
	Security    Security  `json:"security"`
	Runtime     Runtime   `json:"runtime"`
	UpdatedBy   string    `json:"updated_by,omitempty"`
	UpdatedAt   time.Time `json:"updated_at,omitempty"`
}

var ErrInvalid = errors.New("workspacesettings: invalid settings")

func (s Settings) Validate() error {
	if s.LLM.Studio.MaxBuildTokens < 0 || s.LLM.Studio.MaxBuildCostUSD < 0 {
		return fmt.Errorf("%w: Studio budgets must not be negative", ErrInvalid)
	}
	if p := strings.TrimSpace(s.LLM.Studio.Preset); p != "" && p != "fast_local" && p != "reliable_local" && p != "cloud_quality" {
		return fmt.Errorf("%w: invalid Studio preset", ErrInvalid)
	}
	if ux := strings.TrimSpace(s.LLM.Studio.BuildUX); ux != "" && ux != "streamed" && ux != "wizard" {
		return fmt.Errorf("%w: invalid Studio build experience", ErrInvalid)
	}
	if p := strings.ToLower(strings.TrimSpace(s.Search.Provider)); p != "" && p != "tavily" && p != "serper" && p != "ollama" {
		return fmt.Errorf("%w: search provider must be tavily, serper, or ollama", ErrInvalid)
	}
	if raw := strings.TrimSpace(s.Search.Timeout); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 || d > 10*time.Minute {
			return fmt.Errorf("%w: search timeout must be a positive duration no greater than 10m", ErrInvalid)
		}
	}
	if s.Costs.AlertThreshold < 0 || s.Costs.AlertThreshold > 1 {
		return fmt.Errorf("%w: cost alert threshold must be between 0 and 1", ErrInvalid)
	}
	for selector, pricing := range s.Costs.Pricing {
		if strings.TrimSpace(selector) == "" || pricing.Input < 0 || pricing.Output < 0 {
			return fmt.Errorf("%w: pricing selectors must be named and rates must not be negative", ErrInvalid)
		}
	}
	if s.Ops.MaxFailureRate < 0 || s.Ops.MaxFailureRate > 1 || s.Ops.MaxIncompleteRate < 0 || s.Ops.MaxIncompleteRate > 1 {
		return fmt.Errorf("%w: SLO rates must be between 0 and 1", ErrInvalid)
	}
	for _, raw := range []string{s.Ops.SLOWindow, s.Ops.MaxP95Duration, s.Runtime.ToolTimeout} {
		if raw != "" {
			if d, err := time.ParseDuration(strings.TrimSpace(raw)); err != nil || d <= 0 {
				return fmt.Errorf("%w: durations must be positive Go durations", ErrInvalid)
			}
		}
	}
	if s.Ops.MinRuns < 0 || s.Runtime.DefaultMaxTurns < 0 || s.Runtime.MaxAgentCallDepth < 0 {
		return fmt.Errorf("%w: runtime and SLO limits must not be negative", ErrInvalid)
	}
	if v := strings.TrimSpace(s.Ops.AlertMinStatus); v != "" && v != "warn" && v != "fail" {
		return fmt.Errorf("%w: alert status must be warn or fail", ErrInvalid)
	}
	if v := strings.TrimSpace(s.Profile.Environment); v != "" && v != "local" && v != "development" && v != "staging" && v != "production" {
		return fmt.Errorf("%w: invalid workspace environment", ErrInvalid)
	}
	if v := strings.TrimSpace(s.Security.IntentGate); v != "" && v != "off" && v != "prompt" && v != "deny" {
		return fmt.Errorf("%w: intent gate must be off, prompt, or deny", ErrInvalid)
	}
	return nil
}

type Store struct{ db *sql.DB }

const schema = `CREATE TABLE IF NOT EXISTS workspace_settings(
 workspace_id TEXT PRIMARY KEY,
 settings_json TEXT NOT NULL,
 updated_by TEXT NOT NULL DEFAULT '',
 updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);`

func NewStore(path string) (*Store, error) {
	db, err := sqlitex.Open(path, sqlitex.DefaultOptions())
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) Get(ctx context.Context, workspaceID string) (Settings, error) {
	if s == nil || s.db == nil {
		return Settings{}, errors.New("workspacesettings: store unavailable")
	}
	workspaceID = wsroot.Normalize(workspaceID)
	out := Settings{WorkspaceID: workspaceID}
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT settings_json, updated_by, updated_at FROM workspace_settings WHERE workspace_id=?`, workspaceID).Scan(&raw, &out.UpdatedBy, &out.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return Settings{}, err
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return Settings{}, fmt.Errorf("workspacesettings: decode: %w", err)
	}
	out.WorkspaceID = workspaceID
	return out, nil
}

func (s *Store) Set(ctx context.Context, workspaceID, actor string, in Settings) (Settings, error) {
	if s == nil || s.db == nil {
		return Settings{}, errors.New("workspacesettings: store unavailable")
	}
	if err := in.Validate(); err != nil {
		return Settings{}, err
	}
	in.WorkspaceID = wsroot.Normalize(workspaceID)
	in.UpdatedBy = strings.TrimSpace(actor)
	in.UpdatedAt = time.Now().UTC()
	raw, err := json.Marshal(in)
	if err != nil {
		return Settings{}, err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO workspace_settings(workspace_id,settings_json,updated_by,updated_at) VALUES(?,?,?,?)
	 ON CONFLICT(workspace_id) DO UPDATE SET settings_json=excluded.settings_json,updated_by=excluded.updated_by,updated_at=excluded.updated_at`, in.WorkspaceID, string(raw), in.UpdatedBy, in.UpdatedAt)
	if err != nil {
		return Settings{}, err
	}
	return in, nil
}

func (s *Store) PurgeWorkspace(ctx context.Context, workspaceID string) (int64, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("workspacesettings: store unavailable")
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM workspace_settings WHERE workspace_id=?`, wsroot.Normalize(workspaceID))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
