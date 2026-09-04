package learning

import (
	"context"
	"os"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"

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

type EventTailer interface {
	Tail(agentID string, n int) ([]message.Event, error)
}

type Sweeper struct {
	store    *Store
	actions  EventTailer
	agents   AgentSource
	log      *zap.Logger
	interval time.Duration
	limit    int
	maxRuns  int
}

type SweeperConfig struct {
	Store    *Store
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
		store:    cfg.Store,
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
	if s == nil || s.store == nil || s.actions == nil || s.agents == nil || s.interval < 0 {
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
	if s == nil || s.store == nil || s.actions == nil || s.agents == nil {
		return result, nil
	}
	for _, def := range s.agents.All() {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if def == nil || !def.Learning.Enabled || !def.Learning.AutoPropose {
			continue
		}
		result.AgentsReviewed++
		events, err := s.actions.Tail(def.ID, s.limit)
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
			created, err := s.reflectRun(def, run)
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

func (s *Sweeper) reflectRun(def *agent.Definition, run RunEvidence) (int, error) {
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
		alreadyPending, err := s.pendingDedupeExists(def.ID, key)
		if err != nil {
			return created, err
		}
		added, err := s.store.Add(p)
		if err != nil {
			return created, err
		}
		if !alreadyPending && added.Meta["dedupe"] == key && added.Source == p.Source {
			created++
		}
	}
	return created, nil
}

func (s *Sweeper) pendingDedupeExists(agentID, key string) (bool, error) {
	existing, err := s.store.List(agentID, StatusPending, 0)
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
