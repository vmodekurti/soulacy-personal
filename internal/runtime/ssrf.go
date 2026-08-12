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

// ssrfRedirectHook re-runs the check on every redirect hop.
//
// checkSSRF on its own is a pre-flight check, and Go's default client follows up
// to 10 redirects afterwards without telling anyone. A hostname that resolves to
// a public IP therefore passed the check and then 302'd wherever it liked —
// including to the metadata endpoint the check exists to protect. Every outbound
// client built from model-supplied URLs must carry this.
func (e *Engine) ssrfRedirectHook() func(*http.Request, []*http.Request) error {
	return netguard.CheckRedirect(e.ssrfProtection, e.allowPrivateHosts)
}

func (e *Engine) ssrfHTTPClient(timeout time.Duration) *http.Client {
	return netguard.NewHTTPClient(timeout, e.ssrfProtection, e.allowPrivateHosts)
}
