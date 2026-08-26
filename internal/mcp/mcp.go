// Package mcp implements an MCP (Model Context Protocol) client for Soulacy.
//
// The client manages connections to N configured MCP servers — each over either
// stdio (spawned subprocess speaking newline-delimited JSON-RPC) or HTTP
// (Streamable HTTP transport with optional Server-Sent Events responses).
//
// At startup it runs the MCP handshake (initialize + initialized notification)
// and caches each server's tool list. The engine then offers those tools to
// agents with namespaced names: mcp__<server>__<tool>. When the LLM calls one,
// the engine routes the call to Client.Call which executes tools/call against
// the right server and returns the text content.
//
// Spec reference: https://spec.modelcontextprotocol.io/
package mcp

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/sandbox"
)

// ProtocolVersion is the MCP protocol version this client advertises.
const ProtocolVersion = "2024-11-05"

// FullNamePrefix is the namespace prefix for MCP tool names exposed to the LLM.
const FullNamePrefix = "mcp__"

// Config groups all MCP servers.
type Config struct {
	Servers map[string]ServerConfig
}

// ServerConfig describes one MCP server connection.
type ServerConfig struct {
	Transport string            // "stdio" (default), "http", or approved "container"
	Command   string            // stdio: executable
	Args      []string          // stdio: arguments
	Env       map[string]string // stdio: extra env vars, and the highest-priority source
	// InheritEnv names gateway environment variables this server may see in
	// addition to the base allow-list. Named one at a time on purpose: an
	// operator granting a variable should be choosing it.
	InheritEnv []string
	// InheritAll restores the pre-MU-017 behaviour of passing the gateway's
	// entire environment to the child. It exists so a deployment that depended
	// on inheritance has a documented way back, not because it is safe: it
	// hands third-party code every credential this process holds.
	InheritAll bool
	URL        string            // http: server URL
	Headers    map[string]string // http: extra headers (auth, etc.)
	// PublicRemote marks a URL supplied by a workspace rather than an
	// operator. It forces HTTPS plus public-network-only DNS and redirect
	// handling in the transport. It is runtime enforcement, not just API input
	// validation, so DNS rebinding cannot turn an approved URL into host SSRF.
	PublicRemote bool
	// ContainerNetwork and ContainerWorkspace are approval-bound permissions
	// for workspace-installed container transports. Empty values fail to the
	// safest policy (no network and no workspace mount).
	ContainerNetwork   string
	ContainerWorkspace string
	// ContainerDataDir is a platform-derived host directory for the server's
	// private durable state. Workspace input never controls it.
	ContainerDataDir string

	// WorkDir is the directory the stdio child starts in. Empty inherits the
	// gateway's own working directory, which is what every MCP server did
	// before MU-017 criterion 5 — so a server that resolved a relative path
	// resolved it against the gateway's tree, shared by every tenant. A
	// caller that knows the workspace supplies its confinement root here.
	WorkDir string

	// Limits are the rlimits applied to the stdio child through the sandbox
	// re-exec wrapper. Disabled Limits leave the command unwrapped, which is
	// the behaviour every deployment had until now; SelfPath must also be set
	// for wrapping to happen at all.
	Limits sandbox.Limits

	// SelfPath is the soulacy binary used for the sandbox re-exec. Passed
	// explicitly rather than read from os.Executable() inside the transport so
	// the wrapping decision stays testable without a real binary on disk.
	SelfPath string

	// SharedCredentials lets the operator's own Env and Headers travel into
	// EVERY workspace's copy of this server.
	//
	// Off by default, and the default is the security control: see
	// tenantcreds.go. On means every tenant acts as one identity upstream —
	// which is right for a server with no credentials at all, or one whose
	// credential is genuinely deployment-wide (a site licence key), and wrong
	// for anything that can read or write a tenant's data. It is spelled out
	// in config.yaml so that turning it on is a sentence somebody wrote.
	SharedCredentials bool
}

// Tool is one tool exposed by an MCP server.
type Tool struct {
	ServerID    string
	Name        string
	Description string
	InputSchema map[string]any
}

// FullName returns the namespaced tool name the LLM sees and calls.
func (t Tool) FullName() string {
	return FullNamePrefix + sanitizeID(t.ServerID) + "__" + t.Name
}

