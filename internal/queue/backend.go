// Package queue defines the provider-agnostic interface for Soulacy's durable
// message queue layer.
//
// Two implementations ship out of the box:
//
//	internal/queue/memory — in-process channel-based (zero-dependency, default)
//	internal/queue/nats   — NATS JetStream (durable, multi-instance delivery)
//
// Active backend is selected at startup from config.yaml:
//
//	queue:
//	  backend: memory          # or "nats"
//	  nats_url: nats://localhost:4222
//	  nats_stream: soulacy     # JetStream stream name
//	  nats_subject_prefix: ""  # filter — defaults to "<stream>.>"
//
// Env var equivalents (all prefixed SOULACY_):
//
//	SOULACY_QUEUE_BACKEND, SOULACY_QUEUE_NATS_URL,
//	SOULACY_QUEUE_NATS_STREAM, SOULACY_QUEUE_NATS_SUBJECT_PREFIX
//
// Engine and channel-adapter code imports only this package; it never
// references a concrete implementation, so swapping backends requires only
// a config change.
package queue

import sdkqueue "github.com/soulacy/soulacy/sdk/queue"

// Canonical queue contract types live in the versioned SDK (Story E9);
// these aliases keep every existing import path working unchanged.
type (
	// Message is a single queue message delivered to a subscriber.
	Message = sdkqueue.Message
	// Subscription represents an active queue subscription.
	Subscription = sdkqueue.Subscription
	// Backend is the interface satisfied by every queue implementation.
	Backend = sdkqueue.Backend
)

// NewMessage constructs a Message with an acknowledgement callback.
// Intended for Backend implementations.
var NewMessage = sdkqueue.NewMessage
