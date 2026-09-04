// Package events publishes Soulacy's internal event stream to the queue
// backend as versioned envelopes (extensibility story E1, design:
// docs/EXTENSIBILITY.md §4, schema contract: docs/EVENTS.md).
//
// The publisher is deliberately decoupled from the engine hot path: callers
// enqueue into a buffered channel and a single worker goroutine talks to the
// broker. If the buffer fills (broker down, NATS slow), events are dropped
// with a warning — engine progress always wins over observer completeness.
// This mirrors the actionlog writer's design.
package events

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/queue"
	"github.com/soulacy/soulacy/pkg/message"
)

// SchemaVersion is the current event envelope schema. Additive fields do not
// bump this; renamed/removed/retyped fields do (see docs/EVENTS.md).
const SchemaVersion = 1

// SubjectPrefix is the queue subject namespace for all event envelopes.
// Subscribers use "soulacy.events.>" to receive everything.
const SubjectPrefix = "soulacy.events."

// Envelope is the versioned wire format for one event.
type Envelope struct {
	Schema    int       `json:"schema"`
	ID        string    `json:"id"`
	Type      string    `json:"type"`
	AgentID   string    `json:"agent_id"`
	SessionID string    `json:"session_id"`
	TS        time.Time `json:"ts"`
	Data      any       `json:"data"`
}

// NewEnvelope wraps a message.Event in the schema-v1 envelope.
func NewEnvelope(ev message.Event) Envelope {
	ts := ev.Timestamp
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	return Envelope{
		Schema:    SchemaVersion,
		ID:        uuid.New().String(),
		Type:      ev.Type,
		AgentID:   ev.AgentID,
		SessionID: ev.SessionID,
		TS:        ts,
		Data:      ev.Payload,
	}
}

// SubjectFor maps an event type to its queue subject. Event types already
// use dot-separated tokens (message.in, run.failed) which map directly onto
// NATS subject tokens; anything else is sanitised.
func SubjectFor(eventType string) string {
	if eventType == "" {
		return SubjectPrefix + "unknown"
	}
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			return r
		default:
			return '_'
		}
	}, eventType)
	return SubjectPrefix + safe
}

const (
	publishQueueSize = 2048
	publishTimeout   = 5 * time.Second
)

// Publisher forwards events to a queue.Backend without ever blocking the
// caller. Safe for concurrent use. A nil backend yields a no-op publisher.
type Publisher struct {
	backend queue.Backend
	log     *zap.Logger

	queue  chan Envelope
	stop   chan struct{}
	cancel context.CancelFunc
	wg     sync.WaitGroup
	once   sync.Once
}

// NewPublisher starts the worker goroutine. Call Close on shutdown.
func NewPublisher(backend queue.Backend, log *zap.Logger) *Publisher {
	if log == nil {
		log = zap.NewNop()
	}
	ctx, cancel := context.WithCancel(context.Background())
	p := &Publisher{
		backend: backend,
		log:     log,
		queue:   make(chan Envelope, publishQueueSize),
		stop:    make(chan struct{}),
		cancel:  cancel,
	}
	if backend != nil {
		p.wg.Add(1)
		go p.run(ctx)
	}
	return p
}

// PublishEvent enqueues one event for publication. Never blocks; drops with
// a warning when the buffer is full.
func (p *Publisher) PublishEvent(ev message.Event) {
	if p.backend == nil {
		return
	}
	select {
	case p.queue <- NewEnvelope(ev):
	default:
		p.log.Warn("events: publish buffer full, dropping event",
			zap.String("type", ev.Type), zap.String("agent", ev.AgentID))
	}
}

func (p *Publisher) run(ctx context.Context) {
	defer p.wg.Done()
	for {
		select {
		case env := <-p.queue:
			p.publish(ctx, env)
		case <-p.stop:
			// Drain what's already buffered, then exit.
			for {
				select {
				case env := <-p.queue:
					p.publish(ctx, env)
				default:
					return
				}
			}
		}
	}
}

func (p *Publisher) publish(ctx context.Context, env Envelope) {
	data, err := json.Marshal(env)
	if err != nil {
		p.log.Warn("events: marshal failed", zap.String("type", env.Type), zap.Error(err))
		return
	}
	pubCtx, cancel := context.WithTimeout(ctx, publishTimeout)
	defer cancel()
	if err := p.backend.Publish(pubCtx, SubjectFor(env.Type), data); err != nil {
		p.log.Warn("events: queue publish failed", zap.String("type", env.Type), zap.Error(err))
	}
}

// Close stops the worker after draining buffered events. In-flight broker
// calls are cancelled so Close never hangs on a dead broker. Idempotent.
func (p *Publisher) Close() error {
	p.once.Do(func() {
		close(p.stop)
		p.cancel()
		p.wg.Wait()
	})
	return nil
}
