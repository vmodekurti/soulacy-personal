// Package app is the Soulacy composition root (Story E10 part 3).
//
// app.New(cfg, opts...) validates the configuration, applies the security
// guardrails, and builds the process logger; App.Run(ctx) wires every
// subsystem (storage, LLM router, engine, scheduler, channels, gateway) and
// blocks until the gateway exits. cmd/soulacy/main.go is a thin shell:
// sandbox re-exec intercept → config load → app.New → app.Run.
//
// Embedders (E12 flavored binaries, custom distributions) can construct the
// same stack programmatically:
//
//	cfg, path, _ := config.Load("")
//	a, err := app.New(cfg, app.WithConfigPath(path))
//	if err != nil { ... }
//	err = a.Run(ctx)
package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/costs"
	"github.com/soulacy/soulacy/internal/mcp"
	"github.com/soulacy/soulacy/internal/secrets"
	"github.com/soulacy/soulacy/pkg/edition"
)

// App owns the fully-wired Soulacy gateway process.
type App struct {
	cfg     *config.Config
	cfgPath string
	log     *zap.Logger
	edition edition.Descriptor
	// costGovernor is retained from subsystem wiring so the gateway can
	// reinstall a recomposed quota policy when a workspace changes its own
	// limits — without it, a per-workspace budget would take effect only on
	// the next restart, which is not a limit anybody can rely on.
	costGovernor *costs.Governor
	// mcpPool is retained so the gateway can route MCP reads and mutations to
	// the requesting workspace's own servers rather than a deployment-wide
	// client.
	mcpPool *mcp.Pool
	// workerIDValue identifies this process when it claims a durable run's
	// lease. See workerID.
	workerIDOnce  sync.Once
	workerIDValue string
	// secretsManager re-overlays vault-backed config values after a reload.
	// See wireSecrets.
	secretsManager *secrets.Manager
}

// Option customises App construction.
type Option func(*App)

// WithConfigPath records where the config was loaded from so the gateway's
// config-editing API can write back to the right file.
func WithConfigPath(p string) Option {
	return func(a *App) { a.cfgPath = p }
}

// WithLogger replaces the config-derived logger (tests, embedders).
func WithLogger(log *zap.Logger) Option {
	return func(a *App) { a.log = log }
}

// WithEdition supplies the descriptor registered by a distribution's
// composition root. Commercial binaries use this seam to add capabilities
// without making the open-source Personal module import commercial code.
func WithEdition(descriptor edition.Descriptor) Option {
	return func(a *App) { a.edition = descriptor }
}

// New validates cfg, prints the security guardrail warning when binding
// non-loopback without an API key, and builds the process logger.
func New(cfg *config.Config, opts ...Option) (*App, error) {
	if cfg == nil {
		return nil, fmt.Errorf("app: nil config")
	}
	a := &App{cfg: cfg}
	for _, opt := range opts {
		opt(a)
	}
	if a.edition.ID == "" {
		a.edition = edition.PersonalDescriptor()
	}
	if err := a.edition.Validate(); err != nil {
		return nil, fmt.Errorf("app: invalid edition: %w", err)
	}
	if string(a.edition.ID) != cfg.DeploymentMode() {
		return nil, fmt.Errorf("app: edition %q does not match deployment mode %q", a.edition.ID, cfg.DeploymentMode())
	}

	// ── Security guardrail ──────────────────────────────────────────────
	// Warn-only (not fatal): binding 0.0.0.0 without an API key is a
	// legitimate pattern behind an authenticating reverse proxy. The
	// warning is intentionally noisy so service-manager logs surface it.
	if cfg.Server.APIKey == "" && !isLoopbackHost(cfg.Server.Host) {
		fmt.Fprintf(os.Stderr,
			"\n⚠  SECURITY WARNING: server.host=%q is a non-loopback address with no server.api_key.\n"+
				"   All API endpoints are UNAUTHENTICATED. Set server.api_key in config.yaml\n"+
				"   unless a reverse proxy is enforcing authentication upstream.\n\n",
			cfg.Server.Host,
		)
	}

	if a.log == nil {
		log, err := buildLogger(cfg.Log)
		if err != nil {
			return nil, fmt.Errorf("build logger: %w", err)
		}
		a.log = log
	}
	return a, nil
}

// Logger exposes the process logger (embedders, tests).
func (a *App) Logger() *zap.Logger { return a.log }

// Edition exposes the active immutable descriptor to embedders and subsystem
// wiring. The capability slice is copied so callers cannot mutate App state.
func (a *App) Edition() edition.Descriptor {
	descriptor := a.edition
	descriptor.Capabilities = append([]edition.Capability(nil), descriptor.Capabilities...)
	return descriptor
}

// isLoopbackHost returns true if host is a well-known loopback address
// (127.0.0.0/8, ::1, localhost). Gates the empty-API-key guardrail.
func isLoopbackHost(host string) bool {
	host = strings.TrimSpace(host)
	if host == "" || host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return true
	}
	// 127.0.0.0/8 is loopback per RFC 1122.
	return strings.HasPrefix(host, "127.")
}

func buildLogger(cfg config.LogConfig) (*zap.Logger, error) {
	var zcfg zap.Config
	if cfg.Format == "json" {
		zcfg = zap.NewProductionConfig()
	} else {
		// Console mode: human-readable but no stack traces on WARN.
		// Stack traces only appear on ERROR and above.
		zcfg = zap.NewDevelopmentConfig()
		zcfg.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
		zcfg.DisableStacktrace = false // keep for ERROR
		zcfg.Development = false       // disables panic-on-DPanic and WARN stack traces
	}

	level := zap.NewAtomicLevel()
	if err := level.UnmarshalText([]byte(cfg.Level)); err != nil {
		level.SetLevel(zap.InfoLevel)
	}
	zcfg.Level = level

	if cfg.File != "" {
		// The logger is built before EnsureDirs runs, so make sure the log
		// directory exists; if we can't, fall back to stdout-only rather
		// than failing startup.
		if err := os.MkdirAll(filepath.Dir(cfg.File), 0o755); err == nil {
			zcfg.OutputPaths = append(zcfg.OutputPaths, cfg.File)
		} else {
			fmt.Fprintf(os.Stderr, "⚠ log file dir unavailable (%v) — logging to stdout only\n", err)
		}
	}

	return zcfg.Build(zap.WithCaller(true), zap.AddStacktrace(zap.ErrorLevel))
}
