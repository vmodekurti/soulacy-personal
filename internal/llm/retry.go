// retry.go — small retry-with-jitter wrapper for LLM provider HTTP calls.
//
// PRODUCTION_AUDIT → HIGH/Reliability: a single 502 from OpenAI or rate-
// limit blip from Anthropic previously killed an agent run after minutes of
// accumulated state. Providers now wrap their HTTP layer with this helper.
// Only transient errors trigger retry (5xx, 429, connection-level errors);
// 4xx (other than 429) and successful responses pass through unchanged.
//
// Retries are bounded by the request context — once the deadline is hit,
// the final error is returned even if the retry budget hasn't run out.

package llm

import (
	"context"
	"errors"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type retryStatsKey struct{}

// RetryStats is request-scoped and populated by DoWithRetry so the central
// ledger can account for provider attempts without provider-specific plumbing.
type RetryStats struct {
	mu         sync.Mutex
	attempts   int
	requestIDs []string
}

func withRetryStats(ctx context.Context) (context.Context, *RetryStats) {
	stats := &RetryStats{}
	return context.WithValue(ctx, retryStatsKey{}, stats), stats
}

func retryStatsFromContext(ctx context.Context) *RetryStats {
	stats, _ := ctx.Value(retryStatsKey{}).(*RetryStats)
	return stats
}

func (s *RetryStats) record(resp *http.Response) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attempts++
	if resp != nil {
		if id := strings.TrimSpace(resp.Header.Get("x-request-id")); id != "" {
			s.requestIDs = append(s.requestIDs, id)
		}
	}
}

func (s *RetryStats) snapshot() (int, []string) {
	if s == nil {
		return 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.attempts, append([]string(nil), s.requestIDs...)
}

// RetryConfig parameters the helper. Defaults are conservative.
type RetryConfig struct {
	MaxAttempts   int           // total attempts including the first; 0 → 3
	InitialDelay  time.Duration // base delay before first retry; 0 → 250ms
	MaxDelay      time.Duration // cap on exponential growth;       0 → 5s
	MaxRetryAfter time.Duration // cap on provider Retry-After;      0 → 30s
}

func (c RetryConfig) normalize() RetryConfig {
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = 3
	}
	if c.InitialDelay <= 0 {
		c.InitialDelay = 250 * time.Millisecond
	}
	if c.MaxDelay <= 0 {
		c.MaxDelay = 5 * time.Second
	}
	if c.MaxRetryAfter <= 0 {
		c.MaxRetryAfter = 30 * time.Second
	}
	return c
}

// DoWithRetry sends `req` via `client` with retry on transient failures.
// IMPORTANT: callers must ensure `req` has a body that can be rewound — for
// JSON payloads, set `req.GetBody = func() (io.ReadCloser, error) { ... }`
// so net/http can replay the body on each attempt. For requests with no body
// or with `Body` set to a *bytes.Reader, net/http does this automatically.
func DoWithRetry(ctx context.Context, client *http.Client, req *http.Request, cfg RetryConfig) (*http.Response, error) {
	cfg = cfg.normalize()
	var lastResp *http.Response
	var lastErr error
	delay := cfg.InitialDelay

	for attempt := 1; attempt <= cfg.MaxAttempts; attempt++ {
		// Honour context cancellation between attempts.
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if attempt > 1 && req.GetBody != nil {
			body, err := req.GetBody()
			if err != nil {
				return nil, err
			}
			req.Body = body
		}

		resp, err := client.Do(req)
		if stats := retryStatsFromContext(ctx); stats != nil {
			stats.record(resp)
		}
		lastResp = resp
		lastErr = err

		if err == nil && !shouldRetryStatus(resp.StatusCode) {
			return resp, nil
		}
		if !shouldRetryError(err) && err != nil {
			return resp, err
		}

		// Drain and close the body before retrying so the connection can be
		// returned to the pool (net/http won't reuse a conn with an unread body).
		if resp != nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}

		if attempt == cfg.MaxAttempts {
			break
		}

		// Sleep with jitter. Honour Retry-After when present.
		sleep := delay + jitter(delay)
		if resp != nil {
			if ra := parseRetryAfter(resp.Header.Get("Retry-After")); ra > 0 {
				sleep = ra
				if sleep > cfg.MaxRetryAfter {
					sleep = cfg.MaxRetryAfter
				}
			}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(sleep):
		}

		delay *= 2
		if delay > cfg.MaxDelay {
			delay = cfg.MaxDelay
		}
	}

	if lastErr != nil {
		return lastResp, lastErr
	}
	return lastResp, nil
}

// shouldRetryStatus returns true for HTTP status codes that warrant a retry.
// Includes 408 (request timeout), 429 (rate limit), 5xx server-side errors.
func shouldRetryStatus(code int) bool {
	return code == 408 || code == 429 || code >= 500
}

// shouldRetryError returns true for transport-level errors that are likely
// transient: connection reset, EOF during read, dial timeouts.
func shouldRetryError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return netErr.Timeout() || netErr.Temporary() //nolint:staticcheck // SA1019: still useful even with Go 1.18+
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return shouldRetryError(urlErr.Err)
	}
	return false
}

// parseRetryAfter accepts either a delta-seconds value ("60") or an HTTP-date.
// Returns 0 on parse failure.
func parseRetryAfter(s string) time.Duration {
	if s == "" {
		return 0
	}
	if secs, err := time.ParseDuration(s + "s"); err == nil && secs > 0 {
		return secs
	}
	if t, err := http.ParseTime(s); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

// jitter returns a uniformly distributed random value in [0, base/2).
// Halves the variance of thundering-herd retries.
func jitter(base time.Duration) time.Duration {
	if base <= 0 {
		return 0
	}
	return time.Duration(rand.Int63n(int64(base) / 2)) //nolint:gosec // not crypto
}
