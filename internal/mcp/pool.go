package mcp

import (
	"sort"
	"sync"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/sandbox"
	"github.com/soulacy/soulacy/internal/wsroot"
)

// pool.go — MU-017 criterion 5: "MCP processes execute within workspace
// isolation."
//
// ONE PROCESS CANNOT BE ISOLATED TO TWO TENANTS. That sentence is the whole
// design. Every other confinement in this repo — filesystem roots, scratch
// directories, credential namespaces — derives from a workspace, and an MCP
// server had no workspace to derive from: the Client was built once at boot
// from config.yaml and every agent in the deployment called into the same
// subprocesses. So the confinement work in the transport (working directory,
// rlimits, environment allow-list) had nothing to confine TO. A shared
// filesystem server pointed at one directory is one directory for everybody,
// however carefully that directory is chosen.
//
// The pool gives each workspace its OWN processes, started from the same
// operator-configured templates but each rooted in that workspace's tree.
// Two tenants' `filesystem__read_file` now resolve the same relative path to
// two different files, for the same structural reason two tenants' write_file
// already did.
//
// WHY LAZY. Per-workspace processes multiply the subprocess count by the
// number of active tenants, and MCP servers are frequently `npx`-launched with
// a cold download on first start. A pool that eagerly started every server for
// every known workspace would turn adding a workspace into a boot-time cost
// for a tenant who may never call an MCP tool. Clients are created on first
// use and kept; a workspace that never touches MCP never spawns anything.
//
// WHAT THIS COSTS, STATED PLAINLY: a deployment with many active workspaces
// and many configured servers runs many subprocesses. That is the price of
// the criterion — the alternative is one process holding every tenant's
// working directory, which is the bug. Idle eviction is the obvious next
// refinement and is deliberately NOT here: evicting a client mid-call is its
// own correctness problem, and shipping a lifetime policy nobody has needed
// yet would be inventing a second revocation path beside RemoveServer.

// Confinement resolves the per-workspace confinement a spawned MCP server
// runs under. It is supplied by the caller rather than computed here because
// the authority on where a workspace's tree lives is the engine — the same
// function that answers it for read_file and for privileged subprocesses.
// Duplicating that derivation inside this package would be a second answer to
// a question that must have exactly one.
//
// An error means the workspace has no confinement, and the pool then starts
// NOTHING for it. Failing closed matters here more than usual: the fallback a
// reasonable person reaches for — "use the default directory" — is precisely
// the shared directory this file exists to eliminate.
type Confinement interface {
	WorkspaceConfinement(workspaceID string) (workDir string, limits sandbox.Limits, selfPath string, err error)
}

// ConfinementFunc adapts a plain function to Confinement.
type ConfinementFunc func(workspaceID string) (string, sandbox.Limits, string, error)

func (f ConfinementFunc) WorkspaceConfinement(workspaceID string) (string, sandbox.Limits, string, error) {
	return f(workspaceID)
}

// Pool owns one Client per workspace.
type Pool struct {
	template Config
	confine  Confinement
	log      *zap.Logger

	mu      sync.Mutex
	clients map[string]*Client
	// overrides are servers added at runtime through the API, per workspace.
	// They live here rather than only inside the live Client so that a
	// workspace's own servers survive a client being rebuilt — and so that
	// one workspace's `AddServer` cannot reach another's, which it would if
	// runtime additions were merged back into the shared template.
	overrides map[string]map[string]ServerConfig
	// removed records template servers a workspace has revoked. Without it,
	// revocation would be undone the next time that workspace's client was
	// constructed, which reads as the revoke silently failing.
	removed map[string]map[string]bool
	// creds, when set, means every template server's secrets must come from
	// the WORKSPACE rather than the operator's config — see tenantcreds.go.
	// It is set explicitly by the wiring rather than defaulting on, because
	// personal installations have one tenant whose credentials genuinely are
	// the operator's, and defaulting on would break them.
	//
	// tenantCreds is separate from creds being non-nil so that "required, and
	// the resolver is broken or absent" fails CLOSED. A nil resolver with the
	// requirement on withholds every credentialed server, which is the honest
	// reading of "we cannot tell whose credential this would be".
	tenantCreds bool
	creds       CredentialResolver
	// withheld records, per workspace, the servers not started because the
	// workspace has not supplied their credentials. Read by the API so a
	// tenant sees a setup step instead of a missing tool.
	withheld map[string][]WithheldServer
	// own resolves the servers a workspace DEFINED for itself, durably. Before
	// it, those lived only in `overrides` and were lost on the next restart —
	// a feature that appears to work until the first deploy.
	own    ServerStore
	closed bool
}

