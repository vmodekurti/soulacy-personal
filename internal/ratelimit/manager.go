package ratelimit

import (
	"fmt"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"
)

// ---------------------------------------------------------------------------
// Token quota — 24h sliding window, in-memory
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Manager
// ---------------------------------------------------------------------------

// Manager holds the rate-limit state and produces Fiber middleware.
type Manager struct {
	// cfgGuard makes cfg swappable at runtime — see reload.go. Embedded rather
	// than a bare mutex field so every reader is pushed through config().
	cfgGuard
	cfg     Config
	counter Counter
	log     *zap.Logger
	// A shared counter is a SaaS enforcement boundary. If it becomes
	// unavailable, allowing traffic would silently multiply every configured
	// limit by the replica count, so explicit Redis mode fails closed.
	failClosed bool

	// Per-user 24h token buckets. Key: JWT subject (or "anon" for open mode).

	// Per-agent 24h token buckets. Key: agentID.
}

// New creates a Manager from cfg. The Counter backend is selected from
// cfg.Backend ("memory" or "redis"). Explicit Redis mode fails startup rather
// than silently multiplying limits by the number of gateway replicas.
func New(cfg Config, log *zap.Logger) (*Manager, error) {
	var counter Counter
	var err error
	backend := strings.ToLower(strings.TrimSpace(cfg.Backend))
	cfg.Backend = backend

	switch backend {
	case "redis":
		if cfg.RedisURL == "" {
			return nil, fmt.Errorf("ratelimit: backend=redis but redis_url is empty")
		}
		counter, err = NewRedisCounter(cfg.RedisURL)
		if err != nil {
			return nil, err
		} else {
			log.Info("ratelimit: Redis counter ready", zap.String("url", cfg.RedisURL))
		}
	case "memory", "":
		counter = NewMemoryCounter()
		log.Info("ratelimit: in-memory counter ready")
	default:
		return nil, fmt.Errorf("ratelimit: unsupported backend %q", cfg.Backend)
	}

	m := &Manager{
		cfg: cfg, counter: counter, log: log,
		failClosed: backend == "redis",
	}
	return m, nil
}

// ---------------------------------------------------------------------------
// Daily token quotas live in internal/costs, not here
// ---------------------------------------------------------------------------
//
// This package used to carry a second implementation of `per_user_tokens_day`
// and `per_agent_tokens_day`: in-memory 24h buckets, a recorder, two hourly
// sweepers, and two middlewares. It has been removed, and the removal is the
// point of this comment, because the shape it left behind is easy to
// reintroduce.
//
// The buckets were INERT. Nothing anywhere called the recorders — not the
// engine, not the gateway — so every bucket was permanently zero and both
// middlewares compared zero against the limit and allowed the request. The
// gateway's own `rlTokenMW`/`rlAgentTokenMW` helpers had no callers either, so
// the middlewares were never even mounted. An operator setting a daily token
// quota got a configuration line, a status endpoint reporting `tokens_used: 0`
// forever, and no quota.
//
// Meanwhile the SAME two config keys are read by internal/costs, which
// enforces them properly: durably in SQLite so a restart does not hand
// everyone a fresh budget, scoped by workspace, and — decisively — through
// `TryReserve` rather than a read-then-allow. The difference matters at the
// only moment a quota is tested: N concurrent requests all read the same
// under-limit bucket value and all proceed, which is exactly what a
// reservation exists to prevent.
//
// So there is one mechanism now. What this package still owns is REQUEST-RATE
// limiting (per_user_rpm, per_agent_rpm), where an in-memory counter is the
// right tool because the window is a minute and losing it on restart costs
// nothing.
//
// The one hole that removal does not close is that a token quota is only
// enforced when `costs.enforcement_mode` is "hard". Config validation now
// refuses a quota set without it, rather than leaving an operator with the
// same silent nothing in a different package — see config.Validate.

// ---------------------------------------------------------------------------
// Middleware
// ---------------------------------------------------------------------------

