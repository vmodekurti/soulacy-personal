package app

import (
	"fmt"
	"strings"

	"github.com/soulacy/soulacy/internal/config"
)

// resolveChannelIngressSubject chooses a concrete subject that is inside the
// operator's JetStream filter. Custom wildcard layouts require an explicit
// subject because guessing outside the stream makes Publish wait for an ack
// that can never arrive.
func resolveChannelIngressSubject(cfg config.QueueConfig) (string, error) {
	if explicit := strings.TrimSpace(cfg.ChannelIngressSubject); explicit != "" {
		return explicit, nil
	}
	if !strings.EqualFold(strings.TrimSpace(cfg.Backend), "nats") {
		return "soulacy.channels.inbound", nil
	}
	prefix := strings.TrimSpace(cfg.NATSSubjectPrefix)
	if prefix == "" {
		stream := strings.TrimSpace(cfg.NATSStream)
		if stream == "" {
			stream = "soulacy"
		}
		return stream + ".channels.inbound", nil
	}
	if prefix == ">" {
		return "soulacy.channels.inbound", nil
	}
	if strings.HasSuffix(prefix, ".>") {
		return strings.TrimSuffix(prefix, ".>") + ".channels.inbound", nil
	}
	if strings.HasSuffix(prefix, ".*") && strings.Count(prefix, "*") == 1 {
		return strings.TrimSuffix(prefix, ".*") + ".inbound", nil
	}
	if !strings.ContainsAny(prefix, "*>") {
		return prefix, nil
	}
	return "", fmt.Errorf("queue.channel_ingress_subject is required for custom NATS subject filter %q", prefix)
}