// NewPool returns a pool over the operator's configured servers.
//
// confine may be nil, in which case no workspace gets confinement and the pool
// starts nothing — the fail-closed reading of "isolation is not available".
// Callers that genuinely want the old shared, unconfined client keep using
// New directly, which is honest about what it is.
func NewPool(template Config, confine Confinement, log *zap.Logger) *Pool {
	if log == nil {
		log = zap.NewNop()
	}
	return &Pool{
		template:  template,
		confine:   confine,
		log:       log,
		clients:   make(map[string]*Client),
		overrides: make(map[string]map[string]ServerConfig),
		removed:   make(map[string]map[string]bool),
		withheld:  make(map[string][]WithheldServer),
	}
}

// For returns the workspace's client, starting it on first use.
//
// Never nil: an unresolvable workspace gets an EMPTY client rather than a nil
// one or a shared one. Empty means "this workspace has no MCP tools", which
// degrades a feature; nil would panic at a dozen call sites and the shared
// client would be the cross-tenant bug. Of the three, only one is safe to be
// wrong about.
func (p *Pool) For(workspaceID string) *Client {
	workspaceID = wsroot.Normalize(workspaceID)

	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return &Client{log: p.log}
	}
	if existing, ok := p.clients[workspaceID]; ok {
		p.mu.Unlock()
		return existing
	}
	cfg := p.configFor(workspaceID)
	p.mu.Unlock()

	// Started OUTSIDE the lock: New runs the MCP handshake against every
	// configured server, which can take tens of seconds for an npx-launched
	// one. Holding p.mu across that would serialise every other workspace's
	// first MCP call behind the slowest server of whoever asked first.
	client := New(cfg, p.log)

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		go func() { _ = client.Close() }()
		return &Client{log: p.log}
	}
	// Another caller may have raced us here. Keep theirs and discard ours, so
	// a workspace never ends up with two sets of live subprocesses — the
	// duplicate would be invisible except as double the servers in the
	// process table and double the side effects of every tool call.
	if existing, ok := p.clients[workspaceID]; ok {
		go func() { _ = client.Close() }()
		return existing
	}
	p.clients[workspaceID] = client
	return client
}

