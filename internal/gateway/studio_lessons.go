package gateway

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/llm"
	"github.com/soulacy/soulacy/internal/runtime"
	"github.com/soulacy/soulacy/internal/studio"
)

type preferenceMineJob struct {
	owner, agentID string
	initial, final studio.Draft
}

// studio_lessons.go wires the Studio learning loop into the gateway: it locates
// the lessons file, records a lesson when a user ACCEPTS a repair, and grounds
// the generation catalog with the lessons relevant to the tools in play. Gated
// by cfg.LLM.Studio.Learning (default on).

// studioLearningEnabled reports whether Studio should record + inject lessons.
// nil (unset) means enabled; operators opt out with `llm.studio.learning: false`.
func (s *Server) studioLearningEnabled() bool {
	l := s.cfg.LLM.Studio.Learning
	return l == nil || *l
}

// lessonsPath resolves where learned lessons are stored, mirroring the studio
// run-trace resolution: explicit env → workspace → ~/.soulacy. Returns "" when
// no home/workspace is resolvable (learning then silently no-ops).
func lessonsPath() string {
	if p := os.Getenv("SOULACY_STUDIO_LESSONS"); p != "" {
		return p
	}
	ws := os.Getenv("SOULACY_WORKSPACE")
	if ws == "" {
		if home, err := os.UserHomeDir(); err == nil {
			ws = filepath.Join(home, ".soulacy")
		}
	}
	if ws == "" {
		return ""
	}
	dbPath := filepath.Join(ws, "studio-lessons.db")
	legacyPath := filepath.Join(ws, "studio-lessons.json")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		if _, legacyErr := os.Stat(legacyPath); legacyErr == nil {
			// Move the legacy array into the new path. LessonStore detects JSON,
			// preserves it as .legacy.json, and imports every row into sqlite-vec.
			_ = os.Rename(legacyPath, dbPath)
		}
	}
	return dbPath
}

// lessonStore returns the cached sqlite-vec lesson store, or nil when learning
// is disabled/no path is resolvable. Caching keeps one SQLite pool per process.
func (s *Server) lessonStore() *studio.LessonStore {
	if !s.studioLearningEnabled() {
		return nil
	}
	s.lessonStoreOnce.Do(func() {
		p := lessonsPath()
		if p == "" {
			return
		}
		var embedder studio.LessonEmbedder
		if s.engine != nil {
			if svc := s.engine.Knowledge(); svc != nil && svc.Embedders != nil {
				if configured := svc.Embedders.Get(s.cfg.Knowledge.EmbeddingProvider); configured != nil {
					embedder = lessonEmbedAdapter{embedder: configured, provider: s.cfg.Knowledge.EmbeddingProvider, model: s.cfg.Knowledge.EmbeddingModel}
				}
			}
		}
		// A configured default embedding provider is authoritative. If it is
		// unavailable, fail closed instead of creating a local index with a
		// different dimension that the real provider cannot query later.
		if strings.TrimSpace(s.cfg.Knowledge.EmbeddingProvider) != "" && embedder == nil {
			return
		}
		s.lessonStoreCached = studio.NewSemanticLessonStore(p, embedder)
	})
	return s.lessonStoreCached
}

type lessonEmbedAdapter struct {
	embedder llm.Embedder
	provider string
	model    string
}

func (a lessonEmbedAdapter) Identity() string { return "provider:" + a.provider + "/" + a.model }

func (a lessonEmbedAdapter) Embed(ctx context.Context, text string) ([]float32, error) {
	vectors, err := a.embedder.Embed(ctx, a.model, []string{text})
	if err != nil {
		return nil, err
	}
	if len(vectors) == 0 {
		return nil, nil
	}
	return vectors[0], nil
}

func macrosPath() string {
	if p := os.Getenv("SOULACY_STUDIO_MACROS"); p != "" {
		return p
	}
	ws := os.Getenv("SOULACY_WORKSPACE")
	if ws == "" {
		if home, err := os.UserHomeDir(); err == nil {
			ws = filepath.Join(home, ".soulacy")
		}
	}
	if ws == "" {
		return ""
	}
	return filepath.Join(ws, "studio-macros.json")
}

func (s *Server) macroStore() *studio.MacroStore {
	path := macrosPath()
	if path == "" {
		return nil
	}
	return studio.NewMacroStore(path)
}

func (s *Server) groundWorkflowPatterns(cat *studio.Catalog, intent string) {
	if !s.studioLearningEnabled() {
		return
	}
	if store := s.macroStore(); store != nil {
		cat.WorkflowPatterns = store.Similar(intent, 3)
	}
}

