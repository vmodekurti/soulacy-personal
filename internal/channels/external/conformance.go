package external

import (
	"context"

	"github.com/soulacy/soulacy/sdk/extchannel/sidecartest"
)

// RunConformance exercises a sidecar command against the External Channel
// Protocol v1 contract. The implementation was promoted to the SDK in
// Story E11 (sdk/extchannel/sidecartest) so sidecar authors run the SAME
// checks out-of-tree; this re-export keeps the host-internal call sites and
// the historical name.
func RunConformance(ctx context.Context, command string, args ...string) error {
	return sidecartest.RunConformance(ctx, command, args...)
}
