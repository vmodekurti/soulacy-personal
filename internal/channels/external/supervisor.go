package external

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/channels"
	"github.com/soulacy/soulacy/internal/sandbox"
	"github.com/soulacy/soulacy/pkg/message"
)

// SupervisorConfig tunes restart behaviour and the sandbox baseline.
// Zero values get production defaults.
type SupervisorConfig struct {
	// MinBackoff is the first restart delay; it doubles per consecutive
	// crash up to MaxBackoff (±10% jitter). Defaults: 1s / 60s.
	MinBackoff time.Duration
	MaxBackoff time.Duration

	// HealthyReset: a sidecar that stayed up at least this long has its
	// crash counter reset, so one bad deploy doesn't penalise the next
	// month of stability. Default: 10 minutes.
	HealthyReset time.Duration

	// SandboxSelf + SandboxLimits spawn the sidecar through the portable
	// rlimit __exec-sandbox wrapper (sandbox.Wrap). SandboxSelf is the
	// soulacy binary path (os.Executable()); empty disables wrapping.
	SandboxSelf   string
	SandboxLimits sandbox.Limits

	// Env, when set, resolves the sidecar's COMPLETE environment before
	// every spawn (credential delegation, E6). Re-running it per spawn means
	// a restart — including the rotation-triggered Restart() — picks up
	// fresh secrets. An error blocks the spawn and is retried through the
	// normal crash/backoff loop. nil inherits the parent environment.
	Env func() ([]string, error)

	// SharedDir, when set, is advertised to every spawn in hello_ack
	// (Story E24 shared mounts): the absolute per-run scratch directory
	// for file-based attachment transport. Survives restarts — the same
	// dir is re-advertised to the respawned sidecar.
	SharedDir string
}

func (c *SupervisorConfig) defaults() {
	if c.MinBackoff <= 0 {
		c.MinBackoff = time.Second
	}
	if c.MaxBackoff <= 0 {
		c.MaxBackoff = time.Minute
	}
	if c.HealthyReset <= 0 {
		c.HealthyReset = 10 * time.Minute
	}
}

// Supervisor wraps the external-channel Adapter with a crash-restart
// lifecycle (story E4). It satisfies channels.Adapter itself, so the
// registry and Channels GUI need no changes — lifecycle state surfaces
// through AdapterStatus.Detail.
type Supervisor struct {
	id         string
	command    string
	args       []string
	agentID    string
	activation channels.ActivationPolicy
	log        *zap.Logger
	cfg        SupervisorConfig

	// handshakeTimeout propagates to each spawned adapter (test override).
	handshakeTimeout time.Duration

	mu        sync.Mutex
	current   *Adapter
	inbox     chan<- message.Message
	restarts  int
	inBackoff bool
	detail    string
	stopped   bool
	stop      chan struct{}
	stopOnce  sync.Once
}

// NewSupervisor builds a supervised external channel. Call Start to spawn.
func NewSupervisor(id, command string, args []string, agentID string,
	activation channels.ActivationPolicy, log *zap.Logger, cfg SupervisorConfig) *Supervisor {
	if log == nil {
		log = zap.NewNop()
	}
	cfg.defaults()
	return &Supervisor{
		id:               id,
		command:          command,
		args:             args,
		agentID:          agentID,
		activation:       activation,
		log:              log.Named("supervisor." + id),
		cfg:              cfg,
		handshakeTimeout: defaultHandshakeTimeout,
		stop:             make(chan struct{}),
	}
}

func (s *Supervisor) ID() string { return s.id }

func (s *Supervisor) Name() string {
	s.mu.Lock()
	cur := s.current
	s.mu.Unlock()
	if cur != nil {
		return cur.Name()
	}
	return "External channel (" + s.id + ")"
}

// buildCommand applies the sandbox wrapper when configured.
func (s *Supervisor) buildCommand() (string, []string) {
	if s.cfg.SandboxSelf == "" {
		return s.command, s.args
	}
	wrapped := sandbox.Wrap(s.cfg.SandboxSelf, s.cfg.SandboxLimits,
		append([]string{s.command}, s.args...))
	return wrapped[0], wrapped[1:]
}

func (s *Supervisor) newAdapter() (*Adapter, error) {
	command, args := s.buildCommand()
	a := New(s.id, command, args, s.agentID, s.activation, s.log)
	a.handshakeTimeout = s.handshakeTimeout
	if s.cfg.SharedDir != "" {
		a.SetSharedDir(s.cfg.SharedDir)
	}
	if s.cfg.Env != nil {
		env, err := s.cfg.Env()
		if err != nil {
			return nil, fmt.Errorf("external: resolve sidecar env: %w", err)
		}
		a.SetEnv(env)
	}
	return a, nil
}

