// metric_leak_test.go — a tenant's tool name must not reach the shared
// exposition either.
//
// The `agent` label was the obvious one and is gone. `tool` is the same
// disclosure through a label whose NAME looks innocuous, which is why the
// guard in internal/metrics cannot be a blocklist of suspicious label names.
// tc.Name is whatever the model called: an MCP server ID a workspace added
// since MU-017, a plugin ID, a peer agent ID, or a Python tool somebody named
// in their own SOUL.yaml.
//
// Asserted through toolMetricLabel — the function the two dispatch call sites
// pass to WithLabelValues — rather than through metrics.ToolLabel, because the
// classification being correct is not the same as the dispatcher using it, and
// the dispatcher is what decides whether the raw name is emitted.
package runtime

import (
	"strings"
	"testing"
)

func TestATenantsToolNameNeverBecomesAMetricLabel(t *testing.T) {
	e := newMinimalEngine(t)

	// Every category of name that is not compiled into this binary. Each
	// embeds a string a customer would recognise as theirs.
	for _, name := range []string{
		"mcp__acme-internal-crm__lookup",
		"plugin__acme-billing__charge",
		"agent__acme-invoice-reconciliation",
		"scrape_acme_partner_portal",
	} {
		label := e.toolMetricLabel(name)
		if strings.Contains(label, "acme") {
			t.Errorf("tool %q produced the metric label %q, putting a customer's own vocabulary in "+
				"the shared /metrics document", name, label)
		}
	}

	// A builtin must still come through verbatim, or the label is useless and
	// the next person restores the raw name to get their dashboards back.
	builtins := e.Builtins()
	if len(builtins) == 0 {
		t.Fatal("no builtins registered, so the verbatim half of this test proves nothing")
	}
	if label := e.toolMetricLabel(builtins[0].Name); label != builtins[0].Name {
		t.Errorf("builtin %q became the label %q; operators lose per-tool visibility entirely", builtins[0].Name, label)
	}
}
