// Package hooks delivers Soulacy's event stream (docs/EVENTS.md) to
// user-configured HTTPS endpoints as signed webhooks (extensibility story
// E2, design: docs/EXTENSIBILITY.md §4).
//
// Delivery is queue-buffered, never inline with the engine: the dispatcher
// subscribes to "soulacy.events.>" on the queue backend, so a dead endpoint
// can never slow an agent down. Guarantee is best-effort with bounded
// retries (exponential backoff + jitter); exhausted deliveries invoke the
// dead-letter callback and are dropped.
package hooks

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/events"
	"github.com/soulacy/soulacy/internal/queue"
)

const (
	defaultMaxAttempts = 5
	maxBackoff         = 10 * time.Minute
	requestTimeout     = 15 * time.Second
	// signatureSkew is the maximum accepted age of a signed payload.
	signatureSkew = 5 * time.Minute
	// maxConcurrent bounds simultaneous in-flight deliveries.
	maxConcurrent = 16
)

// Sign computes the webhook signature header value for a payload:
// "t=<unix>,v1=<hex hmac-sha256 of "<unix>.<body>">".
func Sign(secret string, unixTS int64, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%d.", unixTS)
	mac.Write(body)
	return fmt.Sprintf("t=%d,v1=%s", unixTS, hex.EncodeToString(mac.Sum(nil)))
}

// VerifySignature checks a header produced by Sign against the body.
// now is the verifier's current unix time (rejects skew > 5 minutes).
// Exported so webhook consumers (and tests) can share the implementation.
func VerifySignature(secret, header string, body []byte, now int64) bool {
	var ts int64
	var v1 string
	for _, part := range strings.Split(header, ",") {
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			return false
		}
		switch k {
		case "t":
			parsed, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return false
			}
			ts = parsed
		case "v1":
			v1 = v
		}
	}
	if ts == 0 || v1 == "" {
		return false
	}
	if now-ts > int64(signatureSkew.Seconds()) || ts-now > int64(signatureSkew.Seconds()) {
		return false
	}
	expected := Sign(secret, ts, body)
	_, expectedV1, _ := strings.Cut(expected, "v1=")
	return hmac.Equal([]byte(expectedV1), []byte(v1))
}

// matches reports whether a hook wants this event. Type patterns: exact
// ("run.failed"), global ("*"), or prefix ("run.*"). An empty On list
// matches nothing (hooks must opt in explicitly).
func matches(h config.HookConfig, eventType, agentID string) bool {
	typeOK := false
	for _, pat := range h.On {
		switch {
		case pat == "*":
			typeOK = true
		case strings.HasSuffix(pat, ".*"):
			if strings.HasPrefix(eventType, strings.TrimSuffix(pat, "*")) {
				typeOK = true
			}
		case pat == eventType:
			typeOK = true
		}
		if typeOK {
			break
		}
	}
	if !typeOK {
		return false
	}
	if len(h.Agents) == 0 {
		return true
	}
	for _, a := range h.Agents {
		if a == agentID {
			return true
		}
	}
	return false
}

// DeadFunc is invoked when all delivery attempts for one envelope to one
// hook are exhausted.
type DeadFunc func(hookURL string, env events.Envelope, reason string)

// Dispatcher subscribes to the event stream and delivers matching envelopes
// to configured webhooks.
type Dispatcher struct {
	backend queue.Backend
	hooks   []config.HookConfig
	log     *zap.Logger

	// Tuning knobs — overridable in tests.
	client      *http.Client
	maxAttempts int
	backoff     func(attempt int) time.Duration
	onDead      DeadFunc

	sub    queue.Subscription
	sem    chan struct{}
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	once   sync.Once
}

// NewDispatcher builds a dispatcher. Call Start to begin consuming.
func NewDispatcher(backend queue.Backend, hookCfgs []config.HookConfig, log *zap.Logger) *Dispatcher {
	if log == nil {
		log = zap.NewNop()
	}
	d := &Dispatcher{
		backend:     backend,
		hooks:       hookCfgs,
		log:         log,
		client:      &http.Client{Timeout: requestTimeout},
		maxAttempts: defaultMaxAttempts,
		sem:         make(chan struct{}, maxConcurrent),
	}
	d.backoff = defaultBackoff
	d.onDead = func(hookURL string, env events.Envelope, reason string) {
		log.Warn("webhook.dead: delivery exhausted",
			zap.String("hook", hookURL),
			zap.String("event_type", env.Type),
			zap.String("event_id", env.ID),
			zap.String("reason", reason))
	}
	return d
}