// configFor builds the effective server set for a workspace. Caller holds p.mu.
func (p *Pool) configFor(workspaceID string) Config {
	if p.confine == nil {
		return Config{}
	}
	workDir, limits, selfPath, err := p.confine.WorkspaceConfinement(workspaceID)
	if err != nil || workDir == "" {
		p.log.Warn("mcp: workspace has no confinement; starting no servers for it",
			zap.String("workspace", workspaceID), zap.Error(err))
		return Config{}
	}

	servers := make(map[string]ServerConfig, len(p.template.Servers)+len(p.overrides[workspaceID]))
	var withheld []WithheldServer
	for id, sc := range p.template.Servers {
		if p.removed[workspaceID][id] {
			continue
		}
		if p.tenantCreds {
			tenantized, missing := tenantize(id, sc, workspaceID, p.creds)
			if missing != nil {
				// Not started, and recorded. Starting it with the operator's
				// token would make this workspace act as the operator
				// upstream, which is the whole thing being prevented.
				withheld = append(withheld, *missing)
				p.log.Info("mcp: server withheld until this workspace supplies its own credentials",
					zap.String("workspace", workspaceID), zap.String("server", id),
					zap.Strings("missing", missing.Missing))
				continue
			}
			sc = tenantized
		}
		servers[id] = confined(sc, workDir, limits, selfPath)
	}
	// The workspace's OWN servers, from its durable store and from any added
	// in this process's lifetime. Neither is run through tenantize: they were
	// authored by this workspace with this workspace's values, so there is no
	// operator credential in them to replace, and demanding a workspace supply
	// credentials to itself would be circular.
	//
	// Stored first, then in-process, so a server added through the API in this
	// run wins over the row it was written from. They are normally identical;
	// when they are not it is because the write to the store failed, and the
	// tenant's most recent intent is the better answer than a stale row.
	if p.own != nil {
		for id, sc := range p.own.ServersFor(workspaceID) {
			servers[id] = confined(sc, workDir, limits, selfPath)
		}
	}
	for id, sc := range p.overrides[workspaceID] {
		servers[id] = confined(sc, workDir, limits, selfPath)
	}
	sort.Slice(withheld, func(i, j int) bool { return withheld[i].ServerID < withheld[j].ServerID })
	p.withheld[workspaceID] = withheld
	return Config{Servers: servers}
}

// ServerStore resolves the servers a workspace defined for itself.
//
// An interface rather than a concrete store because the durable one needs
// SQLite, and internal/mcp is deliberately dependency-light: it starts
// subprocesses and speaks a protocol, and a database driver has no business in
// its import graph.
type ServerStore interface {
	ServersFor(workspaceID string) map[string]ServerConfig
}

// ServerStoreFunc adapts a plain function to ServerStore.
type ServerStoreFunc func(workspaceID string) map[string]ServerConfig

func (f ServerStoreFunc) ServersFor(workspaceID string) map[string]ServerConfig {
	return f(workspaceID)
}

// SetServerStore installs the durable per-workspace server source.
func (p *Pool) SetServerStore(store ServerStore) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.own = store
}

// RequireTenantCredentials makes every template server's secrets come from the
// workspace rather than from the operator's config.
//
// Called by the wiring in multi-user mode. A boolean plus a resolver rather
// than a deployment mode, because internal/mcp has no business knowing what
// mode the process runs in — the same reason internal/runtime does not.
//
// Turning it on takes effect for clients built AFTER the call, so it is called
// during construction, before any workspace has asked for a client. There is a
// build guard on that ordering in internal/app.
func (p *Pool) RequireTenantCredentials(resolver CredentialResolver) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.tenantCreds = true
	p.creds = resolver
}

// Withheld returns the servers this workspace cannot start until it supplies
// their credentials. Empty once it has, or when the requirement is off.
func (p *Pool) Withheld(workspaceID string) []WithheldServer {
	workspaceID = wsroot.Normalize(workspaceID)
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]WithheldServer, len(p.withheld[workspaceID]))
	copy(out, p.withheld[workspaceID])
	return out
}

// InvalidateWorkspace drops a workspace's client so the next call rebuilds it.
//
// Used when the workspace supplies a credential it was missing: without it the
// tenant sets their token, sees nothing change, and concludes the feature is
// broken. The old client is closed OUTSIDE the lock for the same reason For
// builds outside it — closing runs each server's shutdown.
func (p *Pool) InvalidateWorkspace(workspaceID string) {
	workspaceID = wsroot.Normalize(workspaceID)
	p.mu.Lock()
	client := p.clients[workspaceID]
	delete(p.clients, workspaceID)
	delete(p.withheld, workspaceID)
	p.mu.Unlock()
	if client != nil {
		go func() { _ = client.Close() }()
	}
}

// confined stamps the workspace's confinement onto a server config.
//
// It OVERWRITES rather than defaults. A config that already named a WorkDir
// would otherwise keep it, and the one config most likely to name one is a
// filesystem server an operator pointed at a shared directory on purpose —
// exactly the case where the tenant boundary has to win over the operator's
// convenience. If an operator needs a genuinely shared tree they can mount one
// inside each workspace; there is no way to spell "outside every workspace"
// here, which is the point.
func confined(sc ServerConfig, workDir string, limits sandbox.Limits, selfPath string) ServerConfig {
	sc.WorkDir = workDir
	sc.Limits = limits
	sc.SelfPath = selfPath
	return sc
}

