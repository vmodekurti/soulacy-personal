package gateway

import (
	"github.com/gofiber/fiber/v2"

	"github.com/soulacy/soulacy/internal/runtime"
	"github.com/soulacy/soulacy/pkg/agent"
)

// agentScope binds the agent loader to one verified workspace and one acting
// principal for the duration of a request.
//
// It exists because the alternative — threading a workspace string through
// every one of the gateway's agent calls — is a rule that has to be remembered
// at each site, and the sites that forget it are exactly the ones that leak.
// A handler cannot reach an agent without first naming a workspace, because
// the only way to get here is through a request whose workspace the server
// already verified.
//
// The method set deliberately mirrors the loader's unscoped API, so a call
// reads the same as it did before and the scoping is not something a reviewer
// has to hold in their head.
type agentScope struct {
	loader      *runtime.Loader
	workspaceID string
	actor       string
}

// agents returns the request's scoped view of the agent registry.
//
// When no verified identity is present — a directly constructed test gateway,
// or Personal's explicit open development mode — this resolves to the implicit
// personal workspace, which is the same set of agents those deployments have
// always seen.
func (s *Server) agents(c *fiber.Ctx) agentScope {
	scope := agentScope{loader: s.loader, workspaceID: runtime.PersonalWorkspaceID}
	if c == nil {
		return scope
	}
	if identity, ok := requestIdentity(c); ok {
		scope.workspaceID = runtime.NormalizeWorkspace(identity.WorkspaceID())
		scope.actor = identity.Subject()
	}
	return scope
}

// crossWorkspace marks a scope that spans every tenant. It is not a workspace
// ID and can never match one, because ValidateWorkspaceID rejects the empty
// string and every ID with a space in it.
const crossWorkspace = "* all workspaces *"

// agentsAcrossWorkspaces returns a scope covering the whole deployment.
//
// It exists for readiness counters, boot validation, and support bundles,
// where the question is what this installation contains rather than what a
// tenant may see. Listing spans tenants; resolving a single agent does not,
// so this can never be used to reach one workspace's agent from another. Every
// use is a deliberate, greppable decision.
func (s *Server) agentsAcrossWorkspaces() agentScope {
	return agentScope{loader: s.loader, workspaceID: crossWorkspace, actor: "system"}
}

// agentsForWorkspace scopes the registry without a request. Background work —
// boot validation, schedulers, replay — uses this with a workspace it obtained
// from the loader itself, never from user input.
func (s *Server) agentsForWorkspace(workspaceID string) agentScope {
	return agentScope{loader: s.loader, workspaceID: runtime.NormalizeWorkspace(workspaceID), actor: "system"}
}

// eachWorkspace runs fn once per workspace that owns agents. Deployment-wide
// sweeps must cover every tenant, but they must still touch one tenant at a
// time rather than reading a flattened registry.
func (s *Server) eachWorkspace(fn func(agentScope)) {
	if s == nil || s.loader == nil {
		return
	}
	for _, workspaceID := range s.loader.AllWorkspaces() {
		fn(s.agentsForWorkspace(workspaceID))
	}
}

func (a agentScope) Get(id string) *agent.Definition {
	// A cross-workspace scope deliberately cannot resolve a single agent. An
	// aggregate may count what a deployment contains; nothing may look one
	// tenant's agent up without naming that tenant.
	if a.loader == nil || a.workspaceID == crossWorkspace {
		return nil
	}
	return a.loader.GetInWorkspace(a.workspaceID, id)
}

func (a agentScope) All() []*agent.Definition {
	if a.loader == nil {
		return nil
	}
	if a.workspaceID == crossWorkspace {
		return a.loader.AllAcrossWorkspaces()
	}
	return a.loader.AllInWorkspace(a.workspaceID)
}

func (a agentScope) Upsert(dir string, def *agent.Definition) error {
	return a.loader.UpsertInWorkspace(a.workspaceID, dir, def, a.actor)
}

func (a agentScope) Delete(id string) error {
	return a.loader.DeleteInWorkspace(a.workspaceID, id, a.actor)
}

func (a agentScope) Register(def *agent.Definition) {
	a.loader.RegisterInWorkspace(a.workspaceID, def)
}

func (a agentScope) Unregister(id string) {
	a.loader.UnregisterInWorkspace(a.workspaceID, id)
}

func (a agentScope) SetEnabledInMemory(id string, enabled bool) bool {
	return a.loader.SetEnabledInMemoryInWorkspace(a.workspaceID, id, enabled)
}

func (a agentScope) IsBuiltin(id string) bool {
	if a.loader == nil {
		return false
	}
	return a.loader.IsBuiltinInWorkspace(a.workspaceID, id)
}

func (a agentScope) AgentVersions(id string) ([]runtime.AgentVersion, error) {
	return a.loader.AgentVersionsInWorkspace(a.workspaceID, id)
}

func (a agentScope) ReadAgentVersion(id, version string) ([]byte, runtime.AgentVersion, error) {
	return a.loader.ReadAgentVersionInWorkspace(a.workspaceID, id, version)
}

func (a agentScope) RestoreAgentVersion(dir, id, version string) (*agent.Definition, runtime.AgentVersion, error) {
	return a.loader.RestoreAgentVersionInWorkspace(a.workspaceID, dir, id, version, a.actor)
}

// WorkspaceID exposes the bound workspace for callers that must record or
// namespace something alongside the agent.
func (a agentScope) WorkspaceID() string { return a.workspaceID }
