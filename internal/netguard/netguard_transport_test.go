package netguard

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

type sequenceResolver struct {
	mu      sync.Mutex
	answers [][]net.IPAddr
	err     error
	calls   int
}

func (r *sequenceResolver) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if r.err != nil {
		return nil, r.err
	}
	idx := r.calls - 1
	if idx >= len(r.answers) {
		idx = len(r.answers) - 1
	}
	return append([]net.IPAddr(nil), r.answers[idx]...), nil
}

func TestGuardedTransportPinsTheValidatedAddress(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }))
	defer server.Close()
	u, _ := url.Parse(server.URL)
	_, port, _ := net.SplitHostPort(u.Host)
	resolver := &sequenceResolver{answers: [][]net.IPAddr{
		{{IP: net.ParseIP("127.0.0.1")}},
		{{IP: net.ParseIP("169.254.169.254")}}, // would be returned by a second lookup
	}}
	var dialled string
	transport := &GuardedTransport{
		Resolver: resolver,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			dialled = address
			return (&net.Dialer{}).DialContext(ctx, network, address)
		},
	}
	client := &http.Client{Transport: transport}
	resp, err := client.Get("http://rebind.test:" + port + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resolver.calls != 1 {
		t.Fatalf("DNS lookups = %d, want exactly one", resolver.calls)
	}
	if !strings.HasPrefix(dialled, "127.0.0.1:") {
		t.Fatalf("dialled %q instead of validated address", dialled)
	}
}

func TestResolveAllowedDeniesMixedPublicAndPrivateAnswers(t *testing.T) {
	r := &sequenceResolver{answers: [][]net.IPAddr{{
		{IP: net.ParseIP("93.184.216.34")},
		{IP: net.ParseIP("10.0.0.8")},
	}}}
	u, _ := url.Parse("https://mixed.test/")
	if _, err := resolveAllowed(context.Background(), u, true, nil, r); err == nil {
		t.Fatal("mixed public/private DNS answer was accepted")
	}
}

func TestResolveAllowedFailsClosedOnDNSFailure(t *testing.T) {
	r := &sequenceResolver{err: errors.New("DNS unavailable")}
	u, _ := url.Parse("https://missing.test/")
	if _, err := resolveAllowed(context.Background(), u, true, nil, r); err == nil {
		t.Fatal("DNS failure was allowed")
	}
}

func TestPublicPolicyRejectsLoopbackEvenThoughOperatorPolicyAllowsIt(t *testing.T) {
	r := &sequenceResolver{answers: [][]net.IPAddr{{{IP: net.ParseIP("127.0.0.1")}}}}
	u, _ := url.Parse("https://tenant-selected.test/mcp")
	if _, err := resolveAllowedPolicy(context.Background(), u, true, true, nil, r); err == nil {
		t.Fatal("workspace public policy accepted a loopback destination")
	}

	r.calls = 0
	if _, err := resolveAllowed(context.Background(), u, true, nil, r); err != nil {
		t.Fatalf("operator sidecar compatibility policy unexpectedly rejected loopback: %v", err)
	}
}

func TestRedirectRefusesHTTPSDowngrade(t *testing.T) {
	previous, _ := http.NewRequest(http.MethodGet, "https://public.example/start", nil)
	next, _ := http.NewRequest(http.MethodGet, "http://public.example/next", nil)
	if err := CheckRedirect(true, nil)(next, []*http.Request{previous}); err == nil {
		t.Fatal("HTTPS to HTTP redirect downgrade was accepted")
	}
}
