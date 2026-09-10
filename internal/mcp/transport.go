// transport.go — JSON-RPC transports for the MCP client.
//
// Two transports are implemented:
//
//   - stdio: spawn the configured command and exchange newline-delimited
//     JSON-RPC messages over stdin/stdout. This is the most common MCP setup
//     (filesystem, GitHub, Slack, Postgres servers, etc. all ship stdio).
//
//   - http:  POST JSON-RPC requests to a single URL. The response may be
//     either application/json (single response) or text/event-stream
//     (Streamable HTTP — SSE-framed messages); both are handled. Session
//     continuity uses the Mcp-Session-Id header echoed by the server.
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/soulacy/soulacy/internal/netguard"
	"go.uber.org/zap"
)

// transport is the minimal contract every MCP transport satisfies.
type transport interface {
	request(ctx context.Context, method string, params any) (json.RawMessage, error)
	notify(method string, params any) error
	close() error
}

type processRooter interface {
	processRootPID() int
}

// rpcMsg is a JSON-RPC 2.0 envelope (request, response, or notification).
type rpcMsg struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Method  string           `json:"method,omitempty"`
	Params  any              `json:"params,omitempty"`
	Result  json.RawMessage  `json:"result,omitempty"`
	Error   *rpcError        `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// maxHTTPResponseBytes caps a single JSON-RPC response read over the HTTP
// transport, matching the 16 MB budget the stdio scanner already uses.
const maxHTTPResponseBytes = 16 << 20

func jsonUnmarshal(data []byte, v any) error {
	if len(data) == 0 {
		return fmt.Errorf("empty payload")
	}
	return json.Unmarshal(data, v)
}

// ── stdio transport ──────────────────────────────────────────────────────────

type stdioTx struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	log     *zap.Logger
	nextID  atomic.Int64
	mu      sync.Mutex // guards pending ONLY
	pending map[int64]chan rpcMsg
	// writeMu serialises writes to the child's stdin. It is deliberately NOT mu:
	// holding mu across a blocking Write deadlocks the transport. Cycle: this
	// goroutine holds mu writing a large request into a full stdin pipe; the
	// child is not draining stdin because it is blocked writing a large response
	// into a full stdout pipe; readLoop cannot drain stdout because it needs mu
	// to dispatch. Two mutexes, no cycle.
	writeMu sync.Mutex
	closed  atomic.Bool
}

func newStdio(cfg ServerConfig, resolveSecret func(context.Context, string) (string, error), log *zap.Logger) (*stdioTx, error) {
	if cfg.Command == "" {
		return nil, fmt.Errorf("stdio: command is required")
	}
	if cfg.ManagedRoot != "" {
		resolvedRoot, err := filepath.EvalSymlinks(cfg.ManagedRoot)
		if err != nil {
			return nil, fmt.Errorf("stdio: resolve managed root: %w", err)
		}
		resolvedCommand, err := filepath.EvalSymlinks(cfg.Command)
		if err != nil {
			return nil, fmt.Errorf("stdio: resolve managed executable: %w", err)
		}
		rel, err := filepath.Rel(resolvedRoot, resolvedCommand)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("stdio: executable escaped managed root %s", resolvedRoot)
		}
	}
	cmd := exec.Command(cfg.Command, cfg.Args...)
	env := os.Environ()
	for k, v := range cfg.Env {
		env = append(env, k+"="+v)
	}
	for envName, secretRef := range cfg.EnvSecretRefs {
		if resolveSecret == nil {
			return nil, fmt.Errorf("stdio: environment %s requires vault secret %q, but secret resolution is unavailable", envName, secretRef)
		}
		value, err := resolveSecret(context.Background(), secretRef)
		if err != nil {
			return nil, fmt.Errorf("stdio: resolve vault secret %q for environment %s: %w", secretRef, envName, err)
		}
		env = append(env, envName+"="+value)
	}
	cmd.Env = env

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, _ := cmd.StderrPipe()

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", cfg.Command, err)
	}

	t := &stdioTx{
		cmd: cmd, stdin: stdin, log: log,
		pending: make(map[int64]chan rpcMsg),
	}

	// Drain stderr so the server doesn't block; log a few sample lines.
	go func() {
		if stderr == nil {
			return
		}
		sc := bufio.NewScanner(stderr)
		sc.Buffer(make([]byte, 1<<16), 1<<20)
		count := 0
		for sc.Scan() {
			count++
			if count <= 5 {
				log.Debug("mcp stderr", zap.String("line", sc.Text()))
			}
		}
	}()

	// Reader goroutine — dispatches responses to waiting callers by id.
	go t.readLoop(stdout)

	return t, nil
}

func (t *stdioTx) readLoop(stdout io.Reader) {
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 1<<16), 16<<20) // tolerate large tool responses
	for sc.Scan() {
		line := sc.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var msg rpcMsg
		if err := json.Unmarshal(line, &msg); err != nil {
			t.log.Debug("mcp stdio: malformed line", zap.Error(err))
			continue
		}
		if msg.ID == nil {
			continue // notification — ignore for now
		}
		var id int64
		if err := json.Unmarshal(*msg.ID, &id); err != nil {
			continue
		}
		t.mu.Lock()
		ch, ok := t.pending[id]
		delete(t.pending, id)
		t.mu.Unlock()
		if ok {
			ch <- msg
		}
	}
	// On EOF / error, fail any pending requests.
	t.closed.Store(true)
	t.mu.Lock()
	for id, ch := range t.pending {
		close(ch)
		delete(t.pending, id)
	}
	t.mu.Unlock()
}

func (t *stdioTx) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	if t.closed.Load() {
		return nil, fmt.Errorf("stdio transport closed")
	}
	id := t.nextID.Add(1)
	ch := make(chan rpcMsg, 1)
	t.mu.Lock()
	t.pending[id] = ch
	t.mu.Unlock()

	m := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		m["params"] = params
	}
	payload, _ := json.Marshal(m)
	t.writeMu.Lock()
	_, err := t.stdin.Write(append(payload, '\n'))
	t.writeMu.Unlock()
	if err != nil {
		t.mu.Lock()
		delete(t.pending, id)
		t.mu.Unlock()
		return nil, err
	}

	select {
	case msg, ok := <-ch:
		if !ok {
			return nil, fmt.Errorf("stdio transport closed before response")
		}
		if msg.Error != nil {
			return nil, fmt.Errorf("mcp rpc error %d: %s", msg.Error.Code, msg.Error.Message)
		}
		return msg.Result, nil
	case <-ctx.Done():
		t.mu.Lock()
		delete(t.pending, id)
		t.mu.Unlock()
		return nil, ctx.Err()
	}
}

func (t *stdioTx) notify(method string, params any) error {
	if t.closed.Load() {
		return fmt.Errorf("stdio transport closed")
	}
	m := map[string]any{"jsonrpc": "2.0", "method": method}
	if params != nil {
		m["params"] = params
	}
	payload, _ := json.Marshal(m)
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	_, err := t.stdin.Write(append(payload, '\n'))
	return err
}

func (t *stdioTx) close() error {
	t.closed.Store(true)
	_ = t.stdin.Close()
	if t.cmd.Process != nil {
		_ = t.cmd.Process.Kill()
	}
	return nil
}

func (t *stdioTx) processRootPID() int {
	if t == nil || t.cmd == nil || t.cmd.Process == nil {
		return 0
	}
	return t.cmd.Process.Pid
}

// ── HTTP / SSE transport ─────────────────────────────────────────────────────

type httpTx struct {
	url           string
	headers       map[string]string
	query         map[string]string
	auth          AuthConfig
	resolveSecret func(context.Context, string) (string, error)
	client        *http.Client
	tokenMu       sync.Mutex
	token         string
	tokenExpiry   time.Time
	nextID        atomic.Int64
	sessionID     atomic.Pointer[string]
}

type oauthTokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int64  `json:"expires_in"`
}

func newHTTP(cfg ServerConfig, resolveSecret func(context.Context, string) (string, error)) *httpTx {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	client := &http.Client{Timeout: timeout}
	if cfg.PublicOnly {
		client = netguard.NewHTTPClient(timeout, true, nil)
	}
	return &httpTx{
		url:           cfg.URL,
		headers:       cfg.Headers,
		query:         cfg.Query,
		auth:          cfg.Auth,
		resolveSecret: resolveSecret,
		client:        client,
	}
}

func (t *httpTx) requestURL() (string, error) {
	u, err := url.Parse(t.url)
	if err != nil {
		return "", err
	}
	q := u.Query()
	for k, v := range t.query {
		q.Set(k, v)
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func (t *httpTx) secret(ctx context.Context, ref string) (string, error) {
	if strings.TrimSpace(ref) == "" {
		return "", fmt.Errorf("authentication secret is not configured")
	}
	if t.resolveSecret == nil {
		return "", fmt.Errorf("secret store is unavailable")
	}
	return t.resolveSecret(ctx, ref)
}

func (t *httpTx) oauthToken(ctx context.Context) (string, error) {
	t.tokenMu.Lock()
	defer t.tokenMu.Unlock()
	if t.token != "" && time.Now().Add(30*time.Second).Before(t.tokenExpiry) {
		return t.token, nil
	}
	secret, err := t.secret(ctx, t.auth.ClientSecretRef)
	if err != nil {
		return "", err
	}
	values := url.Values{"grant_type": {"client_credentials"}}
	if len(t.auth.Scopes) > 0 {
		values.Set("scope", strings.Join(t.auth.Scopes, " "))
	}
	if t.auth.Audience != "" {
		values.Set("audience", t.auth.Audience)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.auth.TokenURL, strings.NewReader(values.Encode()))
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(t.auth.ClientID, secret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := t.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("OAuth token request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("OAuth token endpoint returned HTTP %d", resp.StatusCode)
	}
	var token oauthTokenResponse
	if err := json.Unmarshal(body, &token); err != nil || token.AccessToken == "" {
		return "", fmt.Errorf("OAuth token endpoint returned an invalid response")
	}
	t.token = token.AccessToken
	expires := token.ExpiresIn
	if expires <= 0 {
		expires = 300
	}
	t.tokenExpiry = time.Now().Add(time.Duration(expires) * time.Second)
	return t.token, nil
}

func (t *httpTx) applyAuth(ctx context.Context, req *http.Request) error {
	kind := strings.ToLower(strings.TrimSpace(t.auth.Type))
	if kind == "" || kind == "none" {
		return nil
	}
	header := strings.TrimSpace(t.auth.Header)
	if header == "" {
		if kind == "api_key" {
			header = "X-API-Key"
		} else {
			header = "Authorization"
		}
	}
	var value string
	switch kind {
	case "bearer", "api_key":
		secret, err := t.secret(ctx, t.auth.SecretRef)
		if err != nil {
			return err
		}
		scheme := strings.TrimSpace(t.auth.Scheme)
		if kind == "bearer" && scheme == "" {
			scheme = "Bearer"
		}
		value = secret
		if scheme != "" {
			value = scheme + " " + secret
		}
	case "oauth_client_credentials":
		token, err := t.oauthToken(ctx)
		if err != nil {
			return err
		}
		value = "Bearer " + token
	default:
		return fmt.Errorf("unsupported MCP authentication type %q", t.auth.Type)
	}
	req.Header.Set(header, value)
	return nil
}

func (t *httpTx) applyRequestOptions(ctx context.Context, req *http.Request) error {
	for k, v := range t.headers {
		req.Header.Set(k, v)
	}
	return t.applyAuth(ctx, req)
}

func (t *httpTx) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	if t.url == "" {
		return nil, fmt.Errorf("http: url is required")
	}
	id := t.nextID.Add(1)
	payload, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": method, "params": params,
	})
	requestURL, err := t.requestURL()
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", requestURL, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if sid := t.sessionID.Load(); sid != nil {
		req.Header.Set("Mcp-Session-Id", *sid)
	}
	if err := t.applyRequestOptions(ctx, req); err != nil {
		return nil, err
	}

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		t.sessionID.Store(&sid)
	}

	// Bounded to the same budget as the stdio scanner below. An MCP server is
	// third-party code by definition — a compromised or simply broken one should
	// not be able to exhaust the gateway's memory with one oversized response.
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxHTTPResponseBytes))
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	if strings.HasPrefix(ct, "text/event-stream") {
		return extractSSEResponse(body, id)
	}
	var msg rpcMsg
	if err := json.Unmarshal(body, &msg); err != nil {
		return nil, fmt.Errorf("decode http json: %w", err)
	}
	if msg.Error != nil {
		return nil, fmt.Errorf("mcp rpc error %d: %s", msg.Error.Code, msg.Error.Message)
	}
	return msg.Result, nil
}

// extractSSEResponse walks an SSE body's data: events looking for the rpc
// message whose id matches wantID. SSE events are separated by blank lines and
// may have multiple data: lines that concatenate.
func extractSSEResponse(body []byte, wantID int64) (json.RawMessage, error) {
	sc := bufio.NewScanner(bytes.NewReader(body))
	sc.Buffer(make([]byte, 1<<16), 16<<20)
	var data strings.Builder
	flush := func() (json.RawMessage, bool, error) {
		s := strings.TrimSpace(data.String())
		data.Reset()
		if s == "" {
			return nil, false, nil
		}
		var m rpcMsg
		if err := json.Unmarshal([]byte(s), &m); err != nil {
			return nil, false, nil // skip non-JSON data blocks
		}
		if m.ID == nil {
			return nil, false, nil
		}
		var id int64
		if err := json.Unmarshal(*m.ID, &id); err != nil || id != wantID {
			return nil, false, nil
		}
		if m.Error != nil {
			return nil, true, fmt.Errorf("mcp rpc error %d: %s", m.Error.Code, m.Error.Message)
		}
		return m.Result, true, nil
	}
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			if r, found, err := flush(); found {
				return r, err
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			data.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			data.WriteString("\n")
		}
	}
	if r, found, err := flush(); found {
		return r, err
	}
	return nil, fmt.Errorf("no SSE response matched id %d", wantID)
}

func (t *httpTx) notify(method string, params any) error {
	if t.url == "" {
		return fmt.Errorf("http: url is required")
	}
	payload, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "method": method, "params": params,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	requestURL, err := t.requestURL()
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", requestURL, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if sid := t.sessionID.Load(); sid != nil {
		req.Header.Set("Mcp-Session-Id", *sid)
	}
	if err := t.applyRequestOptions(ctx, req); err != nil {
		return err
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func (t *httpTx) close() error { return nil }

// ProbeHTTP performs the real MCP initialize handshake with the supplied HTTP
// connection settings. It is used by the GUI's Test connection action so a
// successful result proves that query parameters and authentication work, not
// merely that the host answers HEAD requests.
func ProbeHTTP(ctx context.Context, cfg ServerConfig, resolveSecret func(context.Context, string) (string, error)) error {
	t := newHTTP(cfg, resolveSecret)
	defer t.close()
	_, err := t.request(ctx, "initialize", map[string]any{
		"protocolVersion": ProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "soulacy-connection-test", "version": "dev"},
	})
	return err
}
