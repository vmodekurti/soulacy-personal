// Package rbac implements Soulacy's Role-Based Access Control layer (Task #31).
//
// # Roles
//
//	admin    — full access to every resource and action
//	operator — read + write most resources; cannot delete agents, write providers/config
//	viewer   — read-only; can chat with agents but cannot mutate anything
//
// # Resources and Actions
//
// Each API route is tagged with a (resource, action) pair. The RBAC middleware
// calls Manager.Require(resource, action) which:
//  1. Reads the role from auth.Claims stored by the auth middleware.
//  2. Checks the static default policy.
//  3. For agent-specific routes, optionally checks a per-agent grant from the Store.
//
// # Per-Agent Grants
//
// Operators can be restricted from specific agents (or granted extra access to
// agents they'd otherwise be blocked from). Grants are stored as rows in
// rbac_agent_grants with role, agent_id (or "*" for all), and a comma-separated
// list of allowed actions.
//
// Config (no new config keys required — RBAC is always on when auth is active).
package rbac

// ---------------------------------------------------------------------------
// Roles
// ---------------------------------------------------------------------------

const (
	RoleAdmin    = "admin"
	RoleOperator = "operator"
	RoleViewer   = "viewer"
)

// KnownRoles lists every role the system recognises.
var KnownRoles = []string{RoleAdmin, RoleOperator, RoleViewer}

// IsKnownRole returns true if role is one of the three system roles.
func IsKnownRole(role string) bool {
	for _, r := range KnownRoles {
		if r == role {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Resources
// ---------------------------------------------------------------------------

const (
	ResourceAgents      = "agents"
	ResourceChat        = "chat"
	ResourceMemory      = "memory"
	ResourceChannels    = "channels"
	ResourceProviders   = "providers"
	ResourceSkills      = "skills"
	ResourceMCP         = "mcp"
	ResourceKnowledge   = "knowledge"
	ResourceBuilder     = "builder"
	ResourceTemplates   = "templates"
	ResourceConfig      = "config"
	ResourceLogs        = "logs"
	ResourceMetrics     = "metrics"
	ResourceSchedule    = "schedule"
	ResourceRBAC        = "rbac"
	ResourceSecrets     = "secrets"
	ResourceCredentials = "credentials"
)

// ---------------------------------------------------------------------------
// Actions
// ---------------------------------------------------------------------------

const (
	ActionRead   = "read"
	ActionWrite  = "write"
	ActionDelete = "delete"
	ActionChat   = "chat"   // send a message / confirm a tool
	ActionEnable = "enable" // enable or disable agents/channels
	ActionList   = "list"
	ActionSet    = "set"
	ActionRotate = "rotate"
	ActionReveal = "reveal"
)

// ---------------------------------------------------------------------------
// Default policy
// ---------------------------------------------------------------------------
// defaultPolicy[role][resource][action] = allowed
// This is the static fallback used when no per-agent grant row exists.

var defaultPolicy = map[string]map[string]map[string]bool{
	RoleAdmin: {
		ResourceAgents:    {ActionRead: true, ActionWrite: true, ActionDelete: true, ActionEnable: true},
		ResourceChat:      {ActionRead: true, ActionChat: true},
		ResourceMemory:    {ActionRead: true, ActionDelete: true},
		ResourceChannels:  {ActionRead: true, ActionWrite: true, ActionEnable: true},
		ResourceProviders: {ActionRead: true, ActionWrite: true},
		// Installing, rescanning, and provisioning skills are administrator
		// mutations.  The routes already require skills:write; keep that
		// permission in the default admin policy so the primary Personal API key
		// can actually use the Skills UI.
		ResourceSkills:      {ActionRead: true, ActionWrite: true},
		ResourceMCP:         {ActionRead: true, ActionWrite: true, ActionDelete: true},
		ResourceKnowledge:   {ActionRead: true, ActionWrite: true, ActionDelete: true},
		ResourceBuilder:     {ActionWrite: true},
		ResourceTemplates:   {ActionRead: true, ActionWrite: true},
		ResourceConfig:      {ActionRead: true, ActionWrite: true},
		ResourceLogs:        {ActionRead: true},
		ResourceMetrics:     {ActionRead: true},
		ResourceSchedule:    {ActionRead: true, ActionWrite: true},
		ResourceRBAC:        {ActionRead: true, ActionWrite: true, ActionDelete: true},
		ResourceSecrets:     {ActionList: true, ActionSet: true, ActionDelete: true},
		ResourceCredentials: {ActionList: true, ActionSet: true, ActionDelete: true, ActionRotate: true, ActionReveal: true},
	},
	RoleOperator: {
		ResourceAgents:      {ActionRead: true, ActionWrite: true, ActionEnable: true},
		ResourceChat:        {ActionRead: true, ActionChat: true},
		ResourceMemory:      {ActionRead: true, ActionDelete: true},
		ResourceChannels:    {ActionRead: true, ActionEnable: true},
		ResourceProviders:   {ActionRead: true},
		ResourceSkills:      {ActionRead: true},
		ResourceMCP:         {ActionRead: true, ActionWrite: true},
		ResourceKnowledge:   {ActionRead: true, ActionWrite: true},
		ResourceBuilder:     {ActionWrite: true},
		ResourceTemplates:   {ActionRead: true, ActionWrite: true},
		ResourceConfig:      {ActionRead: true},
		ResourceLogs:        {ActionRead: true},
		ResourceMetrics:     {},
		ResourceSchedule:    {ActionRead: true, ActionWrite: true},
		ResourceRBAC:        {},
		ResourceSecrets:     {},
		ResourceCredentials: {ActionList: true, ActionSet: true, ActionDelete: true, ActionRotate: true},
	},
	RoleViewer: {
		ResourceAgents:      {ActionRead: true},
		ResourceChat:        {ActionRead: true, ActionChat: true},
		ResourceMemory:      {ActionRead: true},
		ResourceChannels:    {ActionRead: true},
		ResourceProviders:   {ActionRead: true},
		ResourceSkills:      {ActionRead: true},
		ResourceMCP:         {ActionRead: true},
		ResourceKnowledge:   {ActionRead: true},
		ResourceBuilder:     {},
		ResourceTemplates:   {ActionRead: true},
		ResourceConfig:      {},
		ResourceLogs:        {ActionRead: true},
		ResourceMetrics:     {},
		ResourceSchedule:    {ActionRead: true},
		ResourceRBAC:        {},
		ResourceSecrets:     {},
		ResourceCredentials: {},
	},
}

// HasPermission returns true if role is allowed to perform action on resource,
// according to the static default policy. Unknown roles are denied.
func HasPermission(role, resource, action string) bool {
	resMap, ok := defaultPolicy[role]
	if !ok {
		return false
	}
	actMap, ok := resMap[resource]
	if !ok {
		return false
	}
	return actMap[action]
}

// ---------------------------------------------------------------------------
// Per-agent grant types
// ---------------------------------------------------------------------------

// AgentGrant records role-specific access to a single agent (or all agents
// when AgentID == "*").
type AgentGrant struct {
	Role    string   `json:"role"`
	AgentID string   `json:"agent_id"` // "*" = all agents
	Actions []string `json:"actions"`  // subset of ActionRead, ActionChat, ActionEnable, ActionWrite, ActionDelete
}
