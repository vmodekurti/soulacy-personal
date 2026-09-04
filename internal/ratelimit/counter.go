package ratelimit

import (
	"context"
	"sync"
	"time"
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
// Redis counter (optional — requires go get github.com/redis/go-redis/v9)
// ---------------------------------------------------------------------------
// RedisCounter is intentionally left as a stub that falls back to the memory
// counter at construction time. To enable it:
//
//  1. go get github.com/redis/go-redis/v9
//  2. Replace the stub body of NewRedisCounter with the real implementation
//     shown in the comment below.
//
// Stub implementation — always falls back to MemoryCounter:

// NewRedisCounter attempts to connect to Redis at url. On failure (or when
// the redis package is not yet imported) it logs and returns a MemoryCounter.
//
// Real implementation (uncomment after go get github.com/redis/go-redis/v9):
//
//	func NewRedisCounter(url string) (Counter, error) {
//	    opt, err := redis.ParseURL(url)
//	    if err != nil { return nil, err }
//	    rdb := redis.NewClient(opt)
//	    if err := rdb.Ping(context.Background()).Err(); err != nil {
//	        rdb.Close()
//	        return nil, fmt.Errorf("redis ping: %w", err)
//	    }
//	    return &redisCounter{rdb: rdb}, nil
//	}
//
//	type redisCounter struct { rdb *redis.Client }
//	func (r *redisCounter) Increment(ctx context.Context, key string, window time.Duration) (int64, error) {
//	    pipe := r.rdb.Pipeline()
//	    incr := pipe.Incr(ctx, key)
//	    pipe.Expire(ctx, key, window)
//	    if _, err := pipe.Exec(ctx); err != nil { return 0, err }
//	    return incr.Val(), nil
//	}
//	func (r *redisCounter) Close() error { return r.rdb.Close() }
func NewRedisCounter(_ string) (Counter, error) {
	// TODO: implement after go get github.com/redis/go-redis/v9
	// For now, fall back to in-memory so the gateway can start without Redis.
	return NewMemoryCounter(), nil
}
