// Package rbac implements Soulacy's Role-Based Access Control layer (Task #31).
//
// # Roles
//
//	owner     — workspace ownership, policy, and full workspace access
//	admin     — full operational access without ownership transfer authority
//	developer — build and maintain agents and their dependencies
//	operator  — run and operate agents; cannot administer the workspace
//	viewer    — read-only; can chat with agents but cannot mutate anything
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

import "sort"

// ---------------------------------------------------------------------------
// Roles
// ---------------------------------------------------------------------------

const (
	RoleOwner     = "owner"
	RoleAdmin     = "admin"
	RoleDeveloper = "developer"
	RoleOperator  = "operator"
	RoleViewer    = "viewer"
)

// KnownRoles lists every role the system recognises.
var KnownRoles = []string{RoleOwner, RoleAdmin, RoleDeveloper, RoleOperator, RoleViewer}

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
	ResourcePlugins     = "plugins"
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

	// ResourceApprovals is the authority to see and decide paused tool calls
	// (MU-022).
	//
	// A separate resource rather than a facet of ResourceChat, because
	// approving is not a stronger form of chatting. The arguments of a paused
	// call are, by construction, the details of something that was stopped for
	// being dangerous, and releasing one authorizes an action the requester
	// could not take alone. A viewer who may chat has not thereby been
	// trusted to release a privileged shell command somebody else's agent
	// composed — which is exactly what "confirm a tool" under ActionChat
	// meant before this.
	ResourceApprovals = "approvals"
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

	// ActionInstall is the authority to bring third-party code into a
	// workspace: a skill from a registry, an MCP server from a marketplace, a
	// plugin from a URL.
	//
	// MU-017 criterion 3 asks for installation permission to be separate from
	// extension-use permission, and the reason is that they are not the same
	// risk at all. Using an extension runs code somebody already vetted.
	// Installing one *chooses* whose code runs, and a developer who may author
	// a local skill has not thereby been trusted to pull an arbitrary package
	// off the internet into everyone else's runtime. Before this, both were
	// ActionWrite.
	ActionInstall = "install"
)

// ---------------------------------------------------------------------------
// Default policy
// ---------------------------------------------------------------------------
// defaultPolicy[role][resource][action] = allowed
// This is the static fallback used when no per-agent grant row exists.