// Start spawns the first sidecar and begins supervising.
func (s *Supervisor) Start(ctx context.Context, inbox chan<- message.Message) error {
	a, err := s.newAdapter()
	if err != nil {
		return err
	}
	if err := a.Start(ctx, inbox); err != nil {
		return err
	}
	s.mu.Lock()
	s.current = a
	s.inbox = inbox
	s.mu.Unlock()
	go s.supervise(ctx)
	return nil
}

// supervise watches the current sidecar and restarts it on crash with
// exponential backoff + jitter, resetting the attempt counter after a
// healthy run.
func (s *Supervisor) supervise(ctx context.Context) {
	for {
		s.mu.Lock()
		cur := s.current
		s.mu.Unlock()
		if cur == nil {
			return
		}
		started := time.Now()

		select {
		case <-s.stop:
			return
		case <-ctx.Done():
			return
		case <-cur.Done():
		}

		s.mu.Lock()
		if s.stopped {
			s.mu.Unlock()
			return
		}
		if time.Since(started) >= s.cfg.HealthyReset {
			s.restarts = 0
		}
		s.restarts++
		attempt := s.restarts
		delay := s.backoff(attempt)
		s.inBackoff = true
		s.detail = fmt.Sprintf("sidecar exited; restart #%d in %s", attempt, delay.Round(time.Millisecond))
		s.mu.Unlock()
		s.log.Warn("sidecar exited; scheduling restart",
			zap.Int("attempt", attempt), zap.Duration("backoff", delay))

		select {
		case <-s.stop:
			return
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}

		a, envErr := s.newAdapter()
		inboxErr := envErr
		if inboxErr == nil {
			inboxErr = func() error {
				s.mu.Lock()
				defer s.mu.Unlock()
				if s.stopped {
					return context.Canceled
				}
				return a.Start(ctx, s.inbox)
			}()
		}
		s.mu.Lock()
		s.inBackoff = false
		if inboxErr != nil {
			// Spawn (or env resolution) failure counts as an immediate
			// crash: loop again with a fake "already exited" adapter.
			s.detail = "restart failed: " + inboxErr.Error()
			s.mu.Unlock()
			if inboxErr == context.Canceled {
				return
			}
			// Synthesize a closed Done so the loop re-enters backoff.
			closed := make(chan struct{})
			close(closed)
			fake := New(s.id, s.command, s.args, s.agentID, s.activation, s.log)
			fake.exited = closed
			s.mu.Lock()
			s.current = fake
			s.mu.Unlock()
			continue
		}
		s.current = a
		s.mu.Unlock()
	}
}

// backoff: MinBackoff << (attempt-1), capped, ±10% jitter.
func (s *Supervisor) backoff(attempt int) time.Duration {
	d := s.cfg.MinBackoff
	for i := 1; i < attempt && d < s.cfg.MaxBackoff; i++ {
		d *= 2
	}
	if d > s.cfg.MaxBackoff {
		d = s.cfg.MaxBackoff
	}
	jitter := time.Duration(rand.Int63n(int64(d)/5 + 1)) //nolint:gosec // jitter
	return d - d/10 + jitter
}

// Restart stops the current sidecar so the supervision loop respawns it
// (with a fresh environment when cfg.Env is set). Used for credential
// rotation (E6): vault change → Restart → new secrets at next spawn.
// No-op when the supervisor is stopped or not started.
func (s *Supervisor) Restart(reason string) {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	cur := s.current
	if reason != "" {
		s.detail = "restarting: " + reason
	}
	s.mu.Unlock()
	if cur != nil {
		s.log.Info("sidecar restart requested", zap.String("reason", reason))
		_ = cur.Stop()
	}
}

// Restarts reports how many times the sidecar has been restarted since the
// last healthy reset.
func (s *Supervisor) Restarts() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.restarts
}

func (s *Supervisor) Send(ctx context.Context, msg message.Message) error {
	s.mu.Lock()
	cur := s.current
	backoff := s.inBackoff
	s.mu.Unlock()
	if cur == nil || backoff {
		return fmt.Errorf("external: %s is restarting; message not delivered", s.id)
	}
	return cur.Send(ctx, msg)
}

func (s *Supervisor) Stop() error {
	var err error
	s.stopOnce.Do(func() {
		s.mu.Lock()
		s.stopped = true
		cur := s.current
		s.mu.Unlock()
		close(s.stop)
		if cur != nil {
			err = cur.Stop()
		}
	})
	return err
}

func (s *Supervisor) Status() channels.AdapterStatus {
	s.mu.Lock()
	if s.inBackoff || s.stopped {
		st := channels.AdapterStatus{Connected: false, Detail: s.detail}
		if s.stopped {
			st.Detail = "stopped"
		}
		s.mu.Unlock()
		return st
	}
	cur := s.current
	s.mu.Unlock()
	if cur == nil {
		return channels.AdapterStatus{Connected: false, Detail: "not started"}
	}
	return cur.Status()
}
