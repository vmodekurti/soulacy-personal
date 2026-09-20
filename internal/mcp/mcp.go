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
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// ProtocolVersion is the MCP protocol version this client advertises.
const ProtocolVersion = "2024-11-05"

// FullNamePrefix is the namespace prefix for MCP tool names exposed to the LLM.
const FullNamePrefix = "mcp__"

// Config groups all MCP servers.
type Config struct {
	Servers       map[string]ServerConfig
	ResolveSecret func(context.Context, string) (string, error)
}

// ServerConfig describes one MCP server connection.
type ServerConfig struct {
	Transport     string            // "stdio" (default) or "http"
	Command       string            // stdio: executable
	Args          []string          // stdio: arguments
	Env           map[string]string // stdio: extra env vars (merged onto os.Environ)
	EnvSecretRefs map[string]string // stdio: env name → encrypted vault key
	URL           string            // http: server URL
	Headers       map[string]string // http: extra headers (auth, etc.)
	Query         map[string]string // http: non-sensitive URL query parameters
	Auth          AuthConfig        // http: structured authentication
	Timeout       time.Duration     // http: per-request timeout
	PublicOnly    bool              // use NetGuard and reject private/loopback destinations
	ManagedRoot   string            // stdio: executable must resolve beneath this directory

	// KeepsProcesses exempts this server from the per-call process janitor.
	//
	// The janitor kills any child process a tool call leaves behind, which is
	// right for a tool that shells out and wrong for a server whose child
	// process IS its state. A browser server is the clear case: the first call
	// launches a browser, the janitor kills it a second later, and the next
	// call finds a fresh about:blank — the page navigated to is simply gone,
	// with nothing in the reply to say why.
	KeepsProcesses bool
}

type AuthConfig struct {
	Type            string   `json:"type,omitempty"`
	Header          string   `json:"header,omitempty"`
	Scheme          string   `json:"scheme,omitempty"`
	SecretRef       string   `json:"secret_ref,omitempty"`
	TokenURL        string   `json:"token_url,omitempty"`
	ClientID        string   `json:"client_id,omitempty"`
	ClientSecretRef string   `json:"client_secret_ref,omitempty"`
	Scopes          []string `json:"scopes,omitempty"`
	Audience        string   `json:"audience,omitempty"`
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
	ID            string            `json:"id"`
	Transport     string            `json:"transport"`
	Connected     bool              `json:"connected"`
	Detail        string            `json:"detail,omitempty"`
	Tools         []ToolSummary     `json:"tools"`
	Command       string            `json:"command,omitempty"`
	Args          []string          `json:"args,omitempty"`
	Env           map[string]string `json:"env,omitempty"`
	EnvSecretRefs map[string]string `json:"env_secret_refs,omitempty"`
	URL           string            `json:"url,omitempty"`
	Headers       map[string]string `json:"headers,omitempty"`
	Query         map[string]string `json:"query,omitempty"`
	Auth          AuthConfig        `json:"auth,omitempty"`
	Timeout       string            `json:"timeout,omitempty"`
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
	log           *zap.Logger
	servers       []*server
	addLocks      map[string]*sync.Mutex // per-id AddServer serialization
	mu            sync.RWMutex
	resolveSecret func(context.Context, string) (string, error)
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
	c := &Client{log: log, resolveSecret: cfg.ResolveSecret}
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
		tx, err := newStdio(s.cfg, c.resolveSecret, c.log.With(zap.String("mcp_server", s.id)))
		if err != nil {
			return err
		}
		s.tx = tx
	case "http", "https":
		s.tx = newHTTP(s.cfg, c.resolveSecret)
	default:
		return fmt.Errorf("unknown transport %q (expected stdio or http)", s.cfg.Transport)
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
	if rooter, ok := srv.tx.(processRooter); ok && !srv.cfg.KeepsProcesses {
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
		c.noteCallOutcome(srv, err.Error())
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
		c.noteCallOutcome(srv, text)
		return "", fmt.Errorf("%s", text)
	}
	c.noteCallOutcome(srv, "")
	if text == "" {
		text = "(no text content)"
	}
	return text, nil
}

// credentialRejectedPrefix marks a server whose last tool call the remote
// side refused for want of a valid credential. It is shown on the MCP page in
// place of the tool count, because "Connected · 117 tools" is exactly what a
// server that lists tools anonymously but rejects every call looks like.
const credentialRejectedPrefix = "credential rejected"

