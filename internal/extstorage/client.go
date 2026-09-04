// Package extstorage is the host side of the External Storage Protocol
// (Story E24): it spawns storage sidecars, negotiates the contract, and
// adapts them to the sdk vector / queue backend interfaces so flow config
// can select third-party database drivers without recompiling.
//
// Process lifecycle is deliberately simpler than the channel supervisor
// (E4): a storage sidecar that exits fails subsequent calls with a clear
// error and surfaces through Done(); automatic respawn (with re-subscribe
// replay) is a documented follow-up. See docs/EXTERNAL_STORAGE_PROTOCOL.md.
package extstorage

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	sdkext "github.com/soulacy/soulacy/sdk/extstorage"
)

const defaultHandshakeTimeout = 5 * time.Second

// ClientConfig describes one storage sidecar.
type ClientConfig struct {
	// Name labels the sidecar in errors/logs (e.g. the config key).
	Name string
	// Command + Args spawn the sidecar process.
	Command string
	Args    []string
	// ScratchRoot hosts the per-run shared directory (workspace data dir;
	// empty = a soulacy-scratch dir under os.TempDir, tests only).
	ScratchRoot string
	// HandshakeTimeout bounds the negotiate round-trip (default 5s).
	HandshakeTimeout time.Duration
	// Log defaults to zap.NewNop().
	Log *zap.Logger
}

// Client is a negotiated JSON-RPC session with one storage sidecar.
type Client struct {
	cfg ClientConfig

	cmd     *exec.Cmd
	stdin   io.WriteCloser
	scratch string
	cleanup func()
	neg     sdkext.NegotiateResult

	mu       sync.Mutex
	nextID   int64
	pending  map[int64]chan sdkext.Message
	notifyFn func(method string, params json.RawMessage)
	closed   bool

	exited   chan struct{}
	exitOnce sync.Once
}

// NewClient prepares a client; call Start to spawn and negotiate.
func NewClient(cfg ClientConfig) *Client {
	if cfg.Log == nil {
		cfg.Log = zap.NewNop()
	}
	if cfg.HandshakeTimeout <= 0 {
		cfg.HandshakeTimeout = defaultHandshakeTimeout
	}
	if cfg.Name == "" {
		cfg.Name = "storage-sidecar"
	}
	return &Client{
		cfg:     cfg,
		pending: map[int64]chan sdkext.Message{},
		exited:  make(chan struct{}),
	}
}

// OnNotification registers the handler for sidecar→host notifications
// (queue.message deliveries). Must be called before Start.
func (c *Client) OnNotification(fn func(method string, params json.RawMessage)) {
	c.mu.Lock()
	c.notifyFn = fn
	c.mu.Unlock()
}

// SharedDir returns the absolute per-run scratch directory (after Start).
func (c *Client) SharedDir() string { return c.scratch }

// Negotiated returns the sidecar's negotiate result (after Start).
func (c *Client) Negotiated() sdkext.NegotiateResult { return c.neg }

// Done is closed when the sidecar process exits.
func (c *Client) Done() <-chan struct{} { return c.exited }

// Start spawns the sidecar, creates the shared scratch dir, and runs the
// negotiate handshake. On any failure the process is reaped and the
// scratch dir removed.
func (c *Client) Start(ctx context.Context) error {
	scratch, cleanup, err := NewScratchDir(c.cfg.ScratchRoot, c.cfg.Name)
	if err != nil {
		return err
	}
	c.scratch = scratch
	c.cleanup = cleanup

	cmd := exec.Command(c.cfg.Command, c.cfg.Args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cleanup()
		return fmt.Errorf("extstorage: %s: stdin pipe: %w", c.cfg.Name, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cleanup()
		return fmt.Errorf("extstorage: %s: stdout pipe: %w", c.cfg.Name, err)
	}
	if err := cmd.Start(); err != nil {
		cleanup()
		return fmt.Errorf("extstorage: %s: start %q: %w", c.cfg.Name, c.cfg.Command, err)
	}
	c.cmd = cmd
	c.stdin = stdin

	go c.readLoop(stdout)
	go func() {
		_ = cmd.Wait()
		c.exitOnce.Do(func() { close(c.exited) })
		c.failAllPending("sidecar exited")
	}()

	hctx, hcancel := context.WithTimeout(ctx, c.cfg.HandshakeTimeout)
	defer hcancel()
	var neg sdkext.NegotiateResult
	err = c.Call(hctx, sdkext.MethodNegotiate, sdkext.NegotiateParams{
		Protocol:  sdkext.ProtocolVersion,
		Name:      "soulacy-gateway",
		SharedDir: scratch,
	}, &neg)
	if err == nil && (neg.Protocol < 1 || neg.Protocol > sdkext.ProtocolVersion) {
		err = fmt.Errorf("extstorage: negotiated protocol %d outside 1..%d", neg.Protocol, sdkext.ProtocolVersion)
	}
	if err == nil && neg.SharedDir != scratch {
		err = fmt.Errorf("extstorage: sidecar did not echo shared dir (got %q)", neg.SharedDir)
	}
	if err != nil {
		// A sidecar that failed negotiation gets no shutdown grace.
		if c.cmd.Process != nil {
			_ = c.cmd.Process.Kill()
		}
		_ = c.Close()
		return fmt.Errorf("extstorage: %s: negotiate: %w", c.cfg.Name, err)
	}
	c.neg = neg
	c.cfg.Log.Info("storage sidecar negotiated",
		zap.String("sidecar", c.cfg.Name),
		zap.String("impl", neg.Name),
		zap.Int("protocol", neg.Protocol),
		zap.Strings("capabilities", neg.Capabilities),
		zap.String("shared_dir", scratch))
	return nil
}

func (c *Client) readLoop(stdout io.Reader) {
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 4096), 8*1024*1024)
	for sc.Scan() {
		m, err := sdkext.ParseMessage(sc.Bytes())
		if err != nil {
			continue // malformed lines never kill the session
		}
		switch {
		case m.IsResponse():
			c.mu.Lock()
			ch := c.pending[*m.ID]
			delete(c.pending, *m.ID)
			c.mu.Unlock()
			if ch != nil {
				ch <- m
			}
		case m.IsNotification():
			c.mu.Lock()
			fn := c.notifyFn
			c.mu.Unlock()
			if fn != nil {
				fn(m.Method, m.Params)
			}
			// Unknown notifications without a handler are skipped.
		}
	}
}

