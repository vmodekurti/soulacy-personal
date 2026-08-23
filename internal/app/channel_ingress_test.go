package app

import (
	"testing"

	"github.com/soulacy/soulacy/internal/config"
)

func TestResolveChannelIngressSubject(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  config.QueueConfig
		want string
	}{
		{name: "default stream", cfg: config.QueueConfig{Backend: "nats", NATSStream: "soulacy"}, want: "soulacy.channels.inbound"},
		{name: "custom stream", cfg: config.QueueConfig{Backend: "nats", NATSStream: "customer"}, want: "customer.channels.inbound"},
		{name: "custom prefix", cfg: config.QueueConfig{Backend: "nats", NATSSubjectPrefix: "platform.>"}, want: "platform.channels.inbound"},
		{name: "explicit", cfg: config.QueueConfig{Backend: "nats", NATSSubjectPrefix: "odd.*.layout", ChannelIngressSubject: "odd.channel.layout"}, want: "odd.channel.layout"},
		{name: "external", cfg: config.QueueConfig{Backend: "external"}, want: "soulacy.channels.inbound"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveChannelIngressSubject(tc.cfg)
			if err != nil || got != tc.want {
				t.Fatalf("subject = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
	if _, err := resolveChannelIngressSubject(config.QueueConfig{Backend: "nats", NATSSubjectPrefix: "odd.*.layout"}); err == nil {
		t.Fatal("ambiguous wildcard filter should require an explicit ingress subject")
	}
}
