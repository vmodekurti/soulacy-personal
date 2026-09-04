package metrics

import "testing"

func TestChannelStatsIncludesChannelCounters(t *testing.T) {
	ChannelInboundTotal.WithLabelValues("slack", "agent-a").Inc()
	ChannelOutboundTotal.WithLabelValues("slack", "agent-a", "success").Inc()
	ChannelInboxDropsTotal.WithLabelValues("slack").Inc()

	stats, err := ChannelStats()
	if err != nil {
		t.Fatalf("ChannelStats: %v", err)
	}
	if !hasChannelRow(stats.Inbound, "slack", "agent-a", "", 1) {
		t.Fatalf("inbound row missing from %#v", stats.Inbound)
	}
	if !hasChannelRow(stats.Outbound, "slack", "agent-a", "success", 1) {
		t.Fatalf("outbound row missing from %#v", stats.Outbound)
	}
	if !hasChannelRow(stats.InboxDrop, "slack", "", "", 1) {
		t.Fatalf("drop row missing from %#v", stats.InboxDrop)
	}
}

func hasChannelRow(rows []ChannelCounterRow, channel, agent, outcome string, min float64) bool {
	for _, row := range rows {
		if row.Channel == channel && row.Agent == agent && row.Outcome == outcome && row.Count >= min {
			return true
		}
	}
	return false
}
