// Package nats provides a NATS JetStream implementation of queue.Backend.
//
// # Overview
//
// This backend connects to a NATS server, ensures the configured JetStream
// stream exists, and then uses push-based durable consumers for delivery.
// Each call to Subscribe creates or reuses a durable consumer named after the
// group (or a per-subscription UUID for group-less subscriptions).
//
// # Delivery guarantees
//
//   - At-least-once: NATS JetStream redelivers unacknowledged messages after
//     the AckWait period (default 30s). Handlers MUST call msg.Ack().
//   - Competing consumers: subscribing with the same group name routes each
//     message to exactly one member of the group (NATS queue groups).
//
// # Stream auto-creation
//
// On New(), the backend checks whether the configured stream already exists.
// If not, it creates one with Limits retention (the NATS default).  Existing
// streams are never modified so operators can tune retention/storage outside
// Soulacy.
//
// # Configuration (via config.yaml)
//
//	queue:
//	  backend: nats
//	  nats_url: nats://localhost:4222       # NATS server URL; can be a comma-separated cluster list
//	  nats_stream: soulacy                  # JetStream stream name
//	  nats_subject_prefix: ""              # subjects filter; defaults to "<stream>.>"
//
// Env var equivalents: SOULACY_QUEUE_NATS_URL, SOULACY_QUEUE_NATS_STREAM,
// SOULACY_QUEUE_NATS_SUBJECT_PREFIX.
package nats

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	natsgo "github.com/nats-io/nats.go"
	"github.com/soulacy/soulacy/internal/queue"
)

// compile-time interface assertion.
var _ queue.Backend = (*Backend)(nil)

// Config holds all connection and stream parameters for the NATS backend.
type Config struct {
	// URL is the NATS server URL (or comma-separated list for cluster).
	// Defaults to nats://localhost:4222.
	URL string

	// StreamName is the JetStream stream that owns the Soulacy subjects.
	// Defaults to "soulacy".
	StreamName string

	// SubjectPrefix is the NATS subject filter applied to the stream
	// (e.g. "soulacy.>" matches all subjects under the "soulacy" namespace).
	// Defaults to "<StreamName>.>".
	SubjectPrefix string

	// AckWait is how long JetStream waits for an Ack before redelivering.
	// Defaults to 30s.
	AckWait time.Duration

	// MaxDeliver is the maximum number of delivery attempts per message.
	// 0 means unlimited.
	MaxDeliver int

	// NATSOptions are extra nats.Option values passed to nats.Connect
	// (e.g. nats.UserCredentials("creds.file"), nats.TLSConfig(…)).
	NATSOptions []natsgo.Option
}

func (c *Config) applyDefaults() {
	if c.URL == "" {
		c.URL = natsgo.DefaultURL
	}
	if c.StreamName == "" {
		c.StreamName = "soulacy"
	}
	if c.SubjectPrefix == "" {
		c.SubjectPrefix = c.StreamName + ".>"
	}
	if c.AckWait == 0 {
		c.AckWait = 30 * time.Second
	}
}

// ---------------------------------------------------------------------------
// subscription
// ---------------------------------------------------------------------------

type natsSub struct {
	sub *natsgo.Subscription
}

func (s *natsSub) Unsubscribe() error {
	return s.sub.Unsubscribe()
}

// ---------------------------------------------------------------------------
// Backend
// ---------------------------------------------------------------------------

// Backend is the NATS JetStream queue backend.
type Backend struct {
	nc   *natsgo.Conn
	js   natsgo.JetStreamContext
	cfg  Config
	mu   sync.Mutex
	subs []*natsgo.Subscription
	once sync.Once
}

// New connects to NATS and ensures the JetStream stream exists.
// Returns an error if the connection fails or the stream cannot be created.
func New(cfg Config) (*Backend, error) {
	cfg.applyDefaults()

	opts := append([]natsgo.Option{
		natsgo.Name("soulacy-gateway"),
		natsgo.MaxReconnects(-1),       // reconnect forever
		natsgo.ReconnectWait(2 * time.Second),
	}, cfg.NATSOptions...)

	nc, err := natsgo.Connect(cfg.URL, opts...)
	if err != nil {
		return nil, fmt.Errorf("nats connect %s: %w", cfg.URL, err)
	}

	js, err := nc.JetStream(natsgo.PublishAsyncMaxPending(256))
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("nats jetstream context: %w", err)
	}

	if err := ensureStream(js, cfg); err != nil {
		nc.Close()
		return nil, err
	}

	return &Backend{nc: nc, js: js, cfg: cfg}, nil
}

