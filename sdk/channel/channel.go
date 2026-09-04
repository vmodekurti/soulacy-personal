// Package channel defines the contract every Soulacy channel adapter
// implements — the bridge between a messaging platform (Telegram, Slack,
// an External Channel Protocol sidecar, …) and the agent engine.
//
// Compatibility: this interface is FROZEN per SDK major version. Additive
// capabilities arrive as optional extension interfaces (e.g. `interface {
// Done() <-chan struct{} }`), never as new methods here. See the SDK README.
package channel

import (
	"context"

	"github.com/soulacy/soulacy/sdk/message"
)

// Adapter is the interface every channel must implement.
type Adapter interface {
	// ID returns the unique identifier for this channel type (e.g. "telegram").
	ID() string

	// Name returns a human-readable display name.
	Name() string

	// Start connects to the platform and begins receiving messages.
	// Inbound messages must be posted to the provided inbox channel.
	// Start must be non-blocking — launch a goroutine internally.
	Start(ctx context.Context, inbox chan<- message.Message) error

	// Send delivers an outbound message to the platform.
	Send(ctx context.Context, msg message.Message) error

	// Stop gracefully disconnects from the platform.
	Stop() error

	// Status reports whether the adapter is connected.
	Status() AdapterStatus
}

// AdapterStatus describes the current connection state of a channel adapter.
type AdapterStatus struct {
	Connected bool   `json:"connected"`
	Detail    string `json:"detail,omitempty"` // e.g. "polling" or error message
	QRCode    string `json:"qr_code,omitempty"`
}
