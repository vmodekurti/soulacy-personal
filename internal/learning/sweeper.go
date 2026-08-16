package learning

import (
	"context"
	"os"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/wsroot"
	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
)

const (
	defaultSweepInterval = 6 * time.Hour
	defaultSweepLimit    = 5000
	defaultSweepMaxRuns  = 20
)

type AgentSource interface {
	All() []*agent.Definition
}

// WorkspaceAgentSource is the tenant-aware agent listing. A source that does
// not implement it is single-tenant, and the sweep stays in the personal
// workspace rather than guessing.
type WorkspaceAgentSource interface {
	AgentSource
	AllWorkspaces() []string
	AllInWorkspace(workspaceID string) []*agent.Definition
}

type EventTailer interface {
	Tail(agentID string, n int) ([]message.Event, error)
}

// WorkspaceEventTailer is the tenant-aware history read. Same rule: a tailer
// without it is single-tenant.
type WorkspaceEventTailer interface {
	EventTailer
	TailInWorkspace(workspaceID, agentID string, n int) ([]message.Event, error)
}

type Sweeper struct {
	stores   *Stores
	actions  EventTailer
	agents   AgentSource
	log      *zap.Logger
	interval time.Duration
	limit    int
	maxRuns  int

	// workspaceStatus, when set, is consulted immediately before a workspace's
	// proposals are written (MU-025 criterion 5). See revalidate.go.
	workspaceStatus WorkspaceStatusFunc
}

type SweeperConfig struct {
	Stores   *Stores
	Actions  EventTailer
	Agents   AgentSource
	Logger   *zap.Logger
	Interval time.Duration
	Limit    int
	MaxRuns  int
}

type SweepResult struct {
	AgentsReviewed int
	RunsReviewed   int
	Created        int
}

func NewSweeper(cfg SweeperConfig) *Sweeper {
	interval := cfg.Interval
	if interval == 0 {
		interval = IntervalFromEnv()
	}
	if interval == 0 {
		interval = defaultSweepInterval
	}
	limit := cfg.Limit
	if limit <= 0 {
		limit = defaultSweepLimit
	}
	maxRuns := cfg.MaxRuns
	if maxRuns <= 0 {
		maxRuns = defaultSweepMaxRuns
	}
	log := cfg.Logger
	if log == nil {
		log = zap.NewNop()
	}
	return &Sweeper{
		stores:   cfg.Stores,
		actions:  cfg.Actions,
		agents:   cfg.Agents,
		log:      log,
		interval: interval,
		limit:    limit,
		maxRuns:  maxRuns,
	}
}

func IntervalFromEnv() time.Duration {
	raw := strings.TrimSpace(os.Getenv("SOULACY_LEARNING_SWEEP_INTERVAL"))
	if raw == "" {
		return 0
	}
	if raw == "0" || strings.EqualFold(raw, "off") || strings.EqualFold(raw, "disabled") {
		return -1
	}
	if d, err := time.ParseDuration(raw); err == nil {
		return d
	}
	if seconds, err := strconv.Atoi(raw); err == nil {
		return time.Duration(seconds) * time.Second
	}
	return 0
}

func (s *Sweeper) Start(ctx context.Context) {
	if s == nil || s.stores == nil || s.actions == nil || s.agents == nil || s.interval < 0 {
		return
	}
	go func() {
		timer := time.NewTimer(s.interval)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				if _, err := s.SweepOnce(ctx); err != nil {
					s.log.Warn("learning reflection sweep failed", zap.Error(err))
				}
				timer.Reset(s.interval)
			}
		}
	}()
	s.log.Info("learning reflection sweeper ready", zap.Duration("interval", s.interval))
}

func (s *Sweeper) SweepOnce(ctx context.Context) (SweepResult, error) {
	var result SweepResult
	if s == nil || s.stores == nil || s.actions == nil || s.agents == nil {
		return result, nil
	}
	// The sweep covers every tenant, but touches one at a time: each
	// workspace's agents are listed, tailed, and written back within that
	// workspace. Reading one tenant's runs and proposing into another's queue
	// would be a rule someone else could accept.
	for _, workspaceID := range s.workspaces() {
		swept, err := s.sweepWorkspace(ctx, workspaceID)
		result.AgentsReviewed += swept.AgentsReviewed
		result.RunsReviewed += swept.RunsReviewed
		result.Created += swept.Created
		if err != nil {
			return result, err
		}
	}
	return result, nil
}

// workspaces lists the tenants to sweep. A single-tenant agent source yields
// just the personal workspace, which is what its agents are.
func (s *Sweeper) workspaces() []string {
	scoped, ok := s.agents.(WorkspaceAgentSource)
	if !ok {
		return []string{wsroot.PersonalWorkspaceID}
	}
	return scoped.AllWorkspaces()
}

