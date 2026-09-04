package ratelimit

import (
	"fmt"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/auth"
)

// ---------------------------------------------------------------------------
// Token quota — 24h sliding window, in-memory
// ---------------------------------------------------------------------------

// tokenBucket tracks a user's token consumption within a 24h sliding window.
type tokenBucket struct {
	mu          sync.Mutex
	total       int64
	windowStart time.Time
}

func (b *tokenBucket) add(n int64) int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	if time.Since(b.windowStart) >= 24*time.Hour {
		b.total = 0
		b.windowStart = time.Now()
	}
	b.total += n
	return b.total
}

func (b *tokenBucket) get() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	if time.Since(b.windowStart) >= 24*time.Hour {
		return 0
	}
	return b.total
}

// ---------------------------------------------------------------------------
// Manager
// ---------------------------------------------------------------------------

// Manager holds the rate-limit state and produces Fiber middleware.
type Manager struct {
	cfg     Config
	counter Counter
	log     *zap.Logger

	// Per-user 24h token buckets. Key: JWT subject (or "anon" for open mode).
	tokenMu      sync.RWMutex
	tokenBuckets map[string]*tokenBucket
	tokenStop    chan struct{}

	// Per-agent 24h token buckets. Key: agentID.
	agentTokenMu      sync.RWMutex
	agentTokenBuckets map[string]*tokenBucket
	agentTokenStop    chan struct{}
}

// New creates a Manager from cfg. The Counter backend is selected from
// cfg.Backend ("memory" or "redis"). Falls back to memory on Redis failure.
func New(cfg Config, log *zap.Logger) (*Manager, error) {
	var counter Counter
	var err error

	switch cfg.Backend {
	case "redis":
		if cfg.RedisURL == "" {
			return nil, fmt.Errorf("ratelimit: backend=redis but redis_url is empty")
		}
		counter, err = NewRedisCounter(cfg.RedisURL)
		if err != nil {
			log.Warn("ratelimit: Redis unavailable, falling back to in-memory counter",
				zap.String("url", cfg.RedisURL), zap.Error(err))
			counter = NewMemoryCounter()
		} else {
			log.Info("ratelimit: Redis counter ready", zap.String("url", cfg.RedisURL))
		}
	default: // "memory" or empty
		counter = NewMemoryCounter()
		log.Info("ratelimit: in-memory counter ready")
	}

	m := &Manager{
		cfg:               cfg,
		counter:           counter,
		log:               log,
		tokenBuckets:      make(map[string]*tokenBucket),
		tokenStop:         make(chan struct{}),
		agentTokenBuckets: make(map[string]*tokenBucket),
		agentTokenStop:    make(chan struct{}),
	}
	go m.sweepTokenBuckets()
	go m.sweepAgentTokenBuckets()
	return m, nil
}

// ---------------------------------------------------------------------------
// Token recording (called by engine after each LLM response)
// ---------------------------------------------------------------------------

// RecordTokens adds n tokens to the 24h bucket for userID.
// userID should be the JWT subject; pass "anon" for unauthenticated requests.
func (m *Manager) RecordTokens(userID string, n int) {
	if m.cfg.PerUserTokensDay == 0 || n <= 0 {
		return
	}
	m.tokenMu.Lock()
	b, ok := m.tokenBuckets[userID]
	if !ok {
		b = &tokenBucket{windowStart: time.Now()}
		m.tokenBuckets[userID] = b
	}
	m.tokenMu.Unlock()
	b.add(int64(n))
}

// RecordAgentTokens adds n tokens to the 24h bucket for agentID.
func (m *Manager) RecordAgentTokens(agentID string, n int) {
	if m.cfg.PerAgentTokensDay == 0 || n <= 0 {
		return
	}
	m.agentTokenMu.Lock()
	b, ok := m.agentTokenBuckets[agentID]
	if !ok {
		b = &tokenBucket{windowStart: time.Now()}
		m.agentTokenBuckets[agentID] = b
	}
	m.agentTokenMu.Unlock()
	b.add(int64(n))
}

// sweepTokenBuckets removes expired token buckets hourly.
func (m *Manager) sweepTokenBuckets() {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-m.tokenStop:
			return
		case <-ticker.C:
			m.tokenMu.Lock()
			for id, b := range m.tokenBuckets {
				b.mu.Lock()
				stale := time.Since(b.windowStart) > 25*time.Hour
				b.mu.Unlock()
				if stale {
					delete(m.tokenBuckets, id)
				}
			}
			m.tokenMu.Unlock()
		}
	}
}

// sweepAgentTokenBuckets removes expired agent token buckets hourly.
func (m *Manager) sweepAgentTokenBuckets() {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-m.agentTokenStop:
			return
		case <-ticker.C:
			m.agentTokenMu.Lock()
			for id, b := range m.agentTokenBuckets {
				b.mu.Lock()
				stale := time.Since(b.windowStart) > 25*time.Hour
				b.mu.Unlock()
				if stale {
					delete(m.agentTokenBuckets, id)
				}
			}
			m.agentTokenMu.Unlock()
		}
	}
}

// ---------------------------------------------------------------------------
// Middleware
// ---------------------------------------------------------------------------

