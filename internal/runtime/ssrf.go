package runtime

import (
	"net/http"
	"time"

	"github.com/soulacy/soulacy/internal/netguard"
)

// checkSSRF resolves rawURL and rejects requests to forbidden address ranges.
//
// The rules now live in internal/netguard, because the agent's HTTP tools were
// not the only place a model-supplied URL reaches an outbound request: the
// webhook-shaped channel adapters take a per-message destination override that
// comes from `channel.send`'s `to` argument. A copy of these rules in two
// packages would have drifted; one package that both import cannot.
//
//   - Cloud metadata / link-local / CGNAT are always blocked, v4 and v6.
//   - RFC-1918 and IPv6 ULA are blocked when ssrfProtection is true.
//   - Loopback is always allowed so local MCP servers work.
//   - allowedHosts exempts a host from the private-range rule only.
func checkSSRF(rawURL string, ssrfProtection bool, allowedHosts []string) error {
	return netguard.Check(rawURL, ssrfProtection, allowedHosts)
}

func (e *Engine) ssrfHTTPClient(timeout time.Duration) *http.Client {
	return netguard.NewHTTPClient(timeout, e.ssrfProtection, e.allowPrivateHosts)
}