func (c *Client) failAllPending(reason string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, ch := range c.pending {
		delete(c.pending, id)
		ch <- sdkext.NewErrorResponse(id, sdkext.CodeInternalError, reason)
	}
}

// Call performs one JSON-RPC request/response round-trip. result may be
// nil to discard the payload. Sidecar-side errors come back as *sdkext.Error.
func (c *Client) Call(ctx context.Context, method string, params, result any) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return fmt.Errorf("extstorage: %s: client closed", c.cfg.Name)
	}
	c.nextID++
	id := c.nextID
	ch := make(chan sdkext.Message, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	req, err := sdkext.NewRequest(id, method, params)
	if err != nil {
		c.dropPending(id)
		return err
	}
	c.mu.Lock()
	werr := sdkext.WriteMessage(c.stdin, req)
	c.mu.Unlock()
	if werr != nil {
		c.dropPending(id)
		return fmt.Errorf("extstorage: %s: write %s: %w", c.cfg.Name, method, werr)
	}

	select {
	case m := <-ch:
		if m.Error != nil {
			return fmt.Errorf("extstorage: %s: %s: %w", c.cfg.Name, method, m.Error)
		}
		if result != nil {
			if err := json.Unmarshal(m.Result, result); err != nil {
				return fmt.Errorf("extstorage: %s: %s result: %w", c.cfg.Name, method, err)
			}
		}
		return nil
	case <-c.exited:
		c.dropPending(id)
		return fmt.Errorf("extstorage: %s: sidecar exited during %s", c.cfg.Name, method)
	case <-ctx.Done():
		c.dropPending(id)
		return fmt.Errorf("extstorage: %s: %s: %w", c.cfg.Name, method, ctx.Err())
	}
}

func (c *Client) dropPending(id int64) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

// Close asks the sidecar to shut down (3s grace, then kill), reaps the
// process, and removes the scratch directory. Idempotent.
func (c *Client) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	stdin := c.stdin
	c.mu.Unlock()

	if stdin != nil {
		c.mu.Lock()
		c.nextID++
		req, _ := sdkext.NewRequest(c.nextID, sdkext.MethodShutdown, struct{}{})
		_ = sdkext.WriteMessage(stdin, req)
		c.mu.Unlock()
		_ = stdin.Close()
	}
	if c.cmd != nil && c.cmd.Process != nil {
		select {
		case <-c.exited:
		case <-time.After(3 * time.Second):
			_ = c.cmd.Process.Kill()
			<-c.exited
		}
	}
	if c.cleanup != nil {
		c.cleanup()
	}
	return nil
}

// WriteScratchFile writes content to a new file in the scratch directory
// and returns the relative path to it.
func (c *Client) WriteScratchFile(prefix string, content string) (string, error) {
	c.mu.Lock()
	scratch := c.scratch
	c.mu.Unlock()
	if scratch == "" {
		return "", fmt.Errorf("extstorage: scratch directory not initialized")
	}
	var rnd [8]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return "", fmt.Errorf("extstorage: failed to generate random bytes: %w", err)
	}
	relPath := fmt.Sprintf("%s-%s.txt", prefix, hex.EncodeToString(rnd[:]))
	fullPath := filepath.Join(scratch, relPath)
	if err := os.WriteFile(fullPath, []byte(content), 0o600); err != nil {
		return "", fmt.Errorf("extstorage: write scratch file: %w", err)
	}
	return relPath, nil
}

// ReadScratchFile reads content from a relative path in the scratch directory.
func (c *Client) ReadScratchFile(relPath string) (string, error) {
	c.mu.Lock()
	scratch := c.scratch
	c.mu.Unlock()
	if scratch == "" {
		return "", fmt.Errorf("extstorage: scratch directory not initialized")
	}
	// Prevent directory traversal. The separator on the prefix is the whole
	// point: without it, "/…/scratch-abc" is a prefix of "/…/scratch-abcdef", so
	// a sibling directory whose name merely EXTENDS the scratch dir's passes the
	// check. plugininstall/archive.go and gateway/plugins.go both terminate the
	// prefix for this reason; this one did not.
	base := filepath.Clean(scratch)
	path := filepath.Clean(filepath.Join(base, relPath))
	if path != base && !strings.HasPrefix(path, base+string(os.PathSeparator)) {
		return "", fmt.Errorf("extstorage: content file escapes scratch directory")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("extstorage: read scratch file: %w", err)
	}
	return string(b), nil
}
