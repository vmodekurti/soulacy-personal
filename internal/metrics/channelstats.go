package metrics

import dto "github.com/prometheus/client_model/go"

type ChannelCounterRow struct {
	Channel string  `json:"channel"`
	Agent   string  `json:"agent,omitempty"`
	Outcome string  `json:"outcome,omitempty"`
	Count   float64 `json:"count"`
}

type ChannelStatsSnapshot struct {
	Inbound   []ChannelCounterRow `json:"inbound"`
	Outbound  []ChannelCounterRow `json:"outbound"`
	InboxDrop []ChannelCounterRow `json:"inbox_drops"`
}

// ChannelStats returns a compact JSON-friendly view of channel counters. The
// raw Prometheus endpoint stays available for full scraping; this powers the
// GUI's delivery health panels without making the browser parse text metrics.
func ChannelStats() (ChannelStatsSnapshot, error) {
	families, err := Registry.Gather()
	if err != nil {
		return ChannelStatsSnapshot{}, err
	}
	var out ChannelStatsSnapshot
	for _, fam := range families {
		switch fam.GetName() {
		case "soulacy_channel_inbound_total":
			out.Inbound = append(out.Inbound, counterRows(fam.GetMetric(), "channel", "agent", "")...)
		case "soulacy_channel_outbound_total":
			out.Outbound = append(out.Outbound, counterRows(fam.GetMetric(), "channel", "agent", "outcome")...)
		case "soulacy_channel_inbox_drops_total":
			out.InboxDrop = append(out.InboxDrop, counterRows(fam.GetMetric(), "channel", "", "")...)
		}
	}
	return out, nil
}

func counterRows(metrics []*dto.Metric, channelKey, agentKey, outcomeKey string) []ChannelCounterRow {
	rows := make([]ChannelCounterRow, 0, len(metrics))
	for _, m := range metrics {
		if m == nil || m.GetCounter() == nil {
			continue
		}
		labels := metricLabels(m)
		rows = append(rows, ChannelCounterRow{
			Channel: labels[channelKey],
			Agent:   labels[agentKey],
			Outcome: labels[outcomeKey],
			Count:   m.GetCounter().GetValue(),
		})
	}
	return rows
}

func metricLabels(m *dto.Metric) map[string]string {
	labels := map[string]string{}
	for _, lp := range m.GetLabel() {
		labels[lp.GetName()] = lp.GetValue()
	}
	return labels
}
