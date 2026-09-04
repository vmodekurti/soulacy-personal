// Package memory provides an in-process, channel-based implementation of
// queue.Backend. It is the default queue backend for single-instance Soulacy
// deployments and requires no external dependencies.
//
// Delivery semantics:
//   - At-most-once: messages that are published while no subscriber is ready
//     are dropped (the internal channel is buffered; overflow is discarded).
//   - Ack() is a no-op — there is nothing to redeliver in process.
//   - Group subscribers: within a named group, exactly one subscriber receives
//     each message (round-robin). Empty-group subscribers receive every message
//     (fan-out).
//
// Subject matching follows the NATS convention:
//
//	"foo.bar"  — exact match
//	"foo.*"    — one wildcard token
//	"foo.>"    — trailing multi-token wildcard
//	">"        — matches everything
package memory

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/google/uuid"
	"github.com/soulacy/soulacy/internal/queue"
)

// bufSize is the per-subscriber channel buffer depth.
// A slow handler that lets this fill will cause messages to be dropped;
// callers should keep handlers short or spawn goroutines internally.
const bufSize = 256

// compile-time interface assertion.
var _ queue.Backend = (*Backend)(nil)

// ---------------------------------------------------------------------------
// subscriber
// ---------------------------------------------------------------------------

type subscriber struct {
	id      string
	pattern string
	group   string
	ch      chan *queue.Message
	quit    chan struct{}
	once    sync.Once
}

func newSubscriber(pattern, group string) *subscriber {
	return &subscriber{
		id:      uuid.NewString(),
		pattern: pattern,
		group:   group,
		ch:      make(chan *queue.Message, bufSize),
		quit:    make(chan struct{}),
	}
}

// stop closes quit so the dispatch goroutine exits after its current handler
// returns. No drain is needed because send() uses a non-blocking select, so no
// goroutine is ever blocked waiting to write to s.ch.
func (s *subscriber) stop() {
	s.once.Do(func() {
		close(s.quit)
	})
}

// ---------------------------------------------------------------------------
// subscription (returned to callers)
// ---------------------------------------------------------------------------

type subscription struct {
	sub *subscriber
	b   *Backend
}

func (s *subscription) Unsubscribe() error {
	s.b.remove(s.sub.id)
	s.sub.stop()
	return nil
}

// ---------------------------------------------------------------------------
// group round-robin tracker
// ---------------------------------------------------------------------------

// groupKey is the map key for a (pattern, group) pair.
type groupKey struct{ pattern, group string }

// Backend is the in-process queue backend.
type Backend struct {
	mu       sync.RWMutex
	subs     []*subscriber                    // all active subscribers
	groupIdx map[groupKey]*atomic.Uint64      // round-robin counter per (pattern,group)
	quit     chan struct{}
	once     sync.Once
}

// New constructs a ready-to-use in-process queue backend.
func New() *Backend {
	return &Backend{
		groupIdx: make(map[groupKey]*atomic.Uint64),
		quit:     make(chan struct{}),
	}
}

// Publish delivers data to all subscribers whose pattern matches subject.
// Within a named group, one subscriber receives the message (round-robin);
// group-less subscribers all receive it.
//
// Publish never blocks the caller: if a subscriber's buffer is full the
// message is silently discarded for that subscriber.
func (b *Backend) Publish(_ context.Context, subject string, data []byte) error {
	// Snapshot subscribers and group counters under a single read lock.
	b.mu.RLock()
	subs := make([]*subscriber, len(b.subs))
	copy(subs, b.subs)
	// Snapshot the groupIdx pointers we might need. Atomic.Uint64 values are
	// allocated once and never moved, so holding a pointer after RUnlock is safe
	// as long as we don't dereference map entries that may be deleted — the
	// atomic counter itself lives independently on the heap.
	groupCounters := make(map[groupKey]*atomic.Uint64, len(b.groupIdx))
	for k, v := range b.groupIdx {
		groupCounters[k] = v
	}
	b.mu.RUnlock()

	// Classify matching subscribers.
	noGroup := []*subscriber{}
	grouped := map[groupKey][]*subscriber{}

	for _, s := range subs {
		if !matchSubject(s.pattern, subject) {
			continue
		}
		if s.group == "" {
			noGroup = append(noGroup, s)
		} else {
			k := groupKey{s.pattern, s.group}
			grouped[k] = append(grouped[k], s)
		}
	}

	// Fan-out: all group-less subscribers receive a copy.
	for _, s := range noGroup {
		b.send(s, subject, data)
	}

	// Competing consumers: one subscriber per group (round-robin).
	for k, list := range grouped {
		if len(list) == 0 {
			continue
		}
		ctr, ok := groupCounters[k]
		if !ok || ctr == nil {
			continue // group cleaned up between snapshot and here
		}
		idx := ctr.Add(1) - 1
		b.send(list[idx%uint64(len(list))], subject, data)
	}

	return nil
}

