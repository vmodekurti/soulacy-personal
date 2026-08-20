package gateway

import (
	"context"
	"time"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"
)

// channelhot.go — the gateway's half of a live channel change.
//
// The applier is supplied by the app, because building a channel adapter needs
// the agent loader, the workspace paths and the capability-tier binding gate —
// things the gateway does not hold and should not learn to. The gateway knows
// WHEN a channel changed; the app knows HOW to make that real.
//
// nil applier keeps the previous behaviour, which is what a directly
// constructed test gateway and any embedding that never built an App have. It
// is reported to the caller rather than silently ignored: "saved, and it will
// take effect on restart" and "saved, and it is live now" are different
// answers, and a handler that gave the second when it meant the first would be
// worse than the honest restart note this replaces.
type channelApplier func(ctx context.Context, channelID string, previous, next map[string]any) error

// SetChannelApplier installs the live-apply path for channel configuration.
func (s *Server) SetChannelApplier(apply channelApplier) {
	if s == nil {
		return
	}
	s.applyChannel = apply
}

// applyChannelLive brings the running adapters in line with a saved channel
// config, and reports what the caller should tell the operator.
//
// Returns the message for the response body rather than an error, because a
// channel that saved but did not connect is not a failed request: the config
// is written, the old adapter is gone, and re-issuing the save would not help.
// The operator needs to know the state, not to retry.
func (s *Server) applyChannelLive(c *fiber.Ctx, channelID string, previous, next map[string]any) string {
	if s == nil || s.applyChannel == nil {
		// No applier means no App built this gateway — a directly constructed
		// test server or an embedding. Said plainly rather than as advice,
		// because "restart to apply" would be advice that does not help: a
		// process with no channel wiring will not have any after a restart
		// either.
		return "Saved to the configuration file. This gateway has no live channel wiring, so nothing was connected."
	}
	// detachedRequestContext, not c.Context(): starting an adapter outlives the
	// HTTP request that asked for it — a Slack websocket handshake takes longer
	// than the response should — and Fiber recycles the request context the
	// moment the handler returns. Cancelling the connect at that instant would
	// make every save look like a connection failure.
	ctx, cancel := context.WithTimeout(detachedRequestContext(c), channelApplyTimeout)
	defer cancel()

	if err := s.applyChannel(ctx, channelID, previous, next); err != nil {
		s.logger().Warn("channel configuration saved but not fully applied",
			zap.String("channel", channelID), zap.Error(err))
		// The error text names which adapters did not connect and why. Given
		// to the operator verbatim because the alternative — "saved, some
		// adapters failed" — sends them to the logs for information the
		// response already had.
		return "Saved and applied. " + err.Error()
	}
	return "Saved and applied. No restart needed."
}

// channelApplyTimeout bounds a live channel apply.
//
// Generous, because it covers a real network handshake against somebody else's
// service — Slack's socket-mode connect, Telegram's getMe — and a timeout
// shorter than the provider's own would report a failure the provider was
// about to resolve. Bounded at all because an adapter that never connects must
// not hold the request goroutine for the life of the process.
const channelApplyTimeout = 45 * time.Second
