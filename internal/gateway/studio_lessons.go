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
	"github.com/soulacy/soulacy/internal/studio"
	"github.com/soulacy/soulacy/internal/wsroot"
)

type preferenceMineJob struct {
	// scope travels with the job: mining runs on a worker goroutine after the
	// request is gone, and writing one workspace's inferred preferences into
	// another's file would be silent and permanent.
	scope          studioScope
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
	l := s.config().LLM.Studio.Learning
	return l == nil || *l
}

// lessonsPath resolves where learned lessons are stored, mirroring the studio
// run-trace resolution: explicit env → workspace → ~/.soulacy. Returns "" when
// no home/workspace is resolvable (learning then silently no-ops).
func lessonsPath() string {
	dbPath := lessonsBasePath()
	if dbPath == "" {
		return ""
	}
	legacyPath := strings.TrimSuffix(dbPath, filepath.Ext(dbPath)) + ".json"
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		if _, legacyErr := os.Stat(legacyPath); legacyErr == nil {
			// Move the legacy array into the new path. LessonStore detects JSON,
			// preserves it as .legacy.json, and imports every row into sqlite-vec.
			_ = os.Rename(legacyPath, dbPath)
		}
	}
	return dbPath
}

// lessonsBasePath resolves the configured lesson database without migrating
// anything. Destructive lifecycle operations use this pure form: deleting one
// named workspace must never rename files in the Personal workspace as a side
// effect of merely calculating the tenant path.
func lessonsBasePath() string {
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
	return filepath.Join(ws, "studio-lessons.db")
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

func (s *Server) groundWorkflowPatterns(scope studioScope, cat *studio.Catalog, intent string) {
	if !s.studioLearningEnabled() {
		return
	}
	if store := scope.macros(); store != nil {
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

// resolveAgentStrategy backs the strategy-fit collector, which observes the
// process-wide event stream. Two workspaces may run agents with the same ID,
// so the lookup is scoped to the workspace the observed event declared;
// resolving by ID alone would read another tenant's agent definition and
// attribute its model choice to this run.
func (s *Server) resolveAgentStrategy(workspaceID, agentID string) (model, strategy string, ok bool) {
	if s.loader == nil {
		return "", "", false
	}
	def := s.loader.GetInWorkspace(wsroot.Normalize(workspaceID), agentID)
	if def == nil {
		return "", "", false
	}
	model = strings.TrimSpace(def.LLM.Model)
	if model == "" {
		_, model = s.defaultAgentLLM()
	}
	return model, strings.TrimSpace(def.Reasoning.Strategy), model != ""
}

func (s *Server) groundStrategyFit(scope studioScope, cat *studio.Catalog) {
	provider, model := s.defaultAgentLLM()
	if strings.TrimSpace(cat.ActiveProvider) != "" {
		provider = cat.ActiveProvider
	}
	if strings.TrimSpace(cat.ActiveModel) != "" {
		model = cat.ActiveModel
	}
	cat.ActiveProvider = provider
	cat.ActiveModel = model
	if store := scope.strategyFit(); store != nil {
		cat.UnreliableStrategies = store.UnreliableProvider(provider, model)
	}
}

func (s *Server) unreliableStrategy(scope studioScope, provider, model, strategy string) bool {
	strategy = strings.ToLower(strings.TrimSpace(strategy))
	if strategy == "" {
		strategy = "auto"
	}
	store := scope.strategyFit()
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

func (s *Server) groundPreferencesFor(scope studioScope, cat *studio.Catalog, owner string) {
	if store := scope.preferences(); store != nil {
		cat.GlobalPreferences = store.RulesFor(owner)
	}
}

func (s *Server) minePreferences(scope studioScope, owner, agentID string, initial, final studio.Draft) {
	if scope.preferences() == nil {
		return
	}
	job := preferenceMineJob{scope: scope, owner: owner, agentID: agentID, initial: initial, final: final}
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
	store := job.scope.preferences()
	if store == nil {
		return
	}
	// MU-025 criterion 5. This runs on a worker goroutine after the request
	// that queued it has returned, so the workspace's status is re-read HERE
	// rather than inherited from the moment the save happened. A save made a
	// second before an owner requested deletion would otherwise mine a
	// preference into that workspace minutes later — which is not a leak (the
	// data lands in the right tenant) and is exactly why it went unnoticed.
	// What it defeats is the recovery window: an owner who cancels on day six
	// should get back the workspace they had on day one.
	admission, cancelAdmission := backgroundAdmissionContext(context.Background())
	admitted := s.backgroundWriteAdmitted(admission, job.scope.workspaceID)
	cancelAdmission()
	if !admitted {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.studioLearningTimeout())
	defer cancel()
	_ = studio.NewPreferenceMiner(store, s.studioLLM()).MineFor(ctx, job.owner, job.agentID, job.initial, job.final)
}

func (s *Server) studioLearningTimeout() time.Duration {
	if s != nil && s.config() != nil {
		if duration, err := time.ParseDuration(s.config().Runtime.Timeouts.LLM); err == nil && duration > 0 {
			return duration
		}
	}
	return config.DefaultTimeoutHierarchy().LLM
}

// recordLessonFromRepair distills an accepted repair into a durable lesson so
// future generations avoid the same shape mistake. Best-effort: any failure
// (learning off, no path, write error) is swallowed — learning must never break
// the apply flow.
func (s *Server) recordLessonFromRepair(scope studioScope, wf studio.Draft, p studio.RepairProposal) {
	store := scope.lessons()
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
func (s *Server) groundLessons(scope studioScope, cat *studio.Catalog, intent string) {
	store := scope.lessons()
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
