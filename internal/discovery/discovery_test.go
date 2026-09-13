package discovery

import (
	"net"
	"strings"
	"testing"

	"github.com/miekg/dns"
)

func fixture() (Options, net.Interface, []net.Addr) {
	_, v4, _ := net.ParseCIDR("192.168.1.7/24")
	v4.IP = net.ParseIP("192.168.1.7")
	_, v6, _ := net.ParseCIDR("fd00::7/64")
	v6.IP = net.ParseIP("fd00::7")
	return Options{Authenticated: true, BindHost: "0.0.0.0", Port: 18789, Hostname: "test-gateway.local"},
		net.Interface{Name: "lan-test", Flags: net.FlagUp | net.FlagMulticast}, []net.Addr{v4, v6}
}

func TestDiscoveryRecords(t *testing.T) {
	opts, iface, addrs := fixture()
	cfg, err := prepare(opts, iface, addrs)
	if err != nil {
		t.Fatal(err)
	}
	zone := cfg.Zone.(*addressZone)
	if cfg.Iface.Name != iface.Name || len(zone.IPs) != 1 || !zone.IPs[0].Equal(net.ParseIP("192.168.1.7")) {
		t.Fatalf("wrong interface/addresses: %+v", zone)
	}
	if strings.Join(zone.TXT, ",") != "scheme=http,protocol=1" || zone.HostName != "test-gateway.local." {
		t.Fatalf("unexpected public metadata: %+v", zone)
	}
	rrs := zone.Records(dns.Question{Name: ServiceType + ".local.", Qtype: dns.TypePTR, Qclass: dns.ClassINET})
	if len(rrs) == 0 {
		t.Fatal("no service records")
	}
	msg := &dns.Msg{Answer: rrs}
	wire, err := msg.Pack()
	if err != nil {
		t.Fatal(err)
	}
	var parsed dns.Msg
	if err := parsed.Unpack(wire); err != nil {
		t.Fatal(err)
	}
	var port, txt, address bool
	for _, rr := range parsed.Answer {
		switch record := rr.(type) {
		case *dns.SRV:
			port = record.Port == 18789 && record.Target == zone.HostName
		case *dns.TXT:
			txt = strings.Join(record.Txt, ",") == "scheme=http,protocol=1"
		case *dns.A:
			address = record.A.Equal(net.ParseIP("192.168.1.7"))
		}
	}
	if !port || !txt || !address {
		t.Fatalf("incomplete DNS-SD response: %s", &parsed)
	}
}

func TestDiscoveryFailsClosed(t *testing.T) {
	for _, name := range []string{"no-auth", "loopback", "public", "hostname", "no-interface-address", "down", "no-multicast", "loopback-interface", "port-zero", "port-overflow", "invalid-name", "long-name", "nonlocal-host", "nested-host", "escaped-host", "stable-host-required", "tls-host-required"} {
		t.Run(name, func(t *testing.T) {
			opts, iface, addrs := fixture()
			switch name {
			case "no-auth":
				opts.Authenticated = false
			case "loopback":
				opts.BindHost = "127.0.0.1"
			case "public":
				opts.BindHost = "8.8.8.8"
			case "hostname":
				opts.BindHost = "gateway.local"
			case "no-interface-address":
				opts.BindHost = "192.168.5.7"
			case "down":
				iface.Flags &^= net.FlagUp
			case "no-multicast":
				iface.Flags &^= net.FlagMulticast
			case "loopback-interface":
				iface.Flags |= net.FlagLoopback
			case "port-zero":
				opts.Port = 0
			case "port-overflow":
				opts.Port = 65536
			case "invalid-name":
				opts.Name = "bad.\\name\n"
			case "long-name":
				opts.Name = strings.Repeat("A", 43)
			case "nonlocal-host":
				opts.Hostname = "example.com"
			case "nested-host":
				opts.Hostname = "a.b.local"
			case "escaped-host":
				opts.Hostname = "host%2E.local"
			case "tls-host-required":
				opts.TLS = true
				opts.Hostname = ""
			case "stable-host-required":
				opts.Hostname = ""
			}
			if _, err := prepare(opts, iface, addrs); err == nil {
				t.Fatal("unsafe advertisement accepted")
			}
		})
	}
}

func TestIPv6TLSAndUniqueInstances(t *testing.T) {
	opts, iface, addrs := fixture()
	opts.BindHost, opts.TLS, opts.Hostname, opts.Name = "::", true, "Gateway.local.", "Office"
	first, err := prepare(opts, iface, addrs)
	if err != nil {
		t.Fatal(err)
	}
	second, err := prepare(opts, iface, addrs)
	if err != nil {
		t.Fatal(err)
	}
	a, b := first.Zone.(*addressZone), second.Zone.(*addressZone)
	if a.Instance == b.Instance || a.HostName != "gateway.local." || a.TXT[0] != "scheme=https" || len(a.IPs) != 1 || a.IPs[0].To4() != nil {
		t.Fatalf("invalid IPv6/TLS identity: %+v", a)
	}
}

func TestNegativeAddressAnswersDoNotStallDualStackResolvers(t *testing.T) {
	for _, v6 := range []bool{false, true} {
		opts, iface, addrs := fixture()
		missing, present := uint16(dns.TypeAAAA), uint16(dns.TypeA)
		if v6 {
			opts.BindHost = "::"
			missing, present = present, missing
		}
		cfg, err := prepare(opts, iface, addrs)
		if err != nil {
			t.Fatal(err)
		}
		records := cfg.Zone.Records(dns.Question{Name: "TEST-GATEWAY.local.", Qtype: missing, Qclass: dns.ClassINET})
		if len(records) != 1 {
			t.Fatalf("missing negative response: %v", records)
		}
		nsec, ok := records[0].(*dns.NSEC)
		if !ok || nsec.NextDomain != "test-gateway.local." || nsec.Hdr.Ttl != 120 || nsec.Hdr.Class != dns.ClassINET|0x8000 || len(nsec.TypeBitMap) != 1 || nsec.TypeBitMap[0] != present {
			t.Fatalf("invalid negative answer: %v", records)
		}
		wire, err := (&dns.Msg{Answer: records}).Pack()
		if err != nil {
			t.Fatal(err)
		}
		if err := new(dns.Msg).Unpack(wire); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"unrelated.local.", ServiceType + ".local."} {
			if rr := cfg.Zone.Records(dns.Question{Name: name, Qtype: missing, Qclass: dns.ClassINET}); len(rr) != 0 {
				t.Fatalf("negative answer for unowned/shared name: %v", rr)
			}
		}
	}
}

func TestPrepareRequiresExplicitInterfaceAndAuth(t *testing.T) {
	if _, err := Prepare(Options{Authenticated: true}); err == nil {
		t.Fatal("missing interface accepted")
	}
	if _, err := Prepare(Options{Interface: "nonexistent-interface"}); err == nil || !strings.Contains(err.Error(), "authentication") {
		t.Fatalf("auth not checked first: %v", err)
	}
}
