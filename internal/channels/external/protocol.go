// protocol.go — External Channel Protocol v1 re-exports. The canonical wire
// types were promoted to the SDK in Story E11 (sdk/extchannel) so sidecar
// authors and the conformance kit share one definition; these are type
// ALIASES — identical types, zero conversion cost (E9 pattern).
package external

import (
	"io"

	"github.com/soulacy/soulacy/sdk/extchannel"
)

// ProtocolVersion is the gateway's current External Channel Protocol
// version. Negotiation picks min(gateway, sidecar).
const ProtocolVersion = extchannel.ProtocolVersion

// Frame is the superset of all protocol frame fields. Type discriminates;
// unused fields are omitted on the wire.
type Frame = extchannel.Frame

// ParseFrame decodes one NDJSON line. Unknown frame types are NOT an error —
// callers skip frames they don't understand (forward compatibility). A frame
// without a type is malformed.
func ParseFrame(line []byte) (Frame, error) { return extchannel.ParseFrame(line) }

// WriteFrame encodes a frame as exactly one NDJSON line.
func WriteFrame(w io.Writer, f Frame) error { return extchannel.WriteFrame(w, f) }

// Negotiate validates a sidecar's hello frame and returns the protocol
// version both sides will speak: min(gateway, sidecar).
func Negotiate(hello Frame) (int, error) { return extchannel.Negotiate(hello) }
