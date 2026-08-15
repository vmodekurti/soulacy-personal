package gateway

import (
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/gofiber/fiber/v2"

	"github.com/soulacy/soulacy/internal/studio"
	"github.com/soulacy/soulacy/internal/wsroot"
)

// studioScope binds Studio's learning and draft state to one workspace and one
// acting subject.
//
// Studio's stores were process singletons behind a sync.Once, resolved from
// environment variables. That is correct for exactly one tenant: every
// workspace's lessons, macros, strategy observations, and drafts landed in the
// same files, so what one team taught Studio, another team's generations
// inherited. Scoping the *path* rather than filtering the *contents* means a
// tenant's learning cannot be read at all, not merely filtered out.
//
// Instances are cached per workspace because a lesson store owns a SQLite
// pool; recreating one per request would open a database file per call.
type studioScope struct {
	server      *Server
	workspaceID string
	subject     string
}

// studioStores caches one set of per-workspace stores. The zero value is
// ready: entries are created on first use.
type studioStores struct {
	mu          sync.Mutex
	lessons     map[string]*studio.LessonStore
	preferences map[string]*studio.PreferenceStore
}

// studio returns the request's scoped view of Studio state. With no verified
// identity — Personal's open development mode, or a directly constructed test
// gateway — this is the implicit personal workspace and Personal's local
// subject, which resolve to exactly the paths those deployments already use.
func (s *Server) studio(c *fiber.Ctx) studioScope {
	scope := studioScope{server: s, workspaceID: wsroot.PersonalWorkspaceID}
	if c == nil {
		return scope
	}
	if identity, ok := requestIdentity(c); ok {
		scope.workspaceID = wsroot.Normalize(identity.WorkspaceID())
		scope.subject = identity.Subject()
		return scope
	}
	// Studio predates verified workspace context and identified users by claim
	// subject. Keep honouring that so an open Personal install still separates
	// its own users' drafts the way it did before.
	scope.subject = studioLearningOwner(c)
	return scope
}

// studioForWorkspace scopes Studio state without a request, for background
// work that already knows which tenant it is acting for.
func (s *Server) studioForWorkspace(workspaceID, subject string) studioScope {
	return studioScope{server: s, workspaceID: wsroot.Normalize(workspaceID), subject: subject}
}

// WorkspaceID and Subject expose the binding for callers that must record it.
func (sc studioScope) WorkspaceID() string { return sc.workspaceID }
func (sc studioScope) Subject() string     { return sc.subject }

func (sc studioScope) enabled() bool {
	return sc.server != nil && sc.server.studioLearningEnabled()
}

// lessons returns this workspace's lesson store, creating and caching it on
// first use. Each workspace gets its own SQLite file, so a semantic search
// cannot surface another tenant's guidance however the query is phrased.
func (sc studioScope) lessons() *studio.LessonStore {
	if !sc.enabled() {
		return nil
	}
	path := wsroot.File(lessonsPath(), sc.workspaceID)
	if path == "" {
		return nil
	}
	stores := &sc.server.studioStores
	stores.mu.Lock()
	defer stores.mu.Unlock()
	if stores.lessons == nil {
		stores.lessons = map[string]*studio.LessonStore{}
	}
	if existing, ok := stores.lessons[sc.workspaceID]; ok {
		return existing
	}
	embedder := sc.server.lessonEmbedder()
	// A configured default embedding provider is authoritative. If it is
	// unavailable, fail closed instead of creating a local index with a
	// different dimension that the real provider cannot query later.
	if sc.server.requiresConfiguredEmbedder() && embedder == nil {
		stores.lessons[sc.workspaceID] = nil
		return nil
	}
	if err := ensureParentDir(path); err != nil {
		stores.lessons[sc.workspaceID] = nil
		return nil
	}
	store := studio.NewSemanticLessonStore(path, embedder)
	stores.lessons[sc.workspaceID] = store
	return store
}

// preferences returns this workspace's preference store. Preferences are
// user-private, and the store already filters by owner; scoping the file as
// well means one workspace's observations are not even readable from another.
func (sc studioScope) preferences() *studio.PreferenceStore {
	if !sc.enabled() {
		return nil
	}
	path := wsroot.File(preferencesPath(), sc.workspaceID)
	if path == "" {
		return nil
	}
	stores := &sc.server.studioStores
	stores.mu.Lock()
	defer stores.mu.Unlock()
	if stores.preferences == nil {
		stores.preferences = map[string]*studio.PreferenceStore{}
	}
	if existing, ok := stores.preferences[sc.workspaceID]; ok {
		return existing
	}
	if err := ensureParentDir(path); err != nil {
		stores.preferences[sc.workspaceID] = nil
		return nil
	}
	store := studio.NewPreferenceStore(path)
	stores.preferences[sc.workspaceID] = store
	return store
}

// macros and strategyFit are cheap file-backed stores with no pooled handle,
// so they are constructed per call exactly as they were before.
func (sc studioScope) macros() *studio.MacroStore {
	path := wsroot.File(macrosPath(), sc.workspaceID)
	if path == "" || ensureParentDir(path) != nil {
		return nil
	}
	return studio.NewMacroStore(path)
}

func (sc studioScope) strategyFit() *studio.StrategyFitStore {
	path := wsroot.File(strategyFitPath(), sc.workspaceID)
	if path == "" || ensureParentDir(path) != nil {
		return nil
	}
	return studio.NewStrategyFitStore(path)
}

// draftsDir is user-private inside the workspace: a draft is one person's
// work in progress, not the team's.
func (sc studioScope) draftsDir() (string, error) {
	base, err := sc.server.studioDraftsRoot()
	if err != nil {
		return "", err
	}
	return wsroot.UserDir(base, sc.workspaceID, sc.subject), nil
}

// rulesDir is workspace-owned: a rulebook is the team's shared standard.
func (sc studioScope) rulesDir() (string, error) {
	base, err := sc.server.studioRulesRoot()
	if err != nil {
		return "", err
	}
	return wsroot.Dir(base, sc.workspaceID), nil
}

// lessonEmbedder builds the embedding adapter shared by every workspace's
// lesson store. The provider is deployment configuration, not tenant data.
func (s *Server) lessonEmbedder() studio.LessonEmbedder {
	if s.engine == nil {
		return nil
	}
	svc := s.engine.Knowledge()
	if svc == nil || svc.Embedders == nil {
		return nil
	}
	configured := svc.Embedders.Get(s.cfg.Knowledge.EmbeddingProvider)
	if configured == nil {
		return nil
	}
	return lessonEmbedAdapter{embedder: configured, provider: s.cfg.Knowledge.EmbeddingProvider, model: s.cfg.Knowledge.EmbeddingModel}
}

func (s *Server) requiresConfiguredEmbedder() bool {
	return s != nil && s.cfg != nil && strings.TrimSpace(s.cfg.Knowledge.EmbeddingProvider) != ""
}

// ensureParentDir creates the directory a namespaced store file will live in.
// The personal path already exists; a workspace's namespace directory does not
// until the first tenant writes there.
func ensureParentDir(path string) error {
	return os.MkdirAll(filepath.Dir(path), 0o700)
}