// UserRPMMiddleware enforces PerUserRPM on every request.
// If claims are absent (open mode), the key "anon" is used so open-mode
// deployments still get a single shared bucket.
func (m *Manager) UserRPMMiddleware() fiber.Handler {
	cfg := m.config()
	if !cfg.Enabled || cfg.PerUserRPM <= 0 {
		return func(c *fiber.Ctx) error { return c.Next() }
	}
	limit := int64(cfg.PerUserRPM)
	return func(c *fiber.Ctx) error {
		key := userKey(c)
		count, err := m.counter.Increment(c.Context(), key, time.Minute)
		if err != nil {
			m.log.Warn("ratelimit: counter error", zap.Error(err))
			if m.failClosed {
				c.Set("Retry-After", "1")
				return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "rate limit service unavailable"})
			}
			return c.Next() // fail open
		}
		if count > limit {
			m.log.Info("ratelimit: user RPM exceeded",
				zap.String("key", key), zap.Int64("count", count), zap.Int64("limit", limit))
			return c.Status(fiber.StatusTooManyRequests).JSON(fiber.Map{
				"error":       "rate limit exceeded",
				"limit":       limit,
				"window":      "1m",
				"retry_after": "60",
			})
		}
		return c.Next()
	}
}

// AgentRPMMiddleware enforces PerAgentRPM. It reads the agent ID from the
// request body field "agent_id" (for /chat) or from the ":id" path param
// (for agent-specific routes). Routes without an agent ID are skipped.
func (m *Manager) AgentRPMMiddleware() fiber.Handler {
	cfg := m.config()
	if !cfg.Enabled || cfg.PerAgentRPM <= 0 {
		return func(c *fiber.Ctx) error { return c.Next() }
	}
	limit := int64(cfg.PerAgentRPM)
	return func(c *fiber.Ctx) error {
		agentID := c.Params("id")
		if agentID == "" {
			// For /chat endpoints, peek at the body without consuming it.
			var body struct {
				AgentID string `json:"agent_id"`
			}
			// BodyParser on Fiber does not consume the body; subsequent
			// handlers can still read it.
			_ = c.BodyParser(&body)
			agentID = body.AgentID
		}
		if agentID == "" {
			return c.Next()
		}

		key := agentKey(c, agentID)
		count, err := m.counter.Increment(c.Context(), key, time.Minute)
		if err != nil {
			m.log.Warn("ratelimit: counter error", zap.Error(err))
			if m.failClosed {
				c.Set("Retry-After", "1")
				return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "rate limit service unavailable"})
			}
			return c.Next()
		}
		if count > limit {
			m.log.Info("ratelimit: agent RPM exceeded",
				zap.String("agent_id", agentID), zap.Int64("count", count), zap.Int64("limit", limit))
			return c.Status(fiber.StatusTooManyRequests).JSON(fiber.Map{
				"error":       "agent rate limit exceeded",
				"agent_id":    agentID,
				"limit":       limit,
				"window":      "1m",
				"retry_after": "60",
			})
		}
		return c.Next()
	}
}

// Close shuts down background goroutines and the counter.
func (m *Manager) Close() error {
	return m.counter.Close()
}

// ---------------------------------------------------------------------------
// Status handler
// ---------------------------------------------------------------------------

// HandleStatus handles GET /api/v1/rate-limit/status.
// Returns the current limits config and, for the calling user, current RPM
// count and token usage. Useful for GUI dashboards.
func (m *Manager) HandleStatus(c *fiber.Ctx) error {
	userID := credentialOf(c)

	// `tokens_used` is GONE from this response, not zeroed.
	//
	// It reported the in-memory bucket this package used to keep, which
	// nothing ever filled — so it was `0` on every request forever, on a
	// deployment burning millions of tokens a day. A field that always reads
	// zero is worse than an absent one: it answers the question, and the
	// answer is wrong in the reassuring direction. Token consumption against
	// a daily quota is now reported by GET /api/v1/costs/status, which reads
	// the durable ledger the quota is actually enforced against.
	//
	// The two limits are still echoed here, because they ARE this section of
	// the config and an operator checking what is configured should see them.
	// `tokens_enforced_by` says where to look for the usage figure.
	// One snapshot for the whole response. Reading m.cfg field by field would
	// race a concurrent SetConfig and could report a mixture of the old and
	// new limits — which is the one answer an operator checking whether their
	// change took effect must not be given.
	snapshot := m.config()
	return c.JSON(fiber.Map{
		"enabled":              snapshot.Enabled,
		"per_user_rpm":         snapshot.PerUserRPM,
		"per_agent_rpm":        snapshot.PerAgentRPM,
		"per_user_tokens_day":  snapshot.PerUserTokensDay,
		"per_agent_tokens_day": snapshot.PerAgentTokensDay,
		"tokens_enforced_by":   "costs",
		"tokens_usage_url":     "/api/v1/costs/status",
		"backend":              snapshot.Backend,
		"user": fiber.Map{
			"id": userID,
		},
	})
}
