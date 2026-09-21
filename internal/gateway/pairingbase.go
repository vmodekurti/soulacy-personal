package gateway

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// pairBase is the address a phone will use to redeem a pairing code.
type pairBase struct {
	URL        string
	Reachable  bool
	Hint       string
	Candidates []string
}

// pairBaseProbe reports whether a candidate base answers /healthz. Injected so
// tests never touch the network.
type pairBaseProbe func(ctx context.Context, base string) bool

// pairProbeBudget bounds one candidate probe. A wrong address must fail fast
// — the resolver tries several in a row while the Mobile page waits — so the
// handler caps each probe at min(runtime.timeouts.http, this).
const pairProbeBudget = 1500 * time.Millisecond

// boundedProbe wraps probe so every call gets its own deadline of d.
func boundedProbe(probe pairBaseProbe, d time.Duration) pairBaseProbe {
	return func(ctx context.Context, base string) bool {
		ctx, cancel := context.WithTimeout(ctx, d)
		defer cancel()
		return probe(ctx, base)
	}
}

// pairProbeFor builds the reachability probe the handler uses, given this
// gateway's own key fingerprint ("" when auto TLS is off). Tests replace it
// so the suite never touches the network.
var pairProbeFor = pairReachProbe

// pairReachProbe answers "does <base>/ping answer 200?" — over plain http, or
// over https when the certificate is ours (the auto certificate the phone
// will pin) or one the OS trusts (a proxy). A typed https address used to
// fail here simply because our own certificate is self-signed.
func pairReachProbe(fp string) pairBaseProbe {
	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig: pairTLSConfig(fp, true),
		DialContext:     pairDialContext,
	}}
	return func(ctx context.Context, base string) bool {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/ping", nil)
		if err != nil {
			return false
		}
		resp, err := client.Do(req)
		if err != nil {
			return false
		}
		_ = resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}
}

const loopbackHint = "This address only works on this computer. Enter the address your phone can reach (its Tailscale name or LAN IP), or set server.public_url in config.yaml. If the gateway listens on localhost, publish it first (remote.sh sets up Tailscale)."

// resolvePairBase picks the base URL to embed in a pairing QR.
//
// Precedence: an explicit override typed on the Mobile page, then
// server.public_url, then the first candidate that actually answers /healthz —
// the request's own origin when it is not loopback (the name the operator is
// already using), then Tailscale (100.64/10), then private LAN addresses. When nothing answers, the request origin is returned with
// Reachable=false and a hint so the page can warn instead of minting a code
// the phone cannot use.
func resolvePairBase(ctx context.Context, publicURL, requestBase string, port int, override string, probe pairBaseProbe) (pairBase, error) {
	if b := strings.TrimSpace(override); b != "" {
		// "my-mac.tailnet.ts.net:18789" is what people type; treat it as http —
		// the pairing step upgrades it to https itself when the address answers
		// this gateway's TLS.
		if !strings.Contains(b, "://") {
			b = "http://" + b
		}
		u, err := url.Parse(b)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return pairBase{}, fmt.Errorf("base_url must be an http(s) URL with a host")
		}
		base := u.Scheme + "://" + u.Host
		pb := pairBase{URL: base, Reachable: probe(ctx, base)}
		if isLoopbackPairHost(u.Hostname()) {
			pb.Reachable, pb.Hint = false, loopbackHint
		} else if !pb.Reachable {
			pb.Hint = "This gateway could not reach " + base + " itself; the phone may still reach it if the address is correct."
		}
		return pb, nil
	}
	if b := strings.TrimSpace(publicURL); b != "" {
		base := strings.TrimRight(b, "/")
		return pairBase{URL: base, Reachable: probe(ctx, base)}, nil
	}
	reqBase := strings.TrimRight(requestBase, "/")
	scheme, reqHost := "http", ""
	if u, err := url.Parse(reqBase); err == nil {
		if u.Scheme != "" {
			scheme = u.Scheme
		}
		reqHost = u.Hostname()
	}
	var candidates []string
	// The origin the operator is already using (e.g. a Tailscale MagicDNS
	// name) is the best answer when it isn't loopback — it's readable and
	// already known to work for them. Then Tailscale, then LAN.
	if reqHost != "" && !isLoopbackPairHost(reqHost) {
		candidates = append(candidates, reqBase)
	}
	for _, ip := range localAddresses() {
		candidates = append(candidates, scheme+"://"+net.JoinHostPort(ip.String(), strconv.Itoa(port)))
	}
	for _, c := range candidates {
		if probe(ctx, c) {
			return pairBase{URL: c, Reachable: true, Candidates: candidates}, nil
		}
	}
	return pairBase{URL: reqBase, Reachable: false, Hint: loopbackHint, Candidates: candidates}, nil
}

func isLoopbackPairHost(h string) bool {
	h = strings.ToLower(strings.Trim(strings.TrimSpace(h), "[]"))
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	if ip := net.ParseIP(h); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// localAddresses lists this machine's IPv4 addresses a phone could plausibly
// reach: Tailscale's CGNAT range (100.64/10) first, then private LAN
// addresses. Loopback, link-local and public addresses are skipped.
func localAddresses() []net.IP {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var ts, lan []net.IP
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip := ipn.IP.To4()
		if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
			continue
		}
		switch {
		case isTailscaleIP(ip):
			ts = append(ts, ip)
		case ip.IsPrivate():
			lan = append(lan, ip)
		}
	}
	return append(ts, lan...)
}

// isTailscaleIP reports an address in 100.64.0.0/10 (CGNAT, used by Tailscale).
func isTailscaleIP(ip net.IP) bool { return len(ip) == 4 && ip[0] == 100 && ip[1]&0xC0 == 64 }