func strategyFitPath() string {
	if p := os.Getenv("SOULACY_STUDIO_STRATEGY_FIT"); p != "" {
		return p
	}
	ws := os.Getenv("SOULACY_WORKSPACE")
	if ws == "" {
		if home, err := os.UserHomeDir(); err == nil {
			ws = filepath.Join(home, ".soulacy")
		}
	}
	if ws == "" {
		return ""
	}
	return filepath.Join(ws, "studio-strategy-fit.json")
}

func (s *Server) strategyFitStore() *studio.StrategyFitStore {
	path := strategyFitPath()
	if path == "" {
		return nil
	}
	return studio.NewStrategyFitStore(path)
}

// resolveAgentStrategy backs the strategy-fit collector, which observes the
// process-wide event stream. Those events carry an agent ID but no workspace,
// and two workspaces may use the same ID, so this resolves only within the
// personal workspace and reports "unknown" otherwise. Guessing across tenants
// would attribute one workspace's model choice to another's runs.
//
// Carrying workspace on runtime events is tracked with the rest of the Studio
// isolation work; until then this fails closed rather than silently mixing.
func (s *Server) resolveAgentStrategy(agentID string) (model, strategy string, ok bool) {
	if s.loader == nil {
		return "", "", false
	}
	def := s.loader.GetInWorkspace(runtime.PersonalWorkspaceID, agentID)
	if def == nil {
		return "", "", false
	}
	model = strings.TrimSpace(def.LLM.Model)
	if model == "" {
		_, model = s.defaultAgentLLM()
	}
	return model, strings.TrimSpace(def.Reasoning.Strategy), model != ""
}

func (s *Server) groundStrategyFit(cat *studio.Catalog) {
	provider, model := s.defaultAgentLLM()
	if strings.TrimSpace(cat.ActiveProvider) != "" {
		provider = cat.ActiveProvider
	}
	if strings.TrimSpace(cat.ActiveModel) != "" {
		model = cat.ActiveModel
	}
	cat.ActiveProvider = provider
	cat.ActiveModel = model
	if store := s.strategyFitStore(); store != nil {
		cat.UnreliableStrategies = store.UnreliableProvider(provider, model)
	}
}

func (s *Server) unreliableStrategy(provider, model, strategy string) bool {
	strategy = strings.ToLower(strings.TrimSpace(strategy))
	if strategy == "" {
		strategy = "auto"
	}
	store := s.strategyFitStore()
	if store == nil {
		return false
	}
	for _, candidate := range store.UnreliableProvider(provider, model) {
		if strings.EqualFold(candidate, strategy) {
			return true
		}
	}
	return false
}

func preferencesPath() string {
	if p := os.Getenv("SOULACY_STUDIO_PREFERENCES"); p != "" {
		return p
	}
	ws := os.Getenv("SOULACY_WORKSPACE")
	if ws == "" {
		if home, err := os.UserHomeDir(); err == nil {
			ws = filepath.Join(home, ".soulacy")
		}
	}
	if ws == "" {
		return ""
	}
	return filepath.Join(ws, "studio-preferences.json")
}

func (s *Server) preferenceStore() *studio.PreferenceStore {
	if !s.studioLearningEnabled() {
		return nil
	}
	s.preferenceStoreOnce.Do(func() {
		if path := preferencesPath(); path != "" {
			s.preferenceStoreCached = studio.NewPreferenceStore(path)
		}
	})
	return s.preferenceStoreCached
}

func (s *Server) groundPreferencesFor(cat *studio.Catalog, owner string) {
	if store := s.preferenceStore(); store != nil {
		cat.GlobalPreferences = store.RulesFor(owner)
	}
}

func (s *Server) minePreferences(owner, agentID string, initial, final studio.Draft) {
	if s.preferenceStore() == nil {
		return
	}
	job := preferenceMineJob{owner: owner, agentID: agentID, initial: initial, final: final}
	s.preferenceJobsWG.Add(1)
	select {
	case s.preferenceJobs <- job:
	default:
		s.processPreferenceJob(job)
		s.preferenceJobsWG.Done()
	}
}

func (s *Server) runPreferenceMiner() {
	for job := range s.preferenceJobs {
		s.processPreferenceJob(job)
		s.preferenceJobsWG.Done()
	}
}

func (s *Server) processPreferenceJob(job preferenceMineJob) {
	ctx, cancel := context.WithTimeout(context.Background(), s.studioLearningTimeout())
	defer cancel()
	_ = studio.NewPreferenceMiner(s.preferenceStore(), s.studioLLM()).MineFor(ctx, job.owner, job.agentID, job.initial, job.final)
}

