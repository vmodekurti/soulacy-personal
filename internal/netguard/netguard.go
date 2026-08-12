// Package netguard decides whether an outbound HTTP request is allowed to a
// given URL.
//
// It exists because the SSRF check used to live inside internal/runtime, where
// only the agent's HTTP tools could reach it — and the HTTP tools turned out not
// to be the only place a URL from untrusted input reaches an outbound request.
// The webhook-shaped channel adapters (webhook, teams, googlechat) accept a
// per-message destination override, and that override comes from the
// `channel.send` tool's `to` argument, i.e. straight from model output. An agent
// that reads a poisoned web page could be told to POST the conversation to
// http://169.254.169.254/… or to any internal address, with the operator's
// configured auth headers attached.
//
// Two things follow from that, and both are implemented here rather than at each
// call site, because a check that is easy to forget will be forgotten:
//
//  1. Redirects are re-checked. The old check ran once, before the request, and
//     Go's default client then followed up to 10 redirects with no further
//     validation — so a public hostname that 302s to the metadata endpoint sailed
//     straight through. CheckRedirect below closes that.
//  2. IPv6 metadata and link-local addresses are blocked unconditionally. The old
//     always-blocked list held only 169.254.0.0/16 and CGNAT, so AWS/GCP IMDS
//     over IPv6 (fd00:ec2::254) was reachable on the default configuration,
//     because that address only fell in the *private* list and private-range
//     blocking is off by default.
package netguard

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
)

var (
	// alwaysBlocked is refused no matter how the deployment is configured.
	// These are addresses that carry credentials or exist only inside the host's
	// own network fabric; there is no legitimate agent use for any of them.
	alwaysBlocked []*net.IPNet

	// privateRanges is refused only when blockPrivate is set. A self-hosted
	// deployment legitimately talks to 192.168.x services, so this stays opt-in.
	privateRanges []*net.IPNet
)

func init() {
	for _, cidr := range []string{
		"169.254.0.0/16", // link-local — AWS/GCP/Azure IMDS lives at 169.254.169.254
		"100.64.0.0/10",  // CGNAT (RFC 6598)
		"fe80::/10",      // IPv6 link-local — the v6 half of the above
		"fd00:ec2::/32",  // AWS IMDS over IPv6 (fd00:ec2::254)
		"0.0.0.0/8",      // "this network" — resolves to the local host on Linux
	} {
		if _, block, err := net.ParseCIDR(cidr); err == nil && block != nil {
			alwaysBlocked = append(alwaysBlocked, block)
		}
	}
	for _, cidr := range []string{
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"fc00::/7", // IPv6 unique-local (includes fd00::/8)
	} {
		if _, block, err := net.ParseCIDR(cidr); err == nil && block != nil {
			privateRanges = append(privateRanges, block)
		}
	}
}

// Check resolves rawURL and reports why the request must not be made, or nil.
//
//   - Cloud-metadata, link-local and CGNAT addresses are always refused, in both
//     address families.
//   - RFC-1918 / ULA private ranges are refused only when blockPrivate is true.
//   - Loopback is always allowed: local MCP servers and sidecars live there.
//   - allowedHosts exempts a hostname from the private-range rule only. It does
//     NOT unblock the metadata endpoint — an allow-list entry is an operator
//     saying "this internal service is fine", not "hand out my instance role".
func Check(rawURL string, blockPrivate bool, allowedHosts []string) error {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return fmt.Errorf("ssrf: invalid URL: %w", err)
	}
	return CheckURL(u, blockPrivate, allowedHosts)
}

// CheckURL is Check for an already-parsed URL, which is the shape the redirect
// hook receives.
func CheckURL(u *url.URL, blockPrivate bool, allowedHosts []string) error {
	if u == nil {
		return fmt.Errorf("ssrf: no URL")
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("ssrf: URL has no host")
	}

	exempt := false
	for _, h := range allowedHosts {
		if strings.EqualFold(strings.TrimSpace(h), host) {
			exempt = true
			break
		}
	}

	ips, err := net.LookupHost(host)
	if err != nil {
		// Resolution failed: let the request itself fail naturally rather than
		// reporting a confusing security error for what is really a typo or a
		// DNS outage.
		return nil
	}

	for _, ipStr := range ips {
		ip := net.ParseIP(ipStr)
		if ip == nil {
			continue
		}
		if ip.IsLoopback() {
			continue
		}
		// Normalise a v4-mapped v6 address (::ffff:169.254.169.254) down to its
		// v4 form so the v4 CIDRs below actually match it.
		if v4 := ip.To4(); v4 != nil {
			ip = v4
		}
		for _, block := range alwaysBlocked {
			if block.Contains(ip) {
				return fmt.Errorf("ssrf: request to %s (%s) is blocked — cloud metadata and link-local addresses are never allowed", host, ipStr)
			}
		}
		if blockPrivate && !exempt {
			for _, block := range privateRanges {
				if block.Contains(ip) {
					return fmt.Errorf("ssrf: request to %s (%s) is blocked — private network addresses require ssrf_protection: false or an explicit allow_private_hosts entry", host, ipStr)
				}
			}
		}
	}
	return nil
}

// CheckRedirect builds the http.Client hook that re-validates every hop.
//
// Without this the pre-flight check is decorative: an attacker-controlled
// hostname resolves to a public IP, passes, and then 302s to
// http://169.254.169.254/latest/meta-data/iam/security-credentials/ — which Go's
// default client follows without asking anyone.
func CheckRedirect(blockPrivate bool, allowedHosts []string) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("stopped after 10 redirects")
		}
		return CheckURL(req.URL, blockPrivate, allowedHosts)
	}
}
