package gateway

import "context"

// Handler tests must never probe real addresses (LAN IPs, example.com):
// treat every candidate as unreachable so the request origin is returned
// unchanged, exactly the pre-#160 pair_url. Resolver tests pass their own
// probe explicitly and are unaffected.
func init() {
	pairProbeFor = func(string) pairBaseProbe { return func(context.Context, string) bool { return false } }
	pairTLSProbe = func(context.Context, string, string) bool { return false }
	tailnetNameLookup = func(context.Context) string { return "" }
}