// send attempts a non-blocking write to the subscriber's channel.
func (b *Backend) send(s *subscriber, subject string, data []byte) {
	select {
	case <-s.quit:
		return
	default:
	}
	msg := queue.NewMessage(subject, data, nil) // ack is a no-op for memory backend
	select {
	case s.ch <- msg:
	default:
		// buffer full — drop
	}
}

// Subscribe registers handler to receive messages matching subject.
// A goroutine is launched for each subscriber; it runs until Unsubscribe is
// called or the Backend is closed.
func (b *Backend) Subscribe(_ context.Context, subject, group string, handler func(*queue.Message)) (queue.Subscription, error) {
	s := newSubscriber(subject, group)

	b.mu.Lock()
	b.subs = append(b.subs, s)
	if group != "" {
		k := groupKey{subject, group}
		if _, ok := b.groupIdx[k]; !ok {
			b.groupIdx[k] = &atomic.Uint64{}
		}
	}
	b.mu.Unlock()

	// Dispatch goroutine: forwards messages to the handler.
	go func() {
		for {
			select {
			case <-b.quit:
				return
			case <-s.quit:
				return
			case msg, ok := <-s.ch:
				if !ok {
					return
				}
				handler(msg)
			}
		}
	}()

	return &subscription{sub: s, b: b}, nil
}

// remove removes a subscriber by id and cleans up empty group counters.
func (b *Backend) remove(id string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	for i, s := range b.subs {
		if s.id == id {
			b.subs = append(b.subs[:i], b.subs[i+1:]...)
			// If no more subscribers in this group, remove the counter.
			if s.group != "" {
				k := groupKey{s.pattern, s.group}
				anyLeft := false
				for _, rem := range b.subs {
					if rem.pattern == s.pattern && rem.group == s.group {
						anyLeft = true
						break
					}
				}
				if !anyLeft {
					delete(b.groupIdx, k)
				}
			}
			return
		}
	}
}

// Close unsubscribes all subscribers and stops all dispatch goroutines.
func (b *Backend) Close() error {
	b.once.Do(func() {
		close(b.quit)
		b.mu.Lock()
		subs := make([]*subscriber, len(b.subs))
		copy(subs, b.subs)
		b.subs = b.subs[:0]
		b.mu.Unlock()
		for _, s := range subs {
			s.stop()
		}
	})
	return nil
}

// ---------------------------------------------------------------------------
// Subject matching — NATS-compatible wildcard rules
// ---------------------------------------------------------------------------

// matchSubject reports whether subject matches the given pattern.
//
// Supported wildcards:
//
//	*  — matches exactly one dot-delimited token
//	>  — matches one or more trailing tokens (must be the last token)
func matchSubject(pattern, subject string) bool {
	if pattern == subject {
		return true
	}
	if pattern == ">" {
		return true
	}

	pp := strings.Split(pattern, ".")
	sp := strings.Split(subject, ".")

	for i, tok := range pp {
		if tok == ">" {
			// must be the last token in the pattern; subject must have at least
			// one more token here.
			return i == len(pp)-1 && i < len(sp)
		}
		if i >= len(sp) {
			return false
		}
		if tok != "*" && tok != sp[i] {
			return false
		}
	}
	return len(pp) == len(sp)
}

