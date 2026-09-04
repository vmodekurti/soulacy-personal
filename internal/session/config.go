// config.go — configuration for the session resource store.
package session

import "time"

const (
	// DefaultAttachmentTTL is how long a resource is retained before Prune
	// removes it.
	DefaultAttachmentTTL = 72 * time.Hour

	// DefaultMaxAttachmentSize is the maximum accepted blob size (50 MiB).
	DefaultMaxAttachmentSize int64 = 52_428_800
)

// Config controls the behaviour of the session resource store.
type Config struct {
	// AttachmentTTL is the default time-to-live applied to resources when no
	// explicit TTL is passed to Put.  Defaults to 72 h.
	AttachmentTTL time.Duration

	// MaxAttachmentSize is the upper bound on the data blob accepted by Put,
	// in bytes.  Put rejects payloads larger than this value.  Defaults to
	// 50 MiB (52 428 800 bytes).
	MaxAttachmentSize int64
}

// DefaultConfig returns a Config pre-populated with the recommended defaults.
func DefaultConfig() Config {
	return Config{
		AttachmentTTL:     DefaultAttachmentTTL,
		MaxAttachmentSize: DefaultMaxAttachmentSize,
	}
}