// UserRPMMiddleware enforces PerUserRPM on every request.
// If claims are absent (open mode), the key "anon" is used so open-mode
// deployments still get a single shared bucket.
func (m *Manager) UserRPMMiddleware() fiber.Handler {
	if !m.cfg.Enabled || m.cfg.PerUserRPM <= 0 {
		return func(c *fiber.Ctx) error { return c.Next() }
	}
	limit := int64(m.cfg.PerUserRPM)
	return func(c *fiber.Ctx) error {
		key := "user:anon"
		if cl := auth.ClaimsFromCtx(c); cl != nil && cl.Subject != "" {
			key = "user:" + cl.Subject
		}
		count, err := m.counter.Increment(c.Context(), key, time.Minute)
		if err != nil {
			m.log.Warn("ratelimit: counter error", zap.Error(err))
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
	if !m.cfg.Enabled || m.cfg.PerAgentRPM <= 0 {
		return func(c *fiber.Ctx) error { return c.Next() }
	}
	limit := int64(m.cfg.PerAgentRPM)
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

		key := "agent:" + agentID
		count, err := m.counter.Increment(c.Context(), key, time.Minute)
		if err != nil {
			m.log.Warn("ratelimit: counter error", zap.Error(err))
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

// AgentTokenQuotaMiddleware enforces PerAgentTokensDay. Apply only to /chat routes.
// Reads agent ID from the ":id" path param or the "agent_id" body field.
func (m *Manager) AgentTokenQuotaMiddleware() fiber.Handler {
	if !m.cfg.Enabled || m.cfg.PerAgentTokensDay <= 0 {
		return func(c *fiber.Ctx) error { return c.Next() }
	}
	limit := int64(m.cfg.PerAgentTokensDay)
	return func(c *fiber.Ctx) error {
		agentID := c.Params("id")
		if agentID == "" {
			var body struct {
				AgentID string `json:"agent_id"`
			}
			_ = c.BodyParser(&body)
			agentID = body.AgentID
		}
		if agentID == "" {
			return c.Next()
		}

		m.agentTokenMu.RLock()
		b := m.agentTokenBuckets[agentID]
		m.agentTokenMu.RUnlock()

		if b != nil && b.get() >= limit {
			m.log.Info("ratelimit: agent token quota exceeded",
				zap.String("agent_id", agentID), zap.Int64("limit", limit))
			return c.Status(fiber.StatusTooManyRequests).JSON(fiber.Map{
				"error":       "agent daily token quota exceeded",
				"agent_id":    agentID,
				"limit":       limit,
				"window":      "24h",
				"retry_after": "3600",
			})
		}
		return c.Next()
	}
}

// TokenQuotaMiddleware enforces PerUserTokensDay. Apply only to /chat routes.
// Reads JWT subject from claims for the key; "anon" for open-mode.
func (m *Manager) TokenQuotaMiddleware() fiber.Handler {
	if !m.cfg.Enabled || m.cfg.PerUserTokensDay <= 0 {
		return func(c *fiber.Ctx) error { return c.Next() }
	}
	limit := int64(m.cfg.PerUserTokensDay)
	return func(c *fiber.Ctx) error {
		userID := "anon"
		if cl := auth.ClaimsFromCtx(c); cl != nil && cl.Subject != "" {
			userID = cl.Subject
		}

		m.tokenMu.RLock()
		b := m.tokenBuckets[userID]
		m.tokenMu.RUnlock()

		if b != nil && b.get() >= limit {
			m.log.Info("ratelimit: user token quota exceeded",
				zap.String("user_id", userID), zap.Int64("limit", limit))
			return c.Status(fiber.StatusTooManyRequests).JSON(fiber.Map{
				"error":       "daily token quota exceeded",
				"limit":       limit,
				"window":      "24h",
				"retry_after": "3600",
			})
		}
		return c.Next()
	}
}

// Close shuts down background goroutines and the counter.
func (m *Manager) Close() error {
	close(m.tokenStop)
	close(m.agentTokenStop)
	return m.counter.Close()
}

// ---------------------------------------------------------------------------
// Status handler
// ---------------------------------------------------------------------------

// HandleStatus handles GET /api/v1/rate-limit/status.
// Returns the current limits config and, for the calling user, current RPM
// count and token usage. Useful for GUI dashboards.
func (m *Manager) HandleStatus(c *fiber.Ctx) error {
	userID := "anon"
	if cl := auth.ClaimsFromCtx(c); cl != nil && cl.Subject != "" {
		userID = cl.Subject
	}

	var tokenUsed int64
	m.tokenMu.RLock()
	if b := m.tokenBuckets[userID]; b != nil {
		tokenUsed = b.get()
	}
	m.tokenMu.RUnlock()

	return c.JSON(fiber.Map{
		"enabled":               m.cfg.Enabled,
		"per_user_rpm":          m.cfg.PerUserRPM,
		"per_agent_rpm":         m.cfg.PerAgentRPM,
		"per_user_tokens_day":   m.cfg.PerUserTokensDay,
		"per_agent_tokens_day":  m.cfg.PerAgentTokensDay,
		"backend":               m.cfg.Backend,
		"user": fiber.Map{
			"id":          userID,
			"tokens_used": tokenUsed,
		},
	})
}
