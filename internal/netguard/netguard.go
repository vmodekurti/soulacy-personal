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
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type resolver interface {
	LookupIPAddr(context.Context, string) ([]net.IPAddr, error)
}

type dialContextFunc func(context.Context, string, string) (net.Conn, error)

// GuardedTransport validates and pins DNS exactly once for each outbound
// request. Redirects pass through RoundTrip again and therefore receive their
// own independently validated, pinned address.
type GuardedTransport struct {
	Base         *http.Transport
	Resolver     resolver
	DialContext  dialContextFunc
	BlockPrivate bool
	AllowedHosts []string
}

// NewHTTPClient returns a client suitable for URLs influenced by users,
// models, or remote content. The validated address is the address dialled.
func NewHTTPClient(timeout time.Duration, blockPrivate bool, allowedHosts []string) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &GuardedTransport{
			BlockPrivate: blockPrivate,
			AllowedHosts: append([]string(nil), allowedHosts...),
		},
		CheckRedirect: CheckRedirect(blockPrivate, allowedHosts),
	}
}

func (t *GuardedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req == nil || req.URL == nil {
		return nil, fmt.Errorf("ssrf: request has no URL")
	}
	ips, err := resolveAllowed(req.Context(), req.URL, t.BlockPrivate, t.AllowedHosts, t.resolver())
	if err != nil {
		return nil, err
	}
	base := t.Base
	if base == nil {
		base, _ = http.DefaultTransport.(*http.Transport)
	}
	if base == nil {
		base = &http.Transport{}
	}
	transport := base.Clone()
	transport.DisableKeepAlives = true
	dial := t.DialContext
	if dial == nil {
		dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
		dial = dialer.DialContext
	}
	expectedHost := strings.TrimSuffix(strings.ToLower(req.URL.Hostname()), ".")
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, fmt.Errorf("ssrf: invalid dial address: %w", err)
		}
		if strings.TrimSuffix(strings.ToLower(host), ".") != expectedHost {
			return nil, fmt.Errorf("ssrf: transport attempted unexpected host %q", host)
		}
		var lastErr error
		for _, ip := range ips {
			conn, err := dial(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return conn, nil
			}
			lastErr = err
		}
		return nil, fmt.Errorf("ssrf: dial validated addresses: %w", lastErr)
	}
	return transport.RoundTrip(req)
}

func (t *GuardedTransport) resolver() resolver {
	if t.Resolver != nil {
		return t.Resolver
	}
	return net.DefaultResolver
}

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
	_, err := resolveAllowed(context.Background(), u, blockPrivate, allowedHosts, net.DefaultResolver)
	return err
}

func resolveAllowed(ctx context.Context, u *url.URL, blockPrivate bool, allowedHosts []string, r resolver) ([]net.IP, error) {
	if u == nil {
		return nil, fmt.Errorf("ssrf: no URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("ssrf: unsupported URL scheme %q", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return nil, fmt.Errorf("ssrf: URL has no host")
	}

	exempt := false
	for _, h := range allowedHosts {
		if strings.EqualFold(strings.TrimSpace(h), host) {
			exempt = true
			break
		}
	}

	addresses, err := r.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("ssrf: resolve %s: %w", host, err)
	}
	if len(addresses) == 0 {
		return nil, fmt.Errorf("ssrf: resolve %s returned no addresses", host)
	}
	approved := make([]net.IP, 0, len(addresses))
	for _, address := range addresses {
		ip := address.IP
		if ip == nil {
			return nil, fmt.Errorf("ssrf: resolver returned an invalid address for %s", host)
		}
		ipStr := ip.String()
		if ip.IsLoopback() {
			approved = append(approved, append(net.IP(nil), ip...))
			continue
		}
		// Normalise a v4-mapped v6 address (::ffff:169.254.169.254) down to its
		// v4 form so the v4 CIDRs below actually match it.
		if v4 := ip.To4(); v4 != nil {
			ip = v4
		}
		for _, block := range alwaysBlocked {
			if block.Contains(ip) {
				return nil, fmt.Errorf("ssrf: request to %s (%s) is blocked — cloud metadata and link-local addresses are never allowed", host, ipStr)
			}
		}
		if blockPrivate && !exempt {
			for _, block := range privateRanges {
				if block.Contains(ip) {
					return nil, fmt.Errorf("ssrf: request to %s (%s) is blocked — private network addresses require ssrf_protection: false or an explicit allow_private_hosts entry", host, ipStr)
				}
			}
		}
		approved = append(approved, append(net.IP(nil), ip...))
	}
	return approved, nil
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
		if len(via) > 0 && strings.EqualFold(via[len(via)-1].URL.Scheme, "https") && !strings.EqualFold(req.URL.Scheme, "https") {
			return fmt.Errorf("ssrf: refusing redirect downgrade from HTTPS to %s", req.URL.Scheme)
		}
		return CheckURL(req.URL, blockPrivate, allowedHosts)
	}
}