// authFailureRe matches what servers say when they did not accept the
// credential: HTTP 401/403 from the transport, or the usual wording in an
// isError result.
var authFailureRe = regexp.MustCompile(`(?i)(^|\b)(http 401|http 403|unauthori[sz]ed|forbidden|missing api key|invalid api key|invalid or expired (access )?token|authentication (failed|required))\b`)

func looksLikeAuthFailure(msg string) bool { return authFailureRe.MatchString(msg) }

// noteCallOutcome updates the server's status line after a tool call: an
// auth-shaped failure flags the credential, and the next success clears the
// flag again. Other failures leave the line alone — one bad argument is not a
// server problem.
func (c *Client) noteCallOutcome(s *server, failure string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch {
	case failure == "":
		if strings.HasPrefix(s.detail, credentialRejectedPrefix) {
			s.detail = fmt.Sprintf("%d tool(s)", len(s.tools))
		}
	case looksLikeAuthFailure(failure):
		msg := strings.TrimSpace(failure)
		if len(msg) > 160 {
			msg = msg[:160] + "…"
		}
		s.detail = credentialRejectedPrefix + ": " + msg
		c.log.Warn("mcp: server rejected the credential on a tool call", zap.String("server", s.id), zap.String("error", msg))
	}
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
			Tools:         make([]ToolSummary, 0, len(s.tools)),
			Command:       s.cfg.Command,
			Args:          s.cfg.Args,
			Env:           s.cfg.Env,
			EnvSecretRefs: s.cfg.EnvSecretRefs,
			URL:           s.cfg.URL,
			Headers:       s.cfg.Headers,
			Query:         s.cfg.Query,
			Auth:          s.cfg.Auth,
		}
		if s.cfg.Timeout > 0 {
			ss.Timeout = s.cfg.Timeout.String()
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
	// One add at a time per id. start() can take seconds (an HTTP handshake,
	// or npx fetching a package) and runs with c.mu released; without this,
	// two overlapping saves of the same server both got past RemoveServer and
	// both appended, and the GUI listed the server twice.
	mu := c.addLock(id)
	mu.Lock()
	defer mu.Unlock()

	// Stop any existing server with this id.
	_ = c.RemoveServer(id)

	s := &server{id: id, cfg: cfg}
	if err := c.start(s); err != nil {
		s.detail = err.Error()
		c.log.Warn("mcp: hot-add failed", zap.String("server", id), zap.Error(err))
		c.put(s)
		return err
	}
	s.connected = true
	s.detail = fmt.Sprintf("%d tool(s)", len(s.tools))
	c.log.Info("mcp: server hot-added", zap.String("server", id), zap.Int("tools", len(s.tools)))
	c.put(s)
	return nil
}

// addLock returns the per-id mutex that serializes AddServer.
func (c *Client) addLock(id string) *sync.Mutex {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.addLocks == nil {
		c.addLocks = map[string]*sync.Mutex{}
	}
	mu, ok := c.addLocks[id]
	if !ok {
		mu = &sync.Mutex{}
		c.addLocks[id] = mu
	}
	return mu
}

// put records s in the server list, replacing (and closing) any entry that
// already carries the same id so an id can never appear twice.
func (c *Client) put(s *server) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, existing := range c.servers {
		if existing.id == s.id {
			if existing.tx != nil && existing != s {
				_ = existing.tx.close()
			}
			c.servers[i] = s
			return
		}
	}
	c.servers = append(c.servers, s)
}

// RemoveServer stops and removes a server by id. Returns nil if not found.
func (c *Client) RemoveServer(id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, s := range c.servers {
		if s.id == id {
			// Children are listed before the parent is closed: afterwards
			// they are reparented to init and indistinguishable from anything
			// else running.
			var orphans []int
			if rooter, ok := s.tx.(processRooter); ok {
				orphans = snapshotDescendants(rooter.processRootPID())
			}
			if s.tx != nil {
				_ = s.tx.close()
			}
			c.servers = append(c.servers[:i], c.servers[i+1:]...)
			c.log.Info("mcp: server removed", zap.String("server", id))
			reapStopped(orphans, c.log.With(zap.String("mcp_server", id)))
			return nil
		}
	}
	return nil
}

// Close shuts down all transports.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, s := range c.servers {
		var orphans []int
		if rooter, ok := s.tx.(processRooter); ok {
			orphans = snapshotDescendants(rooter.processRootPID())
		}
		if s.tx != nil {
			_ = s.tx.close()
		}
		// A shutdown that leaves browsers running makes the NEXT start fail:
		// the orphan still holds the runtime directory its replacement wants.
		reapStopped(orphans, c.log.With(zap.String("mcp_server", s.id)))
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
