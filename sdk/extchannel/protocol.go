// Package extchannel defines the External Channel Protocol v1 wire types
// (Story E3, promoted to the SDK in Story E11): NDJSON frames over a
// sidecar's stdin/stdout, version negotiation via hello/hello_ack.
//
// Compatibility: the Frame struct grows by APPENDING fields only; unknown
// frame types must be skipped by both sides (forward compatibility). See
// docs/EXTERNAL_CHANNEL_PROTOCOL.md and the SDK README.
package extchannel

import (
	"encoding/json"
	"fmt"
	"io"
)

// ProtocolVersion is the gateway's current External Channel Protocol
// version. Negotiation picks min(gateway, sidecar).
const ProtocolVersion = 1

// Frame is the superset of all protocol frame fields. Type discriminates;
// unused fields are omitted on the wire.
type Frame struct {
	Type string `json:"type"`

	// hello / hello_ack
	Protocol     int      `json:"protocol,omitempty"`
	Name         string   `json:"name,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`

	// status
	Connected bool   `json:"connected,omitempty"`
	Detail    string `json:"detail,omitempty"`
	QR        string `json:"qr,omitempty"` // pairing flows (e.g. WhatsApp-style QR)

	// message (inbound from the platform)
	ID         string `json:"id,omitempty"`
	ChatID     string `json:"chat_id,omitempty"`
	SenderID   string `json:"sender_id,omitempty"`
	SenderName string `json:"sender_name,omitempty"`
	Text       string `json:"text,omitempty"`
	Timestamp  int64  `json:"timestamp,omitempty"` // unix seconds
	IsGroup    bool   `json:"is_group,omitempty"`

	// send (outbound to the platform)
	To string `json:"to,omitempty"`

	// error
	Error string `json:"error,omitempty"`

	// hello_ack: absolute path of the per-run shared scratch directory the
	// gateway created for this sidecar (Story E24 shared mounts). Large
	// attachments move as files under it — referenced by relative path —
	// never as inline payloads. Empty = no shared dir provisioned.
	SharedDir string `json:"shared_dir,omitempty"`
}

// ParseFrame decodes one NDJSON line. Unknown frame types are NOT an error —
// callers skip frames they don't understand (forward compatibility). A frame
// without a type is malformed.
func ParseFrame(line []byte) (Frame, error) {
	var f Frame
	if err := json.Unmarshal(line, &f); err != nil {
		return Frame{}, fmt.Errorf("extchannel: invalid frame: %w", err)
	}
	if f.Type == "" {
		return Frame{}, fmt.Errorf("extchannel: frame missing type")
	}
	return f, nil
}

// WriteFrame encodes a frame as exactly one NDJSON line.
func WriteFrame(w io.Writer, f Frame) error {
	data, err := json.Marshal(f)
	if err != nil {
		return fmt.Errorf("extchannel: marshal frame: %w", err)
	}
	_, err = w.Write(append(data, '\n'))
	return err
}

// Negotiate validates a sidecar's hello frame and returns the protocol
// version both sides will speak: min(gateway, sidecar).
func Negotiate(hello Frame) (int, error) {
	if hello.Type != "hello" {
		return 0, fmt.Errorf("extchannel: expected hello frame, got %q", hello.Type)
	}
	if hello.Protocol <= 0 {
		return 0, fmt.Errorf("extchannel: hello missing protocol version")
	}
	if hello.Name == "" {
		return 0, fmt.Errorf("extchannel: hello missing sidecar name")
	}
	v := hello.Protocol
	if v > ProtocolVersion {
		v = ProtocolVersion
	}
	return v, nil
}