// ensureStream creates the stream if it does not already exist.
// If the stream exists with different subjects we leave it unchanged —
// operators control retention/storage outside Soulacy.
func ensureStream(js natsgo.JetStreamContext, cfg Config) error {
	_, err := js.StreamInfo(cfg.StreamName)
	if err == nil {
		return nil // already exists
	}
	if !isNotFound(err) {
		return fmt.Errorf("nats stream info %s: %w", cfg.StreamName, err)
	}

	_, err = js.AddStream(&natsgo.StreamConfig{
		Name:      cfg.StreamName,
		Subjects:  []string{cfg.SubjectPrefix},
		Retention: natsgo.LimitsPolicy,
		Storage:   natsgo.FileStorage,
		MaxAge:    7 * 24 * time.Hour, // 7-day retention by default
	})
	if err != nil && !isAlreadyExists(err) {
		return fmt.Errorf("nats create stream %s: %w", cfg.StreamName, err)
	}
	return nil
}

// Publish sends data to subject and blocks until JetStream acknowledges storage
// or ctx is cancelled.
func (b *Backend) Publish(ctx context.Context, subject string, data []byte) error {
	_, err := b.js.Publish(subject, data, natsgo.Context(ctx))
	if err != nil {
		return fmt.Errorf("nats publish %s: %w", subject, err)
	}
	return nil
}

// Subscribe registers handler to receive messages matching subject.
//
// If group is non-empty, a durable push consumer is created (or reused) with
// that name, and delivery is load-balanced across all subscribers sharing the
// same group. If group is empty, a non-durable ephemeral subscription is used;
// every such subscriber receives every matching message.
//
// handler is invoked from the NATS client's internal goroutine; it must be
// non-blocking or hand off work to its own goroutine.
func (b *Backend) Subscribe(_ context.Context, subject, group string, handler func(*queue.Message)) (queue.Subscription, error) {
	msgHandler := func(m *natsgo.Msg) {
		msg := queue.NewMessage(m.Subject, m.Data, func() error {
			return m.Ack()
		})
		handler(msg)
	}

	ackWait := b.cfg.AckWait
	maxDeliver := b.cfg.MaxDeliver

	var sub *natsgo.Subscription
	var err error

	if group != "" {
		// Durable queue subscription — competing consumers.
		opts := []natsgo.SubOpt{
			natsgo.Durable(group),
			natsgo.ManualAck(),
			natsgo.AckWait(ackWait),
		}
		if maxDeliver > 0 {
			opts = append(opts, natsgo.MaxDeliver(maxDeliver))
		}
		sub, err = b.js.QueueSubscribe(subject, group, msgHandler, opts...)
	} else {
		// Ephemeral subscription — every subscriber gets every message.
		// Use a unique durable name so replays work across restarts.
		durName := "ephemeral-" + uuid.NewString()
		opts := []natsgo.SubOpt{
			natsgo.Durable(durName),
			natsgo.ManualAck(),
			natsgo.AckWait(ackWait),
		}
		if maxDeliver > 0 {
			opts = append(opts, natsgo.MaxDeliver(maxDeliver))
		}
		sub, err = b.js.Subscribe(subject, msgHandler, opts...)
	}
	if err != nil {
		return nil, fmt.Errorf("nats subscribe %s (group=%q): %w", subject, group, err)
	}

	b.mu.Lock()
	b.subs = append(b.subs, sub)
	b.mu.Unlock()

	return &natsSub{sub: sub}, nil
}

// Close drains the NATS connection (flushes pending publish acks) and closes
// all subscriptions.
func (b *Backend) Close() error {
	var firstErr error
	b.once.Do(func() {
		b.mu.Lock()
		subs := make([]*natsgo.Subscription, len(b.subs))
		copy(subs, b.subs)
		b.subs = b.subs[:0]
		b.mu.Unlock()

		for _, s := range subs {
			if err := s.Unsubscribe(); err != nil && firstErr == nil {
				firstErr = err
			}
		}

		if err := b.nc.Drain(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("nats drain: %w", err)
		}
	})
	return firstErr
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "stream not found") ||
		err == natsgo.ErrStreamNotFound
}

func isAlreadyExists(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "stream name already in use") ||
		strings.Contains(err.Error(), "already exists")
}
