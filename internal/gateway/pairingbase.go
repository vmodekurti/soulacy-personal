package gateway

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
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
// already using), then this machine's Tailscale name (https://<name>, then
// <name>:<port>), then Tailscale (100.64/10) and private LAN addresses. When
// nothing answers, the request origin is returned with
// Reachable=false and a hint so the page can warn instead of minting a code
// the phone cannot use.
func resolvePairBase(ctx context.Context, publicURL, requestBase string, port int, override string, probe pairBaseProbe, tailnetName string) (pairBase, error) {
	if b := strings.TrimSpace(override); b != "" {
		// "my-mac.tailnet.ts.net" is what people type. Without a scheme, try
		// https first (port 443 is what remote.sh forwards, and the probe
		// accepts this gateway's own certificate), then http.
		schemeless := !strings.Contains(b, "://")
		if schemeless {
			b = "http://" + b
		}
		u, err := url.Parse(b)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return pairBase{}, fmt.Errorf("base_url must be an http(s) URL with a host")
		}
		base := u.Scheme + "://" + u.Host
		if schemeless && !isLoopbackPairHost(u.Hostname()) && probe(ctx, "https://"+u.Host) {
			base = "https://" + u.Host
			return pairBase{URL: base, Reachable: true}, nil
		}
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
		pb := pairBase{URL: base, Reachable: probe(ctx, base)}
		if !pb.Reachable {
			pb.Hint = selfProbeHint(base)
		}
		return pb, nil
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
	// A public name over https is the address, full stop. The gateway may
	// not be able to call it itself — behind Cloudflare the edge refuses
	// the box's own requests — and falling through to this machine's
	// addresses handed a phone the Docker bridge IP (172.18.0.4) that only
	// the container can reach (#222). Say so instead of guessing.
	if scheme == "https" && reqHost != "" && !isLoopbackPairHost(reqHost) && net.ParseIP(reqHost) == nil {
		pb := pairBase{URL: reqBase, Reachable: probe(ctx, reqBase), Candidates: candidates}
		if !pb.Reachable {
			pb.Hint = selfProbeHint(reqBase)
		}
		return pb, nil
	}
	// This machine's Tailscale name, before any IP: https://<name> when the
	// tailnet forwards 443 to us (remote.sh does), else the name with our
	// port. A name is readable and gives nothing away; an IP:port does. (#178)
	if tailnetName != "" {
		candidates = append(candidates, "https://"+tailnetName, "http://"+net.JoinHostPort(tailnetName, strconv.Itoa(port)))
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

// selfProbeHint explains a public address the gateway cannot call itself.
func selfProbeHint(base string) string {
	return "This gateway could not reach " + base + " itself (a proxy or CDN may refuse its own calls); a phone can, if the address is the one you use."
}

// inContainer reports a Docker container, where this machine's "LAN"
// addresses are the bridge network nobody outside can reach (#222).
func inContainer() bool {
	_, err := os.Stat("/.dockerenv")
	return err == nil
}

// localAddresses lists this machine's IPv4 addresses a phone could plausibly
// reach: Tailscale's CGNAT range (100.64/10) first, then private LAN
// addresses. Loopback, link-local and public addresses are skipped, and so
// are private addresses inside a container: they are the bridge network.
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
			if inContainer() {
				continue
			}
			lan = append(lan, ip)
		}
	}
	return append(ts, lan...)
}

// isTailscaleIP reports an address in 100.64.0.0/10 (CGNAT, used by Tailscale).
func isTailscaleIP(ip net.IP) bool { return len(ip) == 4 && ip[0] == 100 && ip[1]&0xC0 == 64 }
