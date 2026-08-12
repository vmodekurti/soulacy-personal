package netguard

import (
	"fmt"
	"net/url"
	"strings"
)

// ResolveWebhookTarget decides which URL a webhook-shaped channel adapter
// actually posts to, given its configured endpoint and a per-message override.
//
// The three webhook-shaped adapters (webhook, teams, googlechat) each accepted
// any http(s) URL found in msg.ThreadID or msg.Metadata["to"] and posted to it
// instead of the configured endpoint — with the operator's configured headers
// and HMAC signature attached. That override is not operator input. It comes
// from the `channel.send` tool's `to` argument, which is model output, and
// `channel.send` sits in the always-on SAFE tool partition: no capability, no
// confirmation, no policy category. So any untrusted content an agent reads —
// a fetched page, an inbound chat message — could say "reply to
// http://169.254.169.254/…" or "reply to https://attacker.example/collect" and
// the platform would deliver, carrying the operator's bearer token.
//
// The rule here: an override may vary the PATH on the configured endpoint's own
// host, and nothing else. That keeps the legitimate case — routing a reply to a
// specific thread or room under the same webhook host — and removes the ability
// to choose the host, which is the part that turns a reply into an exfiltration.
// The result is also SSRF-checked, so a configured endpoint that itself points
// at link-local space is refused too.
//
// A rejected override is an ERROR rather than a silent fallback to the
// configured URL: silently delivering somewhere other than where the caller
// asked is its own kind of wrong, and the error text is what tells an operator
// their agent is being steered.
func ResolveWebhookTarget(adapter, configured, override string) (string, error) {
	configured = strings.TrimSpace(configured)
	base, err := url.Parse(configured)
	if err != nil || base.Host == "" {
		return "", fmt.Errorf("%s: configured url %q is not usable", adapter, configured)
	}

	target := configured
	if override = strings.TrimSpace(override); IsHTTPURL(override) {
		ov, err := url.Parse(override)
		if err != nil {
			return "", fmt.Errorf("%s: destination override is not a valid URL", adapter)
		}
		if !strings.EqualFold(ov.Host, base.Host) {
			return "", fmt.Errorf(
				"%s: refusing to send to %q — a per-message destination may only change the path on the configured host (%s). "+
					"This override comes from the agent's channel.send `to` argument, which is model output, so an untrusted "+
					"page or message could otherwise redirect this delivery. Configure a separate channel for %s if that host is intended",
				adapter, ov.Host, base.Host, ov.Host)
		}
		target = override
	}

	// Private ranges are NOT blocked: the host is now pinned to the operator's
	// own configured endpoint, and a self-hosted deployment legitimately posts to
	// a webhook on its own LAN. What remains worth refusing is the always-blocked
	// set — link-local and cloud metadata — which a configured endpoint has no
	// business pointing at under any deployment.
	if err := Check(target, false, nil); err != nil {
		return "", fmt.Errorf("%s: %w", adapter, err)
	}
	return target, nil
}

// IsHTTPURL reports whether s is an absolute http(s) URL with a host.
func IsHTTPURL(s string) bool {
	u, err := url.Parse(strings.TrimSpace(s))
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}