// ServerStatus is the API/GUI view of a server (what the MCP page renders).
type ServerStatus struct {
	ID        string            `json:"id"`
	Transport string            `json:"transport"`
	Connected bool              `json:"connected"`
	Detail    string            `json:"detail,omitempty"`
	Tools     []ToolSummary     `json:"tools"`
	Command   string            `json:"command,omitempty"`
	Args      []string          `json:"args,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	URL       string            `json:"url,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
}

// ToolSummary is a short tool descriptor returned by /mcp.
type ToolSummary struct {
	Name        string `json:"name"`
	FullName    string `json:"full_name"`
	Description string `json:"description"`
	// Params is a compact argument hint derived from the tool's input schema,
	// e.g. "title*:string, summary:string" (required args marked with *), so
	// callers pass the right keyword arguments instead of guessing.
	Params string `json:"params,omitempty"`
}

// paramHint summarizes an MCP tool input schema as a compact argument list,
// e.g. "title*:string, description:string". Required properties are marked with
// a trailing "*". Returns "" when there are no properties.
func paramHint(schema map[string]any) string {
	if schema == nil {
		return ""
	}
	props, _ := schema["properties"].(map[string]any)
	if len(props) == 0 {
		return ""
	}
	required := map[string]bool{}
	if rs, ok := schema["required"].([]any); ok {
		for _, r := range rs {
			if s, ok := r.(string); ok {
				required[s] = true
			}
		}
	}
	names := make([]string, 0, len(props))
	for n := range props {
		names = append(names, n)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, n := range names {
		typ := ""
		if pm, ok := props[n].(map[string]any); ok {
			if ts, ok := pm["type"].(string); ok {
				typ = ts
			}
		}
		star := ""
		if required[n] {
			star = "*"
		}
		if typ != "" {
			parts = append(parts, n+star+":"+typ)
		} else {
			parts = append(parts, n+star)
		}
	}
	return strings.Join(parts, ", ")
}

// server tracks the live state of one configured MCP server.
type server struct {
	id        string
	cfg       ServerConfig
	tx        transport
	tools     []Tool
	connected bool
	detail    string
	callMu    sync.Mutex
}

// Client is the top-level MCP client managing multiple servers.
type Client struct {
	log     *zap.Logger
	servers []*server
	mu      sync.RWMutex
}

// New connects to every configured server. Failures are logged but never fatal —
// a misconfigured or unreachable server is recorded with detail and the gateway
// keeps running so others (and non-MCP features) remain usable.
//
// PRODUCTION_AUDIT → HIGH/Reliability: server startups now run in parallel
// goroutines rather than sequentially. A single slow `npx`-launching server
// no longer blocks every other MCP — and by extension the rest of the
// gateway boot. Total wait is capped by the slowest server, not the sum.
func New(cfg Config, log *zap.Logger) *Client {
	c := &Client{log: log}
	ids := make([]string, 0, len(cfg.Servers))
	for id := range cfg.Servers {
		ids = append(ids, id)
	}
	sortStrings(ids)

	// Build server structs in deterministic order so c.servers order is
	// stable for the GUI; start them concurrently.
	servers := make([]*server, len(ids))
	for i, id := range ids {
		servers[i] = &server{id: id, cfg: cfg.Servers[id]}
	}

	var wg sync.WaitGroup
	for _, s := range servers {
		wg.Add(1)
		go func(s *server) {
			defer wg.Done()
			if err := c.start(s); err != nil {
				s.detail = err.Error()
				log.Warn("mcp: server failed to start", zap.String("server", s.id), zap.Error(err))
				return
			}
			s.connected = true
			s.detail = fmt.Sprintf("%d tool(s)", len(s.tools))
			log.Info("mcp: server connected", zap.String("server", s.id), zap.Int("tools", len(s.tools)))
		}(s)
	}
	wg.Wait()

	c.servers = servers
	return c
}

// start opens the transport and runs the MCP handshake + tools/list.
func (c *Client) start(s *server) error {
	transport := strings.ToLower(s.cfg.Transport)
	switch transport {
	case "", "stdio":
		tx, err := newStdio(s.cfg, c.log.With(zap.String("mcp_server", s.id)))
		if err != nil {
			return err
		}
		s.tx = tx
	case "http", "https":
		s.tx = newHTTP(s.cfg)
	case "container":
		tx, err := newContainerStdio(s.cfg, c.log.With(zap.String("mcp_server", s.id)))
		if err != nil {
			return err
		}
		s.tx = tx
	default:
		return fmt.Errorf("unknown transport %q (expected stdio, http, or container)", s.cfg.Transport)
	}

	// Long-ish timeout: stdio servers run via `npx` may need to download their
	// package from npm on first launch, which can take 60s+.
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	if _, err := s.tx.request(ctx, "initialize", map[string]any{
		"protocolVersion": ProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "soulacy", "version": "dev"},
	}); err != nil {
		_ = s.tx.close()
		return fmt.Errorf("initialize: %w", err)
	}
	_ = s.tx.notify("notifications/initialized", nil)

	raw, err := s.tx.request(ctx, "tools/list", nil)
	if err != nil {
		_ = s.tx.close()
		return fmt.Errorf("tools/list: %w", err)
	}
	var lr struct {
		Tools []struct {
			Name        string         `json:"name"`
			Description string         `json:"description"`
			InputSchema map[string]any `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := jsonUnmarshal(raw, &lr); err != nil {
		_ = s.tx.close()
		return fmt.Errorf("decode tools: %w", err)
	}
	s.tools = make([]Tool, 0, len(lr.Tools))
	for _, t := range lr.Tools {
		if blockedHostedTool(s.id, t.Name) {
			continue
		}
		s.tools = append(s.tools, Tool{
			ServerID: s.id, Name: t.Name, Description: t.Description, InputSchema: t.InputSchema,
		})
	}
	return nil
}

// AllTools returns every tool from every connected server.
func (c *Client) AllTools() []Tool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var out []Tool
	for _, s := range c.servers {
		if s.connected {
			out = append(out, s.tools...)
		}
	}
	return out
}

// Call dispatches a tool call by full name (mcp__<server>__<tool>) to the
// matching server. Returns the concatenated text of the response content.
func (c *Client) Call(ctx context.Context, fullName string, args map[string]any) (string, error) {
	if !strings.HasPrefix(fullName, FullNamePrefix) {
		return "", fmt.Errorf("not an MCP tool: %s", fullName)
	}
	rest := strings.TrimPrefix(fullName, FullNamePrefix)
	parts := strings.SplitN(rest, "__", 2)
	if len(parts) != 2 {
		return "", fmt.Errorf("malformed MCP tool name %q (expected mcp__<server>__<tool>)", fullName)
	}
	serverID, toolName := parts[0], parts[1]
	if blockedHostedTool(serverID, toolName) {
		return "", fmt.Errorf("MCP tool %q is blocked by Soulacy's hosted-integration policy", fullName)
	}

	var srv *server
	c.mu.RLock()
	for _, s := range c.servers {
		if sanitizeID(s.id) == serverID && s.connected {
			srv = s
			break
		}
	}
	c.mu.RUnlock()
	if srv == nil {
		return "", fmt.Errorf("MCP server %q not available", serverID)
	}

	if args == nil {
		args = map[string]any{}
	}
	srv.callMu.Lock()
	defer srv.callMu.Unlock()
	var janitor *processJanitor
	if rooter, ok := srv.tx.(processRooter); ok {
		janitor = newProcessJanitor(rooter.processRootPID(), c.log.With(
			zap.String("mcp_server", srv.id),
			zap.String("mcp_tool", toolName),
		))
		defer janitor.Cleanup(context.Background())
	}
	raw, err := srv.tx.request(ctx, "tools/call", map[string]any{
		"name":      toolName,
		"arguments": args,
	})
	if err != nil {
		return "", err
	}
	var cr struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := jsonUnmarshal(raw, &cr); err != nil {
		return "", fmt.Errorf("decode tool result: %w", err)
	}
	var sb strings.Builder
	for _, p := range cr.Content {
		if p.Type == "text" {
			if sb.Len() > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(p.Text)
		}
	}
	text := strings.TrimSpace(sb.String())
	if cr.IsError {
		return "", fmt.Errorf("%s", text)
	}
	if text == "" {
		text = "(no text content)"
	}
	return text, nil
}

// blockedHostedTool is a second boundary behind the Composio session's own
// tool preset. Connected-app sessions are useful precisely because they can
// expose narrowly scoped SaaS actions; their generic remote shell/workbench
// escape hatches are never appropriate inside the Soulacy agent runtime.
func blockedHostedTool(serverID, toolName string) bool {
	if sanitizeID(serverID) != "composio" {
		return false
	}
	name := strings.ToLower(strings.NewReplacer("-", "_", ".", "_").Replace(strings.TrimSpace(toolName)))
	return strings.Contains(name, "remote_workbench") ||
		strings.Contains(name, "remote_bash") ||
		strings.Contains(name, "shell_exec")
}

// ServersSnapshot returns the current view of all configured servers for the
// admin API. Safe to call from any goroutine.
func (c *Client) ServersSnapshot() []ServerStatus {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]ServerStatus, 0, len(c.servers))
	for _, s := range c.servers {
		transport := strings.ToLower(s.cfg.Transport)
		if transport == "" {
			transport = "stdio"
		}
		ss := ServerStatus{
			ID: s.id, Transport: transport, Connected: s.connected, Detail: s.detail,
			Tools:   make([]ToolSummary, 0, len(s.tools)),
			Command: s.cfg.Command,
			Args:    s.cfg.Args,
			Env:     s.cfg.Env,
			URL:     s.cfg.URL,
			Headers: s.cfg.Headers,
		}
		for _, t := range s.tools {
			ss.Tools = append(ss.Tools, ToolSummary{
				Name: t.Name, FullName: t.FullName(), Description: t.Description,
				Params: paramHint(t.InputSchema),
			})
		}
		out = append(out, ss)
	}
	return out
}

// AddServer starts a new MCP server at runtime and adds it to the client.
// If a server with the same id already exists it is stopped first (update semantics).
// This allows hot-adding servers after config.yaml is written without a gateway restart.
func (c *Client) AddServer(id string, cfg ServerConfig) error {
	// Stop any existing server with this id.
	_ = c.RemoveServer(id)

	s := &server{id: id, cfg: cfg}
	if err := c.start(s); err != nil {
		s.detail = err.Error()
		c.log.Warn("mcp: hot-add failed", zap.String("server", id), zap.Error(err))
		c.mu.Lock()
		c.servers = append(c.servers, s)
		c.mu.Unlock()
		return err
	}
	s.connected = true
	s.detail = fmt.Sprintf("%d tool(s)", len(s.tools))
	c.log.Info("mcp: server hot-added", zap.String("server", id), zap.Int("tools", len(s.tools)))

	c.mu.Lock()
	c.servers = append(c.servers, s)
	c.mu.Unlock()
	return nil
}

// drainGrace bounds how long RemoveServer waits for an in-flight tool call to
// finish before shutting the subprocess down underneath it.
//
// A variable rather than a constant so the revocation tests can exercise the
// timeout path in milliseconds instead of parking the suite for ten seconds.
// Production never writes it.
var drainGrace = 10 * time.Second

// RemoveServer stops and removes a server by id. Returns nil if not found.
//
// MU-017 criterion 7 in two distinct steps, and the order matters:
//
//   - The server leaves the registry FIRST, under c.mu, so no new invocation
//     can find it. Revocation has to take effect immediately; a caller that
//     resolved the server a moment earlier is already inside its call.
//   - Only then does it drain and close, OUTSIDE c.mu. Holding the client
//     lock across a shutdown would block every other MCP operation for the
//     length of a stranger's tool call — one revoked extension freezing the
//     rest is a worse failure than the one being fixed.
//
// The drain waits for the in-flight call to release callMu, bounded: a tool
// that never returns must not make revocation unavailable.
func (c *Client) RemoveServer(id string) error {
	var target *server
	c.mu.Lock()
	for i, s := range c.servers {
		if s.id == id {
			target = s
			c.servers = append(c.servers[:i], c.servers[i+1:]...)
			break
		}
	}
	c.mu.Unlock()
	if target == nil {
		return nil
	}

	drained := make(chan struct{})
	go func() {
		target.callMu.Lock()
		target.callMu.Unlock()
		close(drained)
	}()
	select {
	case <-drained:
	case <-time.After(drainGrace):
		c.log.Warn("mcp: server removed with a call still in flight",
			zap.String("server", id), zap.Duration("waited", drainGrace))
	}

	if target.tx != nil {
		_ = target.tx.close()
	}
	c.log.Info("mcp: server removed", zap.String("server", id))
	return nil
}

// Close shuts down all transports.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, s := range c.servers {
		if s.tx != nil {
			_ = s.tx.close()
		}
	}
	return nil
}

// sanitizeID makes a server ID safe to embed in a tool name (lowercase, _).
func sanitizeID(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		default:
			return '_'
		}
	}, s)
}

func sortStrings(s []string) {
	// Tiny insertion sort to avoid importing "sort" for one call.
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}
