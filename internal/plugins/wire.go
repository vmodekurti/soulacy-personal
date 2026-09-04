// wire.go connects loaded manifest-v2 plugin contributions to the host
// (Story E7): sidecar channels become supervised external adapters in the
// channel registry, OpenAI-compatible providers join the LLM router, and
// capability sets register with the enforcer. This makes the long-declared
// pkg/plugin.Registry contract real — hostRegistry below implements it.
//
// Wiring is best-effort per contribution: one broken plugin must never take
// the gateway down. Errors are collected and returned for logging.
package plugins

import (
	"context"
	"fmt"
	"os"
	"time"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/caps"
	"github.com/soulacy/soulacy/internal/channels"
	"github.com/soulacy/soulacy/internal/channels/external"
	"github.com/soulacy/soulacy/internal/credentials"
	"github.com/soulacy/soulacy/internal/extstorage"
	"github.com/soulacy/soulacy/internal/llm"
	"github.com/soulacy/soulacy/internal/sandbox"
	"github.com/soulacy/soulacy/pkg/plugin"
)

// ChannelRegistrar is the slice of channels.Registry that Wire needs.
type ChannelRegistrar interface {
	Register(a channels.Adapter)
}

// ProviderRegistrar is the slice of llm.Router that Wire needs.
type ProviderRegistrar interface {
	Register(p llm.Provider)
}

// WireDeps carries the host subsystems plugin contributions plug into.
// Nil fields disable the corresponding contribution type (with an error
// when a plugin actually needs it).
type WireDeps struct {
	Channels ChannelRegistrar
	LLM      ProviderRegistrar
	Vault    credentials.Vault
	Enforcer *caps.Enforcer
	Log      *zap.Logger

	// Sandbox baseline for sidecars (same rlimit wrapper as E4).
	SandboxSelf   string
	SandboxLimits sandbox.Limits

	// WatchInterval is the credential-rotation poll cadence (default 30s).
	WatchInterval time.Duration

	// PluginsConfig is the parsed plugins_config block from config.yaml
	// (Story E17), keyed by plugin ID. Wire attaches each plugin's section
	// to its LoadedPlugin (Settings) so contributions and host surfaces
	// (E13 install UX) can read it; the shape is owned by the plugin.
	PluginsConfig map[string]map[string]any

	// ScratchRoot, when set, provisions a per-channel shared scratch
	// directory (Story E24 shared mounts) advertised to the sidecar in
	// hello_ack. Typically <workspace data>/scratch; the host sweeps it
	// at boot.
	ScratchRoot string
}

// Wire registers every v2 contribution from l with the host. Returned errors
// are per-contribution diagnostics; the gateway logs them and keeps going.
func Wire(ctx context.Context, l *Loader, deps WireDeps) []error {
	if deps.Log == nil {
		deps.Log = zap.NewNop()
	}
	reg := &hostRegistry{deps: deps, log: deps.Log}
	var errs []error
	for _, lp := range l.All() {
		// plugins_config sections attach to every loaded plugin, v1 and v2
		// alike (Story E17) — tools-only plugins have settings too.
		if section, ok := deps.PluginsConfig[lp.Manifest.ID]; ok {
			lp.Settings = section
		}
		if !lp.isV2() {
			continue
		}
		errs = append(errs, wirePlugin(ctx, lp, reg, deps)...)
	}
	return errs
}

func wirePlugin(ctx context.Context, lp *LoadedPlugin, reg *hostRegistry, deps WireDeps) []error {
	var errs []error
	id := lp.Manifest.ID

	// Capability set → enforcer (E5). The set was validated at load time.
	if deps.Enforcer != nil && lp.Caps != nil {
		deps.Enforcer.SetPluginSet(lp.Caps)
	}

	// Sidecar channels (E3/E4 runtime + E6 credentials).
	for _, ch := range lp.SidecarChannels() {
		ch := ch
		if deps.Channels == nil {
			continue
		}
		if len(lp.Manifest.Credentials) > 0 && deps.Vault == nil {
			errs = append(errs, fmt.Errorf("plugin %s channel %s: manifest declares credentials but no vault is available", id, ch.ID))
			continue
		}
		err := reg.RegisterChannel(ch.ID, func(map[string]any) (any, error) {
			return buildSupervisor(ctx, lp, ch, deps), nil
		})
		if err != nil {
			errs = append(errs, fmt.Errorf("plugin %s channel %s: %w", id, ch.ID, err))
		}
	}

	// OpenAI-compatible providers.
	for _, p := range lp.OpenAIProviders() {
		p := p
		if deps.LLM == nil {
			continue
		}
		err := reg.RegisterProvider(p.ID, func(map[string]any) (any, error) {
			spec := p.OpenAICompatible
			apiKey := ""
			if spec.APIKeyEnv != "" {
				apiKey = os.Getenv(spec.APIKeyEnv)
			}
			return llm.NewOpenAIProvider(p.ID, spec.BaseURL, apiKey, spec.Model), nil
		})
		if err != nil {
			errs = append(errs, fmt.Errorf("plugin %s provider %s: %w", id, p.ID, err))
		}
	}

	return errs
}