func (s *Server) studioLearningTimeout() time.Duration {
	if s != nil && s.cfg != nil {
		if duration, err := time.ParseDuration(s.cfg.Runtime.Timeouts.LLM); err == nil && duration > 0 {
			return duration
		}
	}
	return config.DefaultTimeoutHierarchy().LLM
}

// recordLessonFromRepair distills an accepted repair into a durable lesson so
// future generations avoid the same shape mistake. Best-effort: any failure
// (learning off, no path, write error) is swallowed — learning must never break
// the apply flow.
func (s *Server) recordLessonFromRepair(wf studio.Draft, p studio.RepairProposal) {
	store := s.lessonStore()
	if store == nil {
		return
	}
	tool := ""
	for _, n := range wf.Flow.Nodes {
		if n.ID == p.NodeID {
			tool = n.Tool
			break
		}
	}
	if l, ok := studio.LessonFromProposal(p, tool, wf.Intent); ok {
		ctx, cancel := context.WithTimeout(context.Background(), s.studioLearningTimeout())
		defer cancel()
		_ = store.AddContext(ctx, l)
	}
}

// corpusPath resolves where accepted-repair regression cases are stored, mirroring
// the lessons/runs path resolution. "" disables corpus capture.
func corpusPath() string {
	if p := os.Getenv("SOULACY_STUDIO_CORPUS"); p != "" {
		return p
	}
	ws := os.Getenv("SOULACY_WORKSPACE")
	if ws == "" {
		if home, err := os.UserHomeDir(); err == nil {
			ws = filepath.Join(home, ".soulacy")
		}
	}
	if ws == "" {
		return ""
	}
	return filepath.Join(ws, "studio-corpus.json")
}

// recordCorpusCase persists the FIXED draft as a recoverable generation-eval case
// so a future normalizer regression that breaks this now-correct shape is caught
// by `sy eval generation --corpus`. Best-effort.
func (s *Server) recordCorpusCase(fixed studio.Draft, nodeID string) {
	if !s.studioLearningEnabled() {
		return
	}
	p := corpusPath()
	if p == "" {
		return
	}
	raw, err := json.Marshal(fixed)
	if err != nil {
		return
	}
	name := strings.TrimSpace(fixed.Name)
	if name == "" {
		name = "workflow"
	}
	_ = studio.AppendGenSample(p, studio.GenSample{
		Name:        "repair:" + name + ":" + nodeID,
		Raw:         string(raw),
		Recoverable: true,
	}, 200)
}

// groundLessons populates the catalog with lessons relevant to the tools it can
// use (builtin tools + connected MCP tools), so BuildPrompt can inject them.
func (s *Server) groundLessons(cat *studio.Catalog, intent string) {
	store := s.lessonStore()
	if store == nil {
		return
	}
	var query strings.Builder
	// Intent is the dominant semantic signal. Adding every installed capability
	// makes large workspaces retrieve whatever resembles the catalogue rather
	// than what the user asked. Include only descriptions lexically related to
	// the request; embedding similarity then bridges names such as GitHub/GitLab.
	query.WriteString(intent)
	query.WriteByte(' ')
	query.WriteString(intent)
	// Builtin names are the only descriptions available for that catalogue
	// surface. They are also the exact selected-capability signal used by repair
	// lessons (for example web_search), so retain them alongside intent.
	query.WriteByte(' ')
	query.WriteString(strings.Join(cat.Tools, " "))
	for _, skill := range cat.Skills {
		if studioDescriptionRelevant(intent, skill.Name+" "+skill.Description) {
			query.WriteByte(' ')
			query.WriteString(skill.Name)
			query.WriteByte(' ')
			query.WriteString(skill.Description)
		}
	}
	for _, srv := range cat.MCP {
		for _, t := range srv.Tools {
			if studioDescriptionRelevant(intent, t.Name+" "+t.Description) {
				query.WriteByte(' ')
				query.WriteString(t.Name)
				query.WriteByte(' ')
				query.WriteString(t.Description)
			}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.studioLearningTimeout())
	defer cancel()
	cat.Lessons = store.Semantic(ctx, query.String(), 5, studio.LessonSimilarityThreshold)
}

func studioDescriptionRelevant(intent, description string) bool {
	intent = strings.ToLower(intent)
	description = strings.ToLower(description)
	for _, word := range strings.FieldsFunc(description, func(r rune) bool { return r < 'a' || r > 'z' }) {
		if len(word) >= 5 && strings.Contains(intent, word) {
			return true
		}
	}
	return false
}
