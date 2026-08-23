package ratelimit

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// TestLiveRedisSlidingWindow is a release gate, not a developer prerequisite.
// It verifies the Lua script against a real Redis server because lightweight
// Redis emulators commonly implement only a subset of scripting semantics.
func TestLiveRedisSlidingWindow(t *testing.T) {
	rawURL := os.Getenv("SOULACY_TEST_REDIS_URL")
	if rawURL == "" {
		t.Skip("SOULACY_TEST_REDIS_URL is not set")
	}

	backend, err := NewRedisCounter(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	rc := backend.(*redisCounter)
	ctx := context.Background()
	key := fmt.Sprintf("live-%d", time.Now().UnixNano())
	eventsKey, sequenceKey := redisCounterKeys(key)
	defer rc.client.Del(ctx, eventsKey, sequenceKey)

	serverTime, err := rc.client.Time(ctx).Result()
	if err != nil {
		t.Fatal(err)
	}
	window := time.Second
	if err := rc.client.ZAdd(ctx, eventsKey,
		redis.Z{Score: float64(serverTime.Add(-2 * window).UnixMilli()), Member: "expired"},
		redis.Z{Score: float64(serverTime.Add(-window / 2).UnixMilli()), Member: "live"},
	).Err(); err != nil {
		t.Fatal(err)
	}

	count, err := backend.Increment(ctx, key, window)
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("sliding-window count=%d, want 2 (one prior live event plus this request)", count)
	}
	if ttl, err := rc.client.PTTL(ctx, eventsKey).Result(); err != nil || ttl <= 0 || ttl > window {
		t.Fatalf("events TTL=%s err=%v, want (0, %s]", ttl, err, window)
	}
	t.Log("SOULACY_REDIS_SLIDING_WINDOW_GATE=passed")
}
