package app

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/channels"
	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/runtime"
)

// channelhot.go — applying a channel change without restarting the gateway.
//
// WHAT THE SAVE PATH USED TO DO. handleUpdateChannel wrote the YAML, copied the
// new settings into the in-memory config so the list endpoint would not look
// stale, and stopped. Nothing reached the live registry. The response said
// "Restart the gateway to connect/disconnect adapters", and that was true —
// the code had simply given up half-way, and the message described the
// consequence rather than a limitation.
//
// WHY IT MATTERED MORE THAN IT LOOKED. A channel is a Slack bot, a Telegram
// token, an inbox — the most tenant-shaped setting in the product. In a
// Personal deployment the operator and the tenant are the same person, so
// "save and restart" cost ten seconds of their own time. In a Team deployment
// one workspace connecting a bot would be asking every other workspace to
// accept a service interruption.
//
// HALF THE MECHANISM ALREADY EXISTED. Registry.StartAdapter stops the old
// adapter, swaps the entry and starts the new one, and the WhatsApp pairing
// flow has used it live for some time. What was missing was StopAdapter — and
// a save path can only be hot if BOTH directions are, because an operator who
// can connect live but must restart to disconnect still has a restart in their
// workflow.

// applyChannelLive brings the running registry in line with one channel's
// configuration, connecting, reconfiguring or disconnecting as the config says.
//
// ctx bounds the adapters' Start; a channel that cannot connect is reported and
// does not prevent the others from being applied. A partially applied save is
// better than an abandoned one: the alternative leaves the operator with a
// config file and a process that disagree, and no way to tell which channels
// took effect.
func (a *App) applyChannelLive(ctx context.Context, chanReg *channels.Registry, loader *runtime.Loader,
	ws config.Paths, channelID string, previous, next map[string]any) error {
	if chanReg == nil {
		return fmt.Errorf("channels: no registry is available to apply %q", channelID)
	}
	channelID = strings.TrimSpace(channelID)
	if channelID == "" {
		return fmt.Errorf("channels: a channel id is required")
	}

	// The OLD config decides what to stop. Asking the registry for adapters
	// whose ID starts with the channel name would be a naming convention, and
	// the derived IDs are the construction path's business — a `bots:` list
	// becomes several adapters, and the rule for their IDs differs per channel
	// type. Building from the previous config reproduces exactly what boot
	// produced, without either side having to remember a convention.
	for _, id := range a.channelAdapterIDs(loader, ws, channelID, previous) {
		stopped, err := chanReg.StopAdapter(id)
		if err != nil {
			// Already removed from the registry, so the disconnect HAS taken
			// effect. Logged so an operator who sees a lingering session in
			// Slack's dashboard knows why.
			a.log.Warn("channel adapter complained while being stopped; it is no longer registered",
				zap.String("adapter_id", id), zap.Error(err))
		} else if stopped {
			a.log.Info("channel adapter stopped", zap.String("adapter_id", id))
		}
	}

	enabled, _ := next["enabled"].(bool)
	if !enabled {
		// Disabling is complete once the old adapters are gone. Building from
		// a disabled config would produce nothing anyway; returning here makes
		// that explicit rather than incidental.
		return nil
	}

	var failures []string
	for _, adapter := range a.buildChannelAdapters(loader, ws, channelID, next) {
		if err := chanReg.StartAdapter(ctx, adapter); err != nil {
			failures = append(failures, adapter.ID()+": "+err.Error())
			a.log.Warn("channel adapter could not be started",
				zap.String("adapter_id", adapter.ID()), zap.Error(err))
			continue
		}
		a.log.Info("channel adapter started", zap.String("adapter_id", adapter.ID()))
	}
	if len(failures) > 0 {
		sort.Strings(failures)
		return fmt.Errorf("channels: %s saved, but %d adapter(s) did not connect: %s",
			channelID, len(failures), strings.Join(failures, "; "))
	}
	return nil
}

// buildChannelAdapters constructs, without starting, the adapters one channel's
// configuration produces.
//
// Built by running the REAL registration path against a scratch registry rather
// than by reimplementing it. registerChannels holds every per-channel rule
// there is — multi-bot expansion, adapter ID derivation, the capability-tier
// binding gate, the outbound-only cases — and a second implementation for the
// hot path would drift from it silently, in the direction of a channel that
// behaves differently after a save than after a restart. That is the worst
// possible divergence, because it only appears once somebody restarts.
func (a *App) buildChannelAdapters(loader *runtime.Loader, ws config.Paths,
	channelID string, cfg map[string]any) []channels.Adapter {
	if len(cfg) == 0 {
		return nil
	}
	scratch := channels.NewRegistry(1)
	scratch.SetLogger(a.log)
	// A one-entry map, so the shared registration path considers exactly this
	// channel. Nothing is started: Register only records, and the scratch
	// registry is discarded.
	a.registerChannels(map[string]map[string]any{channelID: cfg}, scratch, loader, ws)
	return scratch.Adapters()
}

// channelAdapterIDs is buildChannelAdapters reduced to the IDs, for the stop
// direction where the adapters themselves are not wanted.
func (a *App) channelAdapterIDs(loader *runtime.Loader, ws config.Paths,
	channelID string, cfg map[string]any) []string {
	adapters := a.buildChannelAdapters(loader, ws, channelID, cfg)
	out := make([]string, 0, len(adapters))
	for _, adapter := range adapters {
		out = append(out, adapter.ID())
	}
	sort.Strings(out)
	return out
}
