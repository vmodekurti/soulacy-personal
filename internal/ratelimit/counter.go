package ratelimit

import (
	"context"
	"crypto/sha256"
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
	// Increment atomically adds an event for key and returns the number of
	// events in the immediately preceding window.
	// Returns the new count after incrementing.
	Increment(ctx context.Context, key string, window time.Duration) (int64, error)

	// Close releases resources (background goroutines, connections).
	Close() error
}

// ---------------------------------------------------------------------------
// In-memory sliding-window counter
// ---------------------------------------------------------------------------

type windowEntry struct {
	mu       sync.Mutex
	events   []time.Time
	lastSeen time.Time
	window   time.Duration
}

// MemoryCounter is a thread-safe, sliding-window in-memory counter.
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
	if window <= 0 {
		return 0, errors.New("ratelimit: window must be positive")
	}
	now := time.Now()

	val, _ := c.entries.LoadOrStore(key, &windowEntry{
		lastSeen: now,
		window:   window,
	})
	entry := val.(*windowEntry)

	entry.mu.Lock()
	defer entry.mu.Unlock()

	cutoff := now.Add(-window)
	firstLive := 0
	for firstLive < len(entry.events) && !entry.events[firstLive].After(cutoff) {
		firstLive++
	}
	if firstLive > 0 {
		entry.events = append([]time.Time(nil), entry.events[firstLive:]...)
	}
	entry.events = append(entry.events, now)
	entry.lastSeen = now
	entry.window = window
	return int64(len(entry.events)), nil
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
				stale := now.Sub(entry.lastSeen) > 2*entry.window
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
// Redis sliding-window counter
// ---------------------------------------------------------------------------

// incrementWindowScript prunes, records, counts, and expires a request in one
// Redis operation. Redis TIME supplies the clock so gateway replicas cannot
// disagree because of host clock skew. KEYS[1] and KEYS[2] share a cluster
// hash tag, keeping the script valid on Redis Cluster as well as standalone.
var incrementWindowScript = redis.NewScript(`
local redis_time = redis.call('TIME')
local now_ms = (tonumber(redis_time[1]) * 1000) + math.floor(tonumber(redis_time[2]) / 1000)
local cutoff_ms = now_ms - tonumber(ARGV[1])
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', cutoff_ms)
local sequence = redis.call('INCR', KEYS[2])
redis.call('ZADD', KEYS[1], now_ms, tostring(now_ms) .. '-' .. tostring(sequence))
redis.call('PEXPIRE', KEYS[1], ARGV[1])
redis.call('PEXPIRE', KEYS[2], ARGV[1])
return redis.call('ZCARD', KEYS[1])
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
	eventsKey, sequenceKey := redisCounterKeys(key)
	count, err := incrementWindowScript.Run(ctx, r.client, []string{eventsKey, sequenceKey}, window.Milliseconds()).Int64()
	if err != nil {
		return 0, fmt.Errorf("ratelimit: Redis increment: %w", err)
	}
	return count, nil
}

func redisCounterKeys(key string) (eventsKey, sequenceKey string) {
	digest := sha256.Sum256([]byte(key))
	hashTag := fmt.Sprintf("%x", digest[:16])
	return "soulacy:ratelimit:{" + hashTag + "}:events", "soulacy:ratelimit:{" + hashTag + "}:sequence"
}

func (r *redisCounter) Close() error {
	if r == nil || r.client == nil {
		return nil
	}
	return r.client.Close()
}