// SetDeadFunc replaces the dead-letter callback (e.g. to write audit
// entries). Must be called before Start.
func (d *Dispatcher) SetDeadFunc(fn DeadFunc) {
	if fn != nil {
		d.onDead = fn
	}
}

// defaultBackoff: 1s, 2s, 4s, … capped at maxBackoff, ±20% jitter.
func defaultBackoff(attempt int) time.Duration {
	base := time.Second << (attempt - 1)
	if base > maxBackoff {
		base = maxBackoff
	}
	jitter := time.Duration(rand.Int63n(int64(base) / 5)) //nolint:gosec // jitter, not crypto
	return base - base/10 + jitter
}

// Start subscribes to the event stream. No-op (but not an error) when no
// hooks are configured or the backend is nil.
func (d *Dispatcher) Start(ctx context.Context) error {
	if d.backend == nil || len(d.hooks) == 0 {
		return nil
	}
	d.ctx, d.cancel = context.WithCancel(ctx)
	sub, err := d.backend.Subscribe(d.ctx, events.SubjectPrefix+">", "webhooks", d.handle)
	if err != nil {
		return fmt.Errorf("hooks: subscribe: %w", err)
	}
	d.sub = sub
	d.log.Info("webhook dispatcher ready", zap.Int("hooks", len(d.hooks)))
	return nil
}

// handle is invoked by the queue for every envelope.
func (d *Dispatcher) handle(m *queue.Message) {
	defer m.Ack() //nolint:errcheck // best-effort stream; see docs/EVENTS.md

	var env events.Envelope
	if err := json.Unmarshal(m.Data, &env); err != nil {
		d.log.Warn("hooks: invalid envelope", zap.Error(err))
		return
	}
	for _, h := range d.hooks {
		if !matches(h, env.Type, env.AgentID) {
			continue
		}
		hook := h // capture
		select {
		case d.sem <- struct{}{}:
			d.wg.Add(1)
			go func() {
				defer d.wg.Done()
				defer func() { <-d.sem }()
				d.deliver(hook, env, m.Data)
			}()
		case <-d.ctx.Done():
			return
		}
	}
}

// deliver posts one envelope to one hook with retries.
func (d *Dispatcher) deliver(h config.HookConfig, env events.Envelope, body []byte) {
	secret := ""
	if h.SecretEnv != "" {
		secret = os.Getenv(h.SecretEnv)
	}

	var lastErr string
	for attempt := 1; attempt <= d.maxAttempts; attempt++ {
		if attempt > 1 {
			select {
			case <-time.After(d.backoff(attempt - 1)):
			case <-d.ctx.Done():
				return
			}
		}
		err := d.post(h.URL, secret, env, body)
		if err == nil {
			return
		}
		lastErr = err.Error()
		d.log.Debug("hooks: delivery attempt failed",
			zap.String("hook", h.URL), zap.Int("attempt", attempt), zap.Error(err))
	}
	d.onDead(h.URL, env, lastErr)
}

func (d *Dispatcher) post(url, secret string, env events.Envelope, body []byte) error {
	ctx, cancel := context.WithTimeout(d.ctx, requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "soulacy-webhook/1")
	req.Header.Set("X-Soulacy-Event", env.Type)
	req.Header.Set("X-Soulacy-Delivery", env.ID)
	if secret != "" {
		req.Header.Set("X-Soulacy-Signature", Sign(secret, time.Now().Unix(), body))
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	return fmt.Errorf("endpoint returned %d", resp.StatusCode)
}

// Close unsubscribes and waits for in-flight deliveries (retries are
// cancelled). Idempotent.
func (d *Dispatcher) Close() error {
	d.once.Do(func() {
		if d.cancel != nil {
			d.cancel()
		}
		if d.sub != nil {
			_ = d.sub.Unsubscribe()
		}
		d.wg.Wait()
	})
	return nil
}
