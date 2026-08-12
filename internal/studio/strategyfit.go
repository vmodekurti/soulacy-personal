package studio

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/soulacy/soulacy/pkg/message"
)

const (
	StrategyFitMinRuns          = 50
	StrategyFitSuccessThreshold = 0.80
)

// StrategyFit is aggregate, local telemetry. It contains no prompts, outputs,
// identities, or session data—only the compatibility tuple and counters.
type StrategyFit struct {
	Provider    string    `json:"provider,omitempty"`
	Model       string    `json:"model"`
	Strategy    string    `json:"strategy"`
	Passes      int       `json:"passes"`
	Failures    int       `json:"failures"`
	SuccessRate float64   `json:"success_rate"`
	Unreliable  bool      `json:"unreliable"`
	UpdatedAt   time.Time `json:"updated_at"`
	RunIDs      []string  `json:"run_ids,omitempty"`
}

type StrategyFitStore struct {
	path string
	mu   sync.Mutex
}

func NewStrategyFitStore(path string) *StrategyFitStore { return &StrategyFitStore{path: path} }

func (s *StrategyFitStore) Record(model, strategy string, success bool) error {
	return s.RecordProvider("", model, strategy, success)
}

func (s *StrategyFitStore) RecordProvider(provider, model, strategy string, success bool) error {
	return s.RecordProviderRun(provider, model, strategy, "", success)
}

func (s *StrategyFitStore) RecordProviderRun(provider, model, strategy, runID string, success bool) error {
	provider = strings.ToLower(strings.TrimSpace(provider))
	model = strings.TrimSpace(model)
	strategy = normalizeFitStrategy(strategy)
	if model == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows := s.loadLocked()
	key := provider + "\x00" + strings.ToLower(model) + "\x00" + strategy
	found := false
	for i := range rows {
		if strings.ToLower(strings.TrimSpace(rows[i].Provider))+"\x00"+strings.ToLower(rows[i].Model)+"\x00"+normalizeFitStrategy(rows[i].Strategy) != key {
			continue
		}
		if runID != "" {
			for _, seen := range rows[i].RunIDs {
				if seen == runID {
					return nil
				}
			}
			rows[i].RunIDs = append(rows[i].RunIDs, runID)
			if len(rows[i].RunIDs) > 1000 {
				rows[i].RunIDs = rows[i].RunIDs[len(rows[i].RunIDs)-1000:]
			}
		}
		if success {
			rows[i].Passes++
		} else {
			rows[i].Failures++
		}
		rows[i].UpdatedAt = time.Now().UTC()
		rows[i] = evaluateStrategyFit(rows[i])
		found = true
		break
	}
	if !found {
		row := StrategyFit{Provider: provider, Model: model, Strategy: strategy}
		if runID != "" {
			row.RunIDs = []string{runID}
		}
		if success {
			row.Passes = 1
		} else {
			row.Failures = 1
		}
		row.UpdatedAt = time.Now().UTC()
		rows = append(rows, evaluateStrategyFit(row))
	}
	return s.writeLocked(rows)
}

func (s *StrategyFitStore) ForModel(model string) []StrategyFit {
	return s.ForProviderModel("", model)
}

func (s *StrategyFitStore) ForProviderModel(provider, model string) []StrategyFit {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []StrategyFit
	for _, row := range s.loadLocked() {
		providerMatches := strings.TrimSpace(provider) == "" || strings.EqualFold(strings.TrimSpace(row.Provider), strings.TrimSpace(provider))
		if providerMatches && strings.EqualFold(strings.TrimSpace(row.Model), strings.TrimSpace(model)) {
			out = append(out, evaluateStrategyFit(row))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Strategy < out[j].Strategy })
	return out
}

func (s *StrategyFitStore) Unreliable(model string) []string {
	return s.UnreliableProvider("", model)
}

func (s *StrategyFitStore) UnreliableProvider(provider, model string) []string {
	var out []string
	for _, row := range s.ForProviderModel(provider, model) {
		if row.Unreliable {
			out = append(out, row.Strategy)
		}
	}
	return out
}