func (s *Sweeper) agentsIn(workspaceID string) []*agent.Definition {
	if scoped, ok := s.agents.(WorkspaceAgentSource); ok {
		return scoped.AllInWorkspace(workspaceID)
	}
	return s.agents.All()
}

func (s *Sweeper) tailIn(workspaceID, agentID string) ([]message.Event, error) {
	if scoped, ok := s.actions.(WorkspaceEventTailer); ok {
		return scoped.TailInWorkspace(workspaceID, agentID, s.limit)
	}
	if workspaceID != wsroot.PersonalWorkspaceID {
		// Falling back to the unscoped tail would read the personal
		// workspace's runs and propose them into this tenant's queue.
		return nil, nil
	}
	return s.actions.Tail(agentID, s.limit)
}

func (s *Sweeper) sweepWorkspace(ctx context.Context, workspaceID string) (SweepResult, error) {
	var result SweepResult
	store := s.stores.For(workspaceID)
	if store == nil {
		return result, nil
	}
	// MU-025 criterion 5. Checked here rather than at Start: a sweep runs
	// every six hours and takes as long as the evidence is wide, so a status
	// read at startup is a policy nobody is looking at by the time anything
	// commits. One workspace being uncommittable skips that workspace, not
	// the sweep — a bare return would let one suspended tenant stop learning
	// for every other.
	if err := s.revalidateWorkspace(ctx, workspaceID); err != nil {
		s.logSkip(workspaceID, err)
		return result, nil
	}
	for _, def := range s.agentsIn(workspaceID) {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if def == nil || !def.Learning.Enabled || !def.Learning.AutoPropose {
			continue
		}
		result.AgentsReviewed++
		events, err := s.tailIn(workspaceID, def.ID)
		if err != nil {
			s.log.Warn("learning reflection tail failed", zap.String("agent", def.ID), zap.Error(err))
			continue
		}
		runs := RunsFromRecentEvents(events, def.ID, s.maxRuns)
		for _, run := range runs {
			if err := ctx.Err(); err != nil {
				return result, err
			}
			if !run.FoundIn || !run.FoundOut {
				continue
			}
			result.RunsReviewed++
			// Re-read the agent from its own workspace immediately before
			// writing. The loop's def was captured before the tail, and a
			// definition that has since had learning turned off must not
			// still produce a rule somebody can accept.
			current, err := s.revalidateAgent(workspaceID, def)
			if err != nil {
				s.logSkip(workspaceID, err)
				break
			}
			created, err := s.reflectRun(store, current, run)
			if err != nil {
				s.log.Warn("learning reflection proposal failed",
					zap.String("agent", def.ID),
					zap.String("session", run.SessionID),
					zap.Error(err),
				)
				continue
			}
			result.Created += created
		}
	}
	return result, nil
}

// reflectRun takes the store explicitly rather than reading a field, so a
// proposal cannot be written into a workspace other than the one whose runs
// produced it.
func (s *Sweeper) reflectRun(store *Store, def *agent.Definition, run RunEvidence) (int, error) {
	minChars := def.Learning.MinChars
	if minChars <= 0 {
		minChars = 80
	}
	maxProposals := def.Learning.MaxProposals
	if maxProposals <= 0 {
		maxProposals = 3
	}
	proposals := BuildProposals(BuildInput{
		AgentID:      def.ID,
		AgentName:    def.Name,
		SessionID:    run.SessionID,
		Channel:      run.Channel,
		UserText:     run.UserText,
		ReplyText:    run.ReplyText,
		ToolsUsed:    run.Tools,
		Source:       "background_reflection",
		MinChars:     minChars,
		MaxProposals: maxProposals,
	})
	created := 0
	for _, p := range proposals {
		if p.Meta == nil {
			p.Meta = map[string]string{}
		}
		p.Meta["background_reflection"] = "true"
		p.Meta["reflection_sweep"] = "true"
		key := dedupeKey(p)
		alreadyPending, err := pendingDedupeExists(store, def.ID, key)
		if err != nil {
			return created, err
		}
		added, err := store.Add(p)
		if err != nil {
			return created, err
		}
		if !alreadyPending && added.Meta["dedupe"] == key && added.Source == p.Source {
			created++
		}
	}
	return created, nil
}

func pendingDedupeExists(store *Store, agentID, key string) (bool, error) {
	existing, err := store.List(agentID, StatusPending, 0)
	if err != nil {
		return false, err
	}
	for _, p := range existing {
		if p.Meta["dedupe"] == key {
			return true, nil
		}
	}
	return false, nil
}