// buildSupervisor assembles the supervised sidecar adapter for one manifest
// channel: per-spawn credential env resolution (E6) plus rotation watch →
// restart.
func buildSupervisor(ctx context.Context, lp *LoadedPlugin, ch plugin.ChannelEntry, deps WireDeps) *external.Supervisor {
	id := lp.Manifest.ID
	cfg := external.SupervisorConfig{
		SandboxSelf:   deps.SandboxSelf,
		SandboxLimits: deps.SandboxLimits,
	}
	if deps.ScratchRoot != "" {
		if dir, _, err := extstorage.NewScratchDir(deps.ScratchRoot, id+"-"+ch.ID); err != nil {
			deps.Log.Warn("shared scratch dir unavailable for sidecar channel",
				zap.String("plugin", id), zap.String("channel", ch.ID), zap.Error(err))
		} else {
			cfg.SharedDir = dir
		}
	}
	if len(lp.Manifest.Credentials) > 0 && deps.Vault != nil {
		delegator := NewDelegator(deps.Vault, deps.Log)
		refs := lp.Manifest.Credentials
		cfg.Env = func() ([]string, error) {
			return delegator.Env(ctx, id, refs)
		}
	}
	sup := external.NewSupervisor(ch.ID, ch.Sidecar.Command, ch.Sidecar.Args,
		ch.AgentID, channels.ActivationPolicy{}, deps.Log, cfg)

	if len(lp.Manifest.Credentials) > 0 && deps.Vault != nil {
		WatchCredentials(ctx, deps.Vault, id, lp.Manifest.Credentials,
			deps.WatchInterval, deps.Log, func() {
				sup.Restart("credential rotated")
			})
	}
	return sup
}

// ---------------------------------------------------------------------------
// hostRegistry — the concrete pkg/plugin.Registry
// ---------------------------------------------------------------------------

// hostRegistry implements pkg/plugin.Registry against the live host
// subsystems. Factories are invoked immediately (manifest contributions are
// fully specified up front); the indirection keeps the public contract that
// Go-native plugins (E9+) will call.
type hostRegistry struct {
	deps WireDeps
	log  *zap.Logger
}

var _ plugin.Registry = (*hostRegistry)(nil)

func (h *hostRegistry) RegisterChannel(id string, factory plugin.ChannelFactory) error {
	if h.deps.Channels == nil {
		return fmt.Errorf("channel registry unavailable")
	}
	v, err := factory(nil)
	if err != nil {
		return fmt.Errorf("channel factory: %w", err)
	}
	a, ok := v.(channels.Adapter)
	if !ok {
		return fmt.Errorf("channel factory for %q returned %T, not a channels.Adapter", id, v)
	}
	h.deps.Channels.Register(a)
	h.log.Info("plugins: sidecar channel registered", zap.String("channel", id))
	return nil
}

func (h *hostRegistry) RegisterProvider(id string, factory plugin.ProviderFactory) error {
	if h.deps.LLM == nil {
		return fmt.Errorf("llm router unavailable")
	}
	v, err := factory(nil)
	if err != nil {
		return fmt.Errorf("provider factory: %w", err)
	}
	p, ok := v.(llm.Provider)
	if !ok {
		return fmt.Errorf("provider factory for %q returned %T, not an llm.Provider", id, v)
	}
	h.deps.LLM.Register(p)
	h.log.Info("plugins: provider registered", zap.String("provider", id))
	return nil
}

func (h *hostRegistry) RegisterToolLibrary(id string, lib plugin.ToolLibrary) error {
	// Python tool libraries flow through the loader's existing catalog
	// (AllTools); a Go-native ToolLibrary registration path arrives with the
	// SDK work (E9/E10).
	return fmt.Errorf("tool library registration not supported yet (tools load via the manifest tools list)")
}