func evaluateStrategyFit(row StrategyFit) StrategyFit {
	total := row.Passes + row.Failures
	if total > 0 {
		row.SuccessRate = float64(row.Passes) / float64(total)
	}
	row.Unreliable = total >= StrategyFitMinRuns && row.SuccessRate < StrategyFitSuccessThreshold
	row.Strategy = normalizeFitStrategy(row.Strategy)
	return row
}

func normalizeFitStrategy(strategy string) string {
	strategy = strings.ToLower(strings.TrimSpace(strategy))
	if strategy == "" {
		return "auto"
	}
	return strategy
}

func (s *StrategyFitStore) loadLocked() []StrategyFit {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return nil
	}
	var rows []StrategyFit
	if json.Unmarshal(data, &rows) != nil {
		return nil
	}
	return rows
}

func (s *StrategyFitStore) writeLocked(rows []StrategyFit) error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".studio-strategy-fit-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, s.path)
}

type StrategyResolver func(agentID string) (model, strategy string, ok bool)

// StrategyFitCollector records exactly one terminal outcome per run. Record is
// dispatched asynchronously so action-log emission is never coupled to disk IO.
type StrategyFitCollector struct {
	store    *StrategyFitStore
	resolve  StrategyResolver
	mu       sync.Mutex
	terminal map[string]struct{}
	wg       sync.WaitGroup
	jobs     chan strategyFitJob
}

type strategyFitJob struct {
	provider, model, strategy, runID string
	success                          bool
}

func NewStrategyFitCollector(store *StrategyFitStore, resolve StrategyResolver) *StrategyFitCollector {
	c := &StrategyFitCollector{store: store, resolve: resolve, terminal: make(map[string]struct{}), jobs: make(chan strategyFitJob, 256)}
	go func() {
		for job := range c.jobs {
			_ = c.store.RecordProviderRun(job.provider, job.model, job.strategy, job.runID, job.success)
			c.wg.Done()
		}
	}()
	return c
}

func (c *StrategyFitCollector) Observe(event message.Event) {
	if c == nil || c.store == nil || event.AgentID == "" {
		return
	}
	if event.Type != "run.completed" {
		return
	}
	var terminal struct {
		RunID    string `json:"run_id"`
		Provider string `json:"provider"`
		Model    string `json:"model"`
		Strategy string `json:"strategy"`
		Success  bool   `json:"success"`
	}
	raw, _ := json.Marshal(event.Payload)
	if json.Unmarshal(raw, &terminal) != nil {
		return
	}
	if strings.TrimSpace(terminal.RunID) == "" {
		return
	}
	key := event.AgentID + "\x00" + terminal.RunID
	c.mu.Lock()
	if len(c.terminal) >= 10000 {
		clear(c.terminal)
	}
	if _, exists := c.terminal[key]; exists {
		c.mu.Unlock()
		return
	}
	c.terminal[key] = struct{}{}
	c.mu.Unlock()
	if strings.TrimSpace(terminal.Model) == "" && c.resolve != nil {
		model, strategy, ok := c.resolve(event.AgentID)
		if ok {
			terminal.Model, terminal.Strategy = model, strategy
		}
	}
	if strings.TrimSpace(terminal.Model) == "" {
		return
	}
	c.wg.Add(1)
	job := strategyFitJob{terminal.Provider, terminal.Model, terminal.Strategy, terminal.RunID, terminal.Success}
	select {
	case c.jobs <- job:
	default:
		_ = c.store.RecordProviderRun(job.provider, job.model, job.strategy, job.runID, job.success)
		c.wg.Done()
	}
}

func (c *StrategyFitCollector) Wait()  { c.wg.Wait() }
func (c *StrategyFitCollector) Close() { c.Wait(); close(c.jobs) }

func degradedMessage(payload any) bool {
	msg, ok := payload.(message.Message)
	return ok && strings.EqualFold(msg.Metadata[message.MetaReasoningDegraded], "true")
}

func UnreliableStrategiesPromptBlock(model string, strategies []string) string {
	if strings.TrimSpace(model) == "" || len(strategies) == 0 {
		return ""
	}
	return "\nSTRATEGY RELIABILITY GUARDRAIL — historical local runs show that model " + model +
		" is unreliable with: " + strings.Join(strategies, ", ") +
		". Do NOT recommend or generate those strategies for this model; choose a reliable alternative.\n"
}
