// Package queue defines the provider-agnostic contract for Soulacy's durable
// message queue layer. Implementations: in-process memory backend (default)
// and NATS JetStream — plus any out-of-tree backend satisfying Backend.
//
// Compatibility: Backend is FROZEN per SDK major version; see the SDK README.
package queue

import "context"

// Message is a single queue message delivered to a subscriber.
// Handlers must call Ack() after successful processing to prevent redelivery
// (on backends that support at-least-once delivery such as NATS JetStream).
type Message struct {
	// Subject is the routing key the message was published to.
	Subject string

	// Data is the raw message payload.
	Data []byte

	// ack is the backend-specific acknowledgement function.
	// Callers invoke Ack() rather than calling this directly.
	ack func() error
}

// Ack signals the broker that delivery succeeded and the message must not be
// redelivered. Safe to call more than once; subsequent calls are no-ops.
func (m *Message) Ack() error {
	if m.ack != nil {
		return m.ack()
	}
	return nil
}

// NewMessage constructs a Message with an acknowledgement callback.
// This constructor is intended for use by Backend implementations; consumers
// receive *Message values from Subscribe handlers.
func NewMessage(subject string, data []byte, ack func() error) *Message {
	return &Message{Subject: subject, Data: data, ack: ack}
}

// Subscription represents an active queue subscription.
// Call Unsubscribe to stop receiving messages and release backend resources.
type Subscription interface {
	// Unsubscribe stops delivery and frees any resources held by this
	// subscription. Idempotent.
	Unsubscribe() error
}

// Backend is the interface satisfied by every queue implementation.
//
// Subject syntax follows the NATS convention:
//   - "foo.bar"  — exact match
//   - "foo.*"    — matches exactly one dot-delimited token
//   - "foo.>"    — matches any number of trailing tokens
//   - ">"        — matches all subjects
//
// Every backend must apply the same matching rules so behaviour is
// consistent regardless of the configured implementation.
type Backend interface {
	// Publish sends data to subject. For JetStream backends, Publish blocks
	// until the broker acknowledges persistence or ctx is cancelled.
	Publish(ctx context.Context, subject string, data []byte) error

	// Subscribe registers handler to receive messages matching subject within
	// the named consumer group. Multiple gateway instances sharing the same
	// group receive each message exactly once (competing consumers). Pass an
	// empty group string for fan-out delivery (every subscriber gets a copy).
	//
	// handler is invoked from a dedicated goroutine and must be safe to call
	// concurrently. handler must call msg.Ack() when done.
	Subscribe(ctx context.Context, subject, group string, handler func(*Message)) (Subscription, error)

	// Close drains in-flight messages and releases all held resources.
	// Idempotent.
	Close() error
}
