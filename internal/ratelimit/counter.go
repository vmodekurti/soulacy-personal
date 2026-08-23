package ratelimit

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// ---------------------------------------------------------------------------
// Counter interface
// ---------------------------------------------------------------------------

// Counter is an atomic rate-limit counting backend.
// Implementations must be safe for concurrent use.
type Counter interface {
	// Increment atomically increments the counter for key within a fixed
	// window of duration window, starting from the first call in that window.
	// Returns the new count after incrementing.
	Increment(ctx context.Context, key string, window time.Duration) (int64, error)

	// Close releases resources (background goroutines, connections).
	Close() error
}

// ---------------------------------------------------------------------------
// In-memory fixed-window counter
// ---------------------------------------------------------------------------

type windowEntry struct {
	mu          sync.Mutex
	count       int64
	windowStart time.Time
	window      time.Duration
}

// MemoryCounter is a thread-safe, fixed-window in-memory counter.
// A background goroutine sweeps expired entries every minute to prevent
// unbounded map growth under many unique keys.
type MemoryCounter struct {
	entries sync.Map // key string → *windowEntry
	stopCh  chan struct{}
}

// NewMemoryCounter creates a MemoryCounter and starts the sweep goroutine.
func NewMemoryCounter() *MemoryCounter {
	c := &MemoryCounter{stopCh: make(chan struct{})}
	go c.sweep()
	return c
}

// Increment implements Counter.
func (c *MemoryCounter) Increment(_ context.Context, key string, window time.Duration) (int64, error) {
	now := time.Now()

	val, _ := c.entries.LoadOrStore(key, &windowEntry{
		windowStart: now,
		window:      window,
	})
	entry := val.(*windowEntry)

	entry.mu.Lock()
	defer entry.mu.Unlock()

	// Roll window if expired.
	if now.Sub(entry.windowStart) >= entry.window {
		entry.windowStart = now
		entry.count = 0
		entry.window = window
	}

	entry.count++
	return entry.count, nil
}

// Close stops the background sweep goroutine.
func (c *MemoryCounter) Close() error {
	close(c.stopCh)
	return nil
}

// sweep deletes entries whose window expired more than one full window ago,
// preventing unbounded memory growth from one-off keys.
func (c *MemoryCounter) sweep() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-c.stopCh:
			return
		case now := <-ticker.C:
			c.entries.Range(func(k, v any) bool {
				entry := v.(*windowEntry)
				entry.mu.Lock()
				stale := now.Sub(entry.windowStart) > 2*entry.window
				entry.mu.Unlock()
				if stale {
					c.entries.Delete(k)
				}
				return true
			})
		}
	}
}

// ---------------------------------------------------------------------------
// Redis fixed-window counter
// ---------------------------------------------------------------------------

// incrementWindowScript makes INCR plus first-write expiry one atomic Redis
// operation. A plain pipeline is not sufficient: a process dying between the
// two commands can leave an immortal counter, and resetting expiry on every
// request turns a fixed window into a lockout that never ends under traffic.
var incrementWindowScript = redis.NewScript(`
local count = redis.call('INCR', KEYS[1])
if count == 1 then
  redis.call('PEXPIRE', KEYS[1], ARGV[1])
end
return count
`)

type redisCounter struct{ client *redis.Client }

func NewRedisCounter(rawURL string) (Counter, error) {
	options, err := redis.ParseURL(rawURL)
	if err != nil {
		return nil, fmt.Errorf("ratelimit: parse Redis URL: %w", err)
	}
	client := redis.NewClient(options)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err = client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("ratelimit: Redis ping: %w", err)
	}
	return &redisCounter{client: client}, nil
}

func (r *redisCounter) Increment(ctx context.Context, key string, window time.Duration) (int64, error) {
	if r == nil || r.client == nil {
		return 0, errors.New("ratelimit: Redis counter is unavailable")
	}
	if window <= 0 {
		return 0, errors.New("ratelimit: window must be positive")
	}
	count, err := incrementWindowScript.Run(ctx, r.client, []string{"soulacy:ratelimit:" + key}, window.Milliseconds()).Int64()
	if err != nil {
		return 0, fmt.Errorf("ratelimit: Redis increment: %w", err)
	}
	return count, nil
}

func (r *redisCounter) Close() error {
	if r == nil || r.client == nil {
		return nil
	}
	return r.client.Close()
}