// AddServer registers a server for ONE workspace and starts it there.
func (p *Pool) AddServer(workspaceID, id string, cfg ServerConfig) error {
	workspaceID = wsroot.Normalize(workspaceID)
	p.mu.Lock()
	if p.overrides[workspaceID] == nil {
		p.overrides[workspaceID] = make(map[string]ServerConfig)
	}
	p.overrides[workspaceID][id] = cfg
	delete(p.removed[workspaceID], id)
	p.mu.Unlock()

	// For() may construct the client from the override we just recorded, in
	// which case the server is already running and AddServer would start a
	// second copy. Resolving the client first and then asking whether it has
	// the server keeps both orderings correct.
	client := p.For(workspaceID)
	for _, status := range client.ServersSnapshot() {
		if status.ID == id {
			return nil
		}
	}

	p.mu.Lock()
	workDir, limits, selfPath := "", sandbox.Limits{}, ""
	if p.confine != nil {
		workDir, limits, selfPath, _ = p.confine.WorkspaceConfinement(workspaceID)
	}
	p.mu.Unlock()
	if workDir == "" {
		return errNoConfinement
	}
	return client.AddServer(id, confined(cfg, workDir, limits, selfPath))
}

// RemoveServer revokes a server in ONE workspace.
func (p *Pool) RemoveServer(workspaceID, id string) error {
	workspaceID = wsroot.Normalize(workspaceID)
	p.mu.Lock()
	delete(p.overrides[workspaceID], id)
	if p.removed[workspaceID] == nil {
		p.removed[workspaceID] = make(map[string]bool)
	}
	// Tombstoned even if it was only an override: a workspace that adds a
	// server, revokes it, and later has its client rebuilt must not get the
	// server back. Recording the revocation is cheap; discovering it was
	// forgotten means a revoked extension is running again.
	p.removed[workspaceID][id] = true
	client := p.clients[workspaceID]
	p.mu.Unlock()

	if client == nil {
		return nil
	}
	return client.RemoveServer(id)
}

// Close shuts down every workspace's servers.
func (p *Pool) Close() error {
	p.mu.Lock()
	p.closed = true
	clients := make([]*Client, 0, len(p.clients))
	for _, client := range p.clients {
		clients = append(clients, client)
	}
	p.clients = make(map[string]*Client)
	p.mu.Unlock()

	for _, client := range clients {
		_ = client.Close()
	}
	return nil
}

type poolError string

func (e poolError) Error() string { return string(e) }

const errNoConfinement = poolError("mcp: workspace has no filesystem confinement, so no server may be started for it")

// ReplaceTemplate swaps the operator-configured server set and stops every
// workspace's clients so the next use rebuilds from it.
//
// A CONFIG RELOAD IS A DEPLOYMENT-WIDE EVENT and has to reach every tenant;
// the previous code applied it to the one shared client, which was every
// tenant by accident. Applying it by diffing each live client against the new
// template would be the same operation done N times with N chances to drift,
// and would silently resurrect servers a workspace had revoked — the removed
// tombstones are per workspace and only configFor consults them.
//
// Stopping and lazily rebuilding costs a restart of every running server on
// reload. That is the honest cost: a reload already restarts changed servers,
// and an operator editing config.yaml is not on a latency budget. Workspaces
// that were idle pay nothing, because they had no client to stop.
func (p *Pool) ReplaceTemplate(template Config) {
	p.mu.Lock()
	p.template = template
	clients := make([]*Client, 0, len(p.clients))
	for _, client := range p.clients {
		clients = append(clients, client)
	}
	p.clients = make(map[string]*Client)
	p.mu.Unlock()

	for _, client := range clients {
		_ = client.Close()
	}
}
