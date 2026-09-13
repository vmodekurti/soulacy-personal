// Package discovery advertises an explicitly enabled, authenticated gateway on
// one private LAN interface. DNS-SD is a hint, never an identity or auth channel.
package discovery

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net"
	"strings"

	"github.com/hashicorp/mdns"
	"github.com/miekg/dns"
)

const ServiceType = "_soulacy-gw._tcp"

type Options struct {
	Interface     string
	Name          string
	Hostname      string
	BindHost      string
	Port          int
	TLS           bool
	Authenticated bool
}

// Prepare validates the actual listener and snapshots the selected interface.
// No DNS lookups, multicast sockets, credentials, or machine names are used.
// Call only after the HTTP listener has successfully bound.
func Prepare(opts Options) (*mdns.Config, error) {
	if !opts.Authenticated {
		return nil, fmt.Errorf("LAN discovery requires effective gateway authentication, even with allow_unauthenticated")
	}
	if opts.Interface == "" {
		return nil, fmt.Errorf("LAN discovery requires server.discovery.interface")
	}
	iface, err := net.InterfaceByName(opts.Interface)
	if err != nil {
		return nil, fmt.Errorf("LAN discovery interface: %w", err)
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return nil, fmt.Errorf("LAN discovery interface addresses: %w", err)
	}
	return prepare(opts, *iface, addrs)
}

func prepare(opts Options, iface net.Interface, addrs []net.Addr) (*mdns.Config, error) {
	if !opts.Authenticated {
		return nil, fmt.Errorf("LAN discovery requires effective gateway authentication")
	}
	if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagMulticast == 0 || iface.Flags&net.FlagLoopback != 0 {
		return nil, fmt.Errorf("LAN discovery interface must be up, multicast-capable, and non-loopback")
	}
	if opts.Port < 1 || opts.Port > 65535 {
		return nil, fmt.Errorf("LAN discovery port must be between 1 and 65535")
	}
	bind := net.ParseIP(strings.Trim(opts.BindHost, "[]"))
	if bind == nil || (!bind.IsUnspecified() && !bind.IsPrivate()) || bind.IsLoopback() {
		return nil, fmt.Errorf("LAN discovery requires a private-IP or wildcard listener, not a hostname, public IP, or loopback")
	}
	var ips []net.IP
	for _, addr := range addrs {
		ip, _, err := net.ParseCIDR(addr.String())
		if err != nil || !ip.IsPrivate() || ip.IsLoopback() {
			continue
		}
		// Wildcard listeners advertise only their explicit address family. This
		// does not assume the host OS enables dual-stack IPv6 sockets.
		if (bind.To4() == nil) != (ip.To4() == nil) || (!bind.IsUnspecified() && !bind.Equal(ip)) {
			continue
		}
		ips = append(ips, ip)
		if len(ips) == 4 {
			break
		}
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("LAN discovery listener has no matching private address on interface %s", iface.Name)
	}
	name := opts.Name
	if name == "" {
		name = "Soulacy"
	}
	if len(name) > 42 || strings.TrimSpace(name) != name || !safeName(name) {
		return nil, fmt.Errorf("server.discovery.name must be 1–42 ASCII letters, digits, spaces, hyphens, or underscores")
	}
	var nonce [6]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, fmt.Errorf("LAN discovery instance identity: %w", err)
	}
	suffix := hex.EncodeToString(nonce[:])
	host := strings.TrimSuffix(strings.ToLower(opts.Hostname), ".")
	if !validHost(host) {
		return nil, fmt.Errorf("server.discovery.hostname requires a stable, unique DNS label followed by .local")
	}
	scheme := "http"
	if opts.TLS {
		scheme = "https"
	}
	zone, err := mdns.NewMDNSService(name+"-"+suffix, ServiceType, "local.", host+".", opts.Port, ips,
		[]string{"scheme=" + scheme, "protocol=1"})
	if err != nil {
		return nil, fmt.Errorf("LAN discovery records: %w", err)
	}
	// Never log arbitrary packets from unauthenticated peers. Startup failures
	// are returned to the gateway; record contents carry no secrets or paths.
	return &mdns.Config{Zone: &addressZone{zone}, Iface: &iface, Logger: log.New(io.Discard, "", 0)}, nil
}

// addressZone supplies negative address answers for the operator-assigned,
// unique hostname. Without these, dual-stack resolvers can time out waiting
// for AAAA on an IPv4-only listener (or A on IPv6-only). Never assert absence
// for a shared service name or a name this gateway does not own.
// The synthesized NSEC format is specified in RFC 6762 section 6.1.
type addressZone struct{ *mdns.MDNSService }

func (z *addressZone) Records(q dns.Question) []dns.RR {
	if !strings.EqualFold(q.Name, z.HostName) || (q.Qtype != dns.TypeA && q.Qtype != dns.TypeAAAA && q.Qtype != dns.TypeANY) {
		return z.MDNSService.Records(q)
	}
	if class := q.Qclass & 0x7fff; class != dns.ClassINET && class != dns.ClassANY {
		return nil
	}
	q.Name = z.HostName
	a := z.MDNSService.Records(dns.Question{Name: z.HostName, Qtype: dns.TypeA})
	aaaa := z.MDNSService.Records(dns.Question{Name: z.HostName, Qtype: dns.TypeAAAA})
	var types []uint16
	if len(a) > 0 {
		types = append(types, dns.TypeA)
	}
	if len(aaaa) > 0 {
		types = append(types, dns.TypeAAAA)
	}
	negative := &dns.NSEC{
		Hdr:        dns.RR_Header{Name: z.HostName, Rrtype: dns.TypeNSEC, Class: dns.ClassINET | 0x8000, Ttl: 120},
		NextDomain: z.HostName, TypeBitMap: types, // synthesized NSEC must NOT include the NSEC bit
	}
	switch {
	case q.Qtype == dns.TypeANY:
		return append(append(a, aaaa...), negative)
	case q.Qtype == dns.TypeA && len(a) > 0:
		return a
	case q.Qtype == dns.TypeAAAA && len(aaaa) > 0:
		return aaaa
	default:
		return []dns.RR{negative}
	}
}

func safeName(name string) bool {
	for _, ch := range name {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == ' ' || ch == '-' || ch == '_') {
			return false
		}
	}
	return name != ""
}

func validHost(host string) bool {
	if !strings.HasSuffix(host, ".local") {
		return false
	}
	label := strings.TrimSuffix(host, ".local")
	if len(label) < 1 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
		return false
	}
	for _, ch := range label {
		if !(ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' || ch == '-') {
			return false
		}
	}
	return true
}
