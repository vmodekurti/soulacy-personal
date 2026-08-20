// metric_leak_test.go — a tenant's agent name must not reach the shared
// exposition.
//
// The guard in internal/metrics reads the DECLARATIONS: it fails if a label
// named something unbounded is declared. This reads the OUTPUT, through the
// real Enqueue/Send path, because a declaration says what a label is called
// and not what is fed into it. A future edit that added the agent ID as a
// `channel` label value would pass the declaration guard and be exactly the
// same leak.
//
// Why it lives in internal/channels rather than beside the guard: the metric
// has to be incremented by production code for the assertion to mean anything,
// and this is the package that increments it.
package channels

import (
	"context"
	"strings"
	"testing"

	"github.com/prometheus/common/expfmt"

	"github.com/soulacy/soulacy/internal/metrics"
	"github.com/soulacy/soulacy/pkg/message"
)

func TestAnAgentNameNeverReachesTheSharedMetricsExposition(t *testing.T) {
	// A plausible real agent ID: agent IDs are prose somebody typed, and this
	// is what makes them a disclosure rather than merely high-cardinality.
	const tenantAgent = "acme-invoice-reconciliation"

	reg := NewRegistry(1)
	if !reg.Enqueue(message.Message{Channel: "slack", AgentID: tenantAgent, Parts: message.Text("hi")}) {
		t.Fatal("enqueue should succeed, or nothing was recorded and this test proves nothing")
	}
	// Drain so the registry does not block a later test.
	<-reg.Inbox()

	// An unregistered send: the outbound counter's error paths were the other
	// place the agent ID was attached.
	_ = reg.Send(context.Background(), message.Message{Channel: "nowhere", AgentID: tenantAgent, Parts: message.Text("hi")})

	families, err := metrics.Registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	var rendered strings.Builder
	encoder := expfmt.NewEncoder(&rendered, expfmt.NewFormat(expfmt.TypeTextPlain))
	for _, family := range families {
		if encodeErr := encoder.Encode(family); encodeErr != nil {
			t.Fatal(encodeErr)
		}
	}
	// The scrape is ONE document for the whole process. Every workspace owner
	// or admin permitted to read /api/v1/metrics receives all of it, so
	// anything in here is readable by every other tenant's administrators.
	if strings.Contains(rendered.String(), tenantAgent) {
		for _, line := range strings.Split(rendered.String(), "\n") {
			if strings.Contains(line, tenantAgent) {
				t.Errorf("a tenant's agent name is in the shared metrics exposition: %s", line)
			}
		}
	}
	// And the counters must still have moved, or the assertion above is
	// satisfied by nothing being recorded at all.
	if !strings.Contains(rendered.String(), "soulacy_channel_inbound_total") {
		t.Error("the inbound counter is absent from the exposition, so the leak check had nothing to inspect")
	}
}