var defaultPolicy = map[string]map[string]map[string]bool{
	RoleOwner: {
		ResourceAgents:      {ActionRead: true, ActionWrite: true, ActionDelete: true, ActionEnable: true},
		ResourceChat:        {ActionRead: true, ActionWrite: true, ActionChat: true},
		ResourceApprovals:   {ActionRead: true, ActionWrite: true},
		ResourceMemory:      {ActionRead: true, ActionWrite: true, ActionDelete: true},
		ResourceChannels:    {ActionRead: true, ActionWrite: true, ActionEnable: true},
		ResourceProviders:   {ActionRead: true, ActionWrite: true},
		ResourceSkills:      {ActionRead: true, ActionWrite: true, ActionDelete: true, ActionInstall: true},
		ResourceMCP:         {ActionRead: true, ActionWrite: true, ActionDelete: true, ActionInstall: true},
		ResourcePlugins:     {ActionRead: true, ActionWrite: true, ActionDelete: true, ActionInstall: true},
		ResourceKnowledge:   {ActionRead: true, ActionWrite: true, ActionDelete: true},
		ResourceBuilder:     {ActionRead: true, ActionWrite: true},
		ResourceTemplates:   {ActionRead: true, ActionWrite: true, ActionDelete: true},
		ResourceConfig:      {ActionRead: true, ActionWrite: true},
		ResourceLogs:        {ActionRead: true},
		ResourceMetrics:     {ActionRead: true, ActionWrite: true},
		ResourceSchedule:    {ActionRead: true, ActionWrite: true, ActionDelete: true, ActionEnable: true},
		ResourceRBAC:        {ActionRead: true, ActionWrite: true, ActionDelete: true},
		ResourceSecrets:     {ActionList: true, ActionSet: true, ActionDelete: true},
		ResourceCredentials: {ActionList: true, ActionSet: true, ActionDelete: true, ActionRotate: true, ActionReveal: true},
	},
	RoleAdmin: {
		ResourceAgents:      {ActionRead: true, ActionWrite: true, ActionDelete: true, ActionEnable: true},
		ResourceChat:        {ActionRead: true, ActionChat: true},
		ResourceApprovals:   {ActionRead: true, ActionWrite: true},
		ResourceMemory:      {ActionRead: true, ActionDelete: true},
		ResourceChannels:    {ActionRead: true, ActionWrite: true, ActionEnable: true},
		ResourceProviders:   {ActionRead: true, ActionWrite: true},
		ResourceSkills:      {ActionRead: true, ActionInstall: true},
		ResourceMCP:         {ActionRead: true, ActionWrite: true, ActionDelete: true, ActionInstall: true},
		ResourcePlugins:     {ActionRead: true, ActionWrite: true, ActionDelete: true, ActionInstall: true},
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
	RoleDeveloper: {
		ResourceAgents:      {ActionRead: true, ActionWrite: true, ActionDelete: true, ActionEnable: true},
		ResourceChat:        {ActionRead: true, ActionWrite: true, ActionChat: true},
		ResourceApprovals:   {ActionRead: true},
		ResourceMemory:      {ActionRead: true, ActionWrite: true, ActionDelete: true},
		ResourceChannels:    {ActionRead: true},
		ResourceProviders:   {ActionRead: true},
		ResourceSkills:      {ActionRead: true, ActionWrite: true},
		ResourceMCP:         {ActionRead: true, ActionWrite: true, ActionDelete: true},
		ResourcePlugins:     {ActionRead: true},
		ResourceKnowledge:   {ActionRead: true, ActionWrite: true, ActionDelete: true},
		ResourceBuilder:     {ActionRead: true, ActionWrite: true},
		ResourceTemplates:   {ActionRead: true, ActionWrite: true},
		ResourceConfig:      {},
		ResourceLogs:        {ActionRead: true},
		ResourceMetrics:     {ActionRead: true},
		ResourceSchedule:    {ActionRead: true, ActionWrite: true},
		ResourceRBAC:        {},
		ResourceSecrets:     {},
		ResourceCredentials: {ActionList: true, ActionSet: true, ActionDelete: true, ActionRotate: true},
	},
	RoleOperator: {
		ResourceAgents:      {ActionRead: true, ActionWrite: true, ActionEnable: true},
		ResourceChat:        {ActionRead: true, ActionChat: true},
		ResourceApprovals:   {ActionRead: true, ActionWrite: true},
		ResourceMemory:      {ActionRead: true, ActionDelete: true},
		ResourceChannels:    {ActionRead: true, ActionEnable: true},
		ResourceProviders:   {ActionRead: true},
		ResourceSkills:      {ActionRead: true},
		ResourceMCP:         {ActionRead: true, ActionWrite: true},
		ResourcePlugins:     {ActionRead: true},
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
		ResourceApprovals:   {},
		ResourceMemory:      {ActionRead: true},
		ResourceChannels:    {ActionRead: true},
		ResourceProviders:   {ActionRead: true},
		ResourceSkills:      {ActionRead: true},
		ResourceMCP:         {ActionRead: true},
		ResourcePlugins:     {ActionRead: true},
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
	WorkspaceID   string   `json:"workspace_id,omitempty"`
	Role          string   `json:"role"`
	AgentID       string   `json:"agent_id"` // "*" = all agents
	Actions       []string `json:"actions"`  // subset of ActionRead, ActionChat, ActionEnable, ActionWrite, ActionDelete
	Elevated      bool     `json:"elevated,omitempty"`
	GrantedByRole string   `json:"granted_by_role,omitempty"`
}

// Resources and Actions enumerate the policy's own vocabulary, so a caller can
// project the matrix without restating it.
//
// They read the policy rather than being a hand-written list beside it: a
// resource added to defaultPolicy and forgotten here would be a permission the
// server enforces and never tells a client about, and the client would then
// hide a control the caller is in fact allowed to use.
func Resources() []string {
	seen := map[string]bool{}
	for _, resources := range defaultPolicy {
		for resource := range resources {
			seen[resource] = true
		}
	}
	out := make([]string, 0, len(seen))
	for resource := range seen {
		out = append(out, resource)
	}
	sort.Strings(out)
	return out
}

// Actions returns every action any role holds on any resource.
func Actions() []string {
	seen := map[string]bool{}
	for _, resources := range defaultPolicy {
		for _, actions := range resources {
			for action := range actions {
				seen[action] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for action := range seen {
		out = append(out, action)
	}
	sort.Strings(out)
	return out
}

// PermissionsFor projects the static policy for one role into
// resource -> allowed actions (MU-030 criterion 2).
//
// It exists so a client does not have to carry a second copy of the matrix.
// The GUI's job is to hide controls the caller cannot use; deciding that from
// a duplicated table is how two answers drift, and the drift is worst in the
// direction that looks like a server bug — a control offered for a permission
// the server refuses.
//
// The projection is ADVISORY. Every route still authorizes itself; this says
// what the caller would be allowed and grants nothing.
func PermissionsFor(role string) map[string][]string {
	out := map[string][]string{}
	for _, resource := range Resources() {
		var allowed []string
		for _, action := range Actions() {
			if HasPermission(role, resource, action) {
				allowed = append(allowed, action)
			}
		}
		if len(allowed) > 0 {
			out[resource] = allowed
		}
	}
	return out
}
