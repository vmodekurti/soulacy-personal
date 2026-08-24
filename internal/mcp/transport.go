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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/dockerutil"
	"github.com/soulacy/soulacy/internal/mcppackage"
	"github.com/soulacy/soulacy/internal/netguard"
	"github.com/soulacy/soulacy/internal/sandbox"
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

func newStdio(cfg ServerConfig, log *zap.Logger) (*stdioTx, error) {
	if cfg.Command == "" {
		return nil, fmt.Errorf("stdio: command is required")
	}
	// CONFINEMENT, MU-017 criterion 5. Three separate things an MCP server
	// used to have and should not: the gateway's whole environment (closed
	// earlier by ProcessEnv), the gateway's working directory, and no
	// resource ceiling at all.
	//
	// The working directory is the one that crosses tenants. An MCP server is
	// a general-purpose subprocess — a filesystem server, a git server, a
	// shell — and every relative path it resolves resolved against wherever
	// the gateway happened to be started. That is one directory for the whole
	// deployment, so two workspaces' servers wrote into the same tree and
	// could read each other's files by naming an ordinary relative path. The
	// caller that knows the workspace passes its confinement root; the
	// transport does not guess one, because a guessed default here would be
	// the shared directory again under a different name.
	argv := append([]string{cfg.Command}, cfg.Args...)
	argv = sandbox.Wrap(cfg.SelfPath, cfg.Limits, argv)
	cmd := exec.Command(argv[0], argv[1:]...)
	if dir := strings.TrimSpace(cfg.WorkDir); dir != "" {
		cmd.Dir = dir
	}
	env, withheld := ProcessEnv(cfg, os.Environ())
	cmd.Env = env
	if withheld > 0 {
		// Counted, not named: a variable name is itself a hint about what this
		// deployment holds. The number is enough for an operator to recognise
		// this as the cause when a server complains about a missing setting,
		// and the fix is mcp.servers.<id>.inherit_env.
		log.Info("mcp: gateway environment withheld from server subprocess",
			zap.Int("withheld", withheld), zap.Int("passed", len(env)),
			zap.String("hint", "add the variable to mcp.servers.<id>.env or .inherit_env if the server needs it"))
	}

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

// newContainerStdio launches an admin-approved, immutable OCI image and uses
// its stdio as the MCP transport. Command is the image digest, not a host
// executable. Every docker option is generated here; workspace input can
// neither add mounts nor weaken the isolation profile.
func newContainerStdio(cfg ServerConfig, log *zap.Logger) (*stdioTx, error) {
	args, err := containerDockerArgs(cfg)
	if err != nil {
		return nil, err
	}
	dockerBin, err := dockerutil.Resolve()
	if err != nil {
		return nil, fmt.Errorf("container: %w", err)
	}
	dockerEnv := dockerutil.Environ()
	dockerPATH := ""
	for _, entry := range dockerEnv {
		if strings.HasPrefix(entry, "PATH=") {
			dockerPATH = strings.TrimPrefix(entry, "PATH=")
			break
		}
	}
	return newStdio(ServerConfig{Command: dockerBin, Args: args, Env: map[string]string{"PATH": dockerPATH}}, log)
}

// containerDockerArgs is deliberately the only place where a tenant-owned
// container command is assembled. Keeping this pure also makes the complete
// isolation profile testable without a Docker daemon.
func containerDockerArgs(cfg ServerConfig) ([]string, error) {
	image := strings.TrimSpace(cfg.Command)
	if strings.HasPrefix(image, "sha256:") {
		digest := strings.TrimPrefix(image, "sha256:")
		digestBytes, digestErr := hex.DecodeString(digest)
		if digestErr != nil || len(digestBytes) != sha256.Size || strings.ToLower(digest) != digest {
			return nil, fmt.Errorf("container: immutable image digest is required")
		}
	} else {
		digestAt := strings.LastIndex(image, "@sha256:")
		digest := ""
		if digestAt >= 0 {
			digest = image[digestAt+len("@sha256:"):]
		}
		digestBytes, digestErr := hex.DecodeString(digest)
		if digestAt <= 0 || digestErr != nil || len(digestBytes) != sha256.Size || strings.ToLower(digest) != digest {
			return nil, fmt.Errorf("container: immutable image digest is required")
		}
	}
	args := []string{
		"run", "--rm", "-i",
		"--init",
		"--read-only",
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
		"--pids-limit", "128",
		"--memory", "1g",
		"--memory-swap", "1g",
		"--cpus", "1",
		"--ulimit", "nofile=1024:1024",
		// npm/PyPI runners install their exact-version package into this
		// disposable filesystem. It must permit execution, but is never mounted
		// from the host and disappears with --rm.
		"--tmpfs", "/tmp:rw,nosuid,nodev,size=256m",
		"--env", "HOME=/tmp",
		"--env", "XDG_CACHE_HOME=/tmp/.cache",
		"--env", "XDG_CONFIG_HOME=/tmp/.config",
	}
	uid, gid := os.Getuid(), os.Getgid()
	if uid == 0 {
		uid, gid = 65532, 65532
	}
	args = append(args, "--user", strconv.Itoa(uid)+":"+strconv.Itoa(gid))

	switch strings.ToLower(strings.TrimSpace(cfg.ContainerNetwork)) {
	case "", "none":
		args = append(args, "--network", "none")
	case "public":
		// This is an explicit permission disclosed in the approval report. Host
		// networking remains impossible and the Docker socket is never mounted.
		args = append(args, "--network", "bridge", "--add-host", "host.docker.internal:127.0.0.1")
	default:
		return nil, fmt.Errorf("container: unsupported network permission %q", cfg.ContainerNetwork)
	}

	switch strings.ToLower(strings.TrimSpace(cfg.ContainerWorkspace)) {
	case "", "none":
	case "read", "write":
		root := strings.TrimSpace(cfg.WorkDir)
		if root == "" || !filepath.IsAbs(root) || strings.Contains(root, ",") {
			return nil, fmt.Errorf("container: workspace confinement is unavailable")
		}
		mount := "type=bind,src=" + root + ",dst=/workspace"
		if strings.EqualFold(cfg.ContainerWorkspace, "read") {
			mount += ",readonly"
		}
		args = append(args, "--mount", mount, "--workdir", "/workspace")
	default:
		return nil, fmt.Errorf("container: unsupported workspace permission %q", cfg.ContainerWorkspace)
	}
	if dataDir := strings.TrimSpace(cfg.ContainerDataDir); dataDir != "" {
		if !filepath.IsAbs(dataDir) || strings.Contains(dataDir, ",") {
			return nil, fmt.Errorf("container: invalid private data directory")
		}
		args = append(args, "--mount", "type=bind,src="+dataDir+",dst=/data")
	}

	for key, value := range cfg.Env {
		if key == "" || strings.ContainsAny(key, "=\x00") || strings.ContainsRune(value, '\x00') {
			return nil, fmt.Errorf("container: invalid environment entry")
		}
		args = append(args, "--env", key+"="+value)
	}
	args = append(args, image)
	args = append(args, mcppackage.UpgradeLegacyNodeRunnerArgs(cfg.Args)...)
	return args, nil
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

// shutdownGrace is how long a server subprocess is given to exit on its own
// after its stdin is closed, before it is signalled.
const shutdownGrace = 3 * time.Second

// close shuts the server subprocess down and reaps it (MU-017 criterion 7).
//
// The documented policy, in order:
//
//  1. Close stdin. An MCP server reads requests from stdin, so EOF is the
//     protocol's own "no more work" and a well-behaved server exits on it.
//  2. Wait up to shutdownGrace for that to happen.
//  3. SIGTERM, then a short grace, then SIGKILL. Terminate before kill so a
//     server that holds a lock, a partial file, or an upstream session gets
//     the chance to release it — a revoked extension should stop running, not
//     be made to corrupt something on the way out.
//  4. Wait in every path.
//
// Step 4 is the one that was missing. The old close() called Kill and
// returned, so every removed or hot-replaced server left a zombie until the
// gateway itself exited. Nothing visibly broke, which is why it survived: the
// process table just kept growing on a deployment that reconfigures servers.
func (t *stdioTx) close() error {
	t.closed.Store(true)
	_ = t.stdin.Close()
	if t.cmd == nil || t.cmd.Process == nil {
		return nil
	}

	exited := make(chan struct{})
	go func() {
		_ = t.cmd.Wait()
		close(exited)
	}()

	select {
	case <-exited:
		return nil
	case <-time.After(shutdownGrace):
	}

	_ = t.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-exited:
		return nil
	case <-time.After(shutdownGrace):
	}

	_ = t.cmd.Process.Kill()
	select {
	case <-exited:
	case <-time.After(shutdownGrace):
		// Unreapable after SIGKILL means the process is stuck in uninterruptible
		// sleep. Returning is right: blocking the caller forever would turn one
		// wedged extension into a wedged gateway.
		t.log.Warn("mcp: server subprocess did not exit after SIGKILL",
			zap.Int("pid", t.cmd.Process.Pid))
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
	url          string
	headers      map[string]string
	client       *http.Client
	publicRemote bool
	nextID       atomic.Int64
	sessionID    atomic.Pointer[string]
}

func newHTTP(cfg ServerConfig) *httpTx {
	client := &http.Client{Timeout: 60 * time.Second}
	if cfg.PublicRemote {
		client = netguard.NewPublicHTTPClient(60 * time.Second)
	}
	return &httpTx{
		url:          cfg.URL,
		headers:      cfg.Headers,
		client:       client,
		publicRemote: cfg.PublicRemote,
	}
}

func (t *httpTx) validateURL() error {
	if t.url == "" {
		return fmt.Errorf("http: url is required")
	}
	if !t.publicRemote {
		return nil
	}
	u, err := url.Parse(t.url)
	if err != nil || !strings.EqualFold(u.Scheme, "https") || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return fmt.Errorf("http: workspace MCP URL must be an absolute HTTPS URL")
	}
	return nil
}

func (t *httpTx) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	if err := t.validateURL(); err != nil {
		return nil, err
	}
	id := t.nextID.Add(1)
	payload, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": method, "params": params,
	})
	req, err := http.NewRequestWithContext(ctx, "POST", t.url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if sid := t.sessionID.Load(); sid != nil {
		req.Header.Set("Mcp-Session-Id", *sid)
	}
	for k, v := range t.headers {
		req.Header.Set(k, v)
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
	if err := t.validateURL(); err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "method": method, "params": params,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", t.url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if sid := t.sessionID.Load(); sid != nil {
		req.Header.Set("Mcp-Session-Id", *sid)
	}
	for k, v := range t.headers {
		req.Header.Set(k, v)
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func (t *httpTx) close() error { return nil }
