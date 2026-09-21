package gateway

import (
	"context"
	"testing"
)

// A pairing code must carry an address the phone can reach; the browser's own
// loopback origin never is. (Issue #160.)
func TestResolvePairBase(t *testing.T) {
	ctx := context.Background()
	answers := func(set ...string) pairBaseProbe {
		ok := map[string]bool{}
		for _, s := range set {
			ok[s] = true
		}
		return func(_ context.Context, base string) bool { return ok[base] }
	}
	const loopback = "http://localhost:18789"

	t.Run("an explicit address wins and is validated", func(t *testing.T) {
		pb, err := resolvePairBase(ctx, "", loopback, 18789, "http://mac.tailnet.ts.net:18789", answers("http://mac.tailnet.ts.net:18789"))
		if err != nil || pb.URL != "http://mac.tailnet.ts.net:18789" || !pb.Reachable {
			t.Fatalf("%+v %v", pb, err)
		}
		// Typed without a scheme, as people do: treated as http (#174).
		pb, err = resolvePairBase(ctx, "", loopback, 18789, "mac.tailnet.ts.net:18789", answers("http://mac.tailnet.ts.net:18789"))
		if err != nil || pb.URL != "http://mac.tailnet.ts.net:18789" || !pb.Reachable {
			t.Fatalf("scheme-less override: %+v err=%v", pb, err)
		}
		if _, err := resolvePairBase(ctx, "", loopback, 18789, "not a url", answers()); err == nil {
			t.Fatal("an invalid base_url must be rejected")
		}
		pb, _ = resolvePairBase(ctx, "", "http://host:18789", 18789, "http://127.0.0.1:18789", answers("http://127.0.0.1:18789"))
		if pb.Reachable || pb.Hint == "" {
			t.Fatalf("a loopback override must be flagged, got %+v", pb)
		}
	})
	t.Run("server.public_url is used when set", func(t *testing.T) {
		pb, err := resolvePairBase(ctx, "https://soul.example.com/", loopback, 18789, "", answers("https://soul.example.com"))
		if err != nil || pb.URL != "https://soul.example.com" || !pb.Reachable {
			t.Fatalf("%+v %v", pb, err)
		}
	})
	t.Run("a loopback origin is never chosen while another address answers", func(t *testing.T) {
		pb, err := resolvePairBase(ctx, "", loopback, 18789, "", func(_ context.Context, base string) bool { return base != loopback })
		if err != nil {
			t.Fatal(err)
		}
		if len(localAddresses()) > 0 && (pb.URL == loopback || !pb.Reachable) {
			t.Fatalf("expected a reachable non-loopback base, got %+v", pb)
		}
	})
	t.Run("nothing reachable flags the loopback origin with a hint", func(t *testing.T) {
		pb, err := resolvePairBase(ctx, "", loopback, 18789, "", answers())
		if err != nil || pb.Reachable || pb.URL != loopback || pb.Hint == "" {
			t.Fatalf("%+v %v", pb, err)
		}
	})
	t.Run("a non-loopback origin is a candidate", func(t *testing.T) {
		pb, err := resolvePairBase(ctx, "", "http://192.168.1.10:18789", 18789, "", answers("http://192.168.1.10:18789"))
		if err != nil || pb.URL != "http://192.168.1.10:18789" || !pb.Reachable {
			t.Fatalf("%+v %v", pb, err)
		}
	})
}

func TestIsLoopbackPairHost(t *testing.T) {
	for h, want := range map[string]bool{"localhost": true, "LOCALHOST": true, "127.0.0.1": true, "::1": true, "[::1]": true, "app.localhost": true, "192.168.1.5": false, "mac.tailnet.ts.net": false, "100.101.102.103": false, "": false} {
		if got := isLoopbackPairHost(h); got != want {
			t.Errorf("isLoopbackPairHost(%q) = %v, want %v", h, got, want)
		}
	}
}
