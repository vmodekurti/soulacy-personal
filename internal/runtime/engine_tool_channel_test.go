package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/internal/channels"
	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
)

func TestChannelSendBuiltinRoutesThroughRegistry(t *testing.T) {
	e := newMinimalEngine(t)
	reg := channels.NewRegistry(1)
	adp := &channelSendCaptureAdapter{id: "test-channel"}
	reg.Register(adp)
	e.SetChannelRegistry(reg)

	tool := builtinByName(t, e.buildBuiltins(), "channel.send")
	out, err := tool.Handler(context.Background(), map[string]any{
		"channel": "test-channel",
		"to":      "dest-123",
		"text":    "hello delivery",
		"metadata": map[string]any{
			"source": "unit-test",
		},
	})
	if err != nil {
		t.Fatalf("channel.send returned error: %v", err)
	}
	if !strings.Contains(out, `"ok":true`) {
		t.Fatalf("channel.send result should confirm success, got %s", out)
	}
	if !strings.Contains(out, `"delivered":true`) || !strings.Contains(out, `"route_source":"explicit"`) {
		t.Fatalf("channel.send result should expose delivery route details, got %s", out)
	}
	if adp.sent.ThreadID != "dest-123" || firstText(adp.sent) != "hello delivery" {
		t.Fatalf("sent message mismatch: %+v", adp.sent)
	}
	if adp.sent.Channel != "test-channel" {
		t.Fatalf("sent channel = %q, want test-channel", adp.sent.Channel)
	}
	if adp.sent.Metadata["source"] != "unit-test" || adp.sent.Metadata["tool"] != "channel.send" {
		t.Fatalf("metadata mismatch: %+v", adp.sent.Metadata)
	}
}

func TestChannelSendBuiltinUsesInboundContextAndMessageAlias(t *testing.T) {
	e := newMinimalEngine(t)
	reg := channels.NewRegistry(1)
	adp := &channelSendCaptureAdapter{id: "telegram-research-librarian"}
	reg.Register(adp)
	e.SetChannelRegistry(reg)

	ctx := context.WithValue(context.Background(), inboundMsgKey{}, message.Message{
		Channel:  "telegram-research-librarian",
		ThreadID: "8546291328",
	})
	tool := builtinByName(t, e.buildBuiltins(), "channel.send")
	out, err := tool.Handler(ctx, map[string]any{
		"message": "queued",
	})
	if err != nil {
		t.Fatalf("channel.send returned error: %v", err)
	}
	if !strings.Contains(out, `"ok":true`) {
		t.Fatalf("channel.send result should confirm success, got %s", out)
	}
	if !strings.Contains(out, `"route_source":"inbound"`) || !strings.Contains(out, `"text_preview":"queued"`) {
		t.Fatalf("channel.send result should identify inbound route and preview, got %s", out)
	}
	if adp.sent.Channel != "telegram-research-librarian" {
		t.Fatalf("sent channel = %q, want inbound adapter", adp.sent.Channel)
	}
	if adp.sent.ThreadID != "8546291328" || firstText(adp.sent) != "queued" {
		t.Fatalf("sent message mismatch: %+v", adp.sent)
	}
}

func TestChannelSendBuiltinAcceptsCommonAliases(t *testing.T) {
	e := newMinimalEngine(t)
	reg := channels.NewRegistry(1)
	adp := &channelSendCaptureAdapter{id: "slack-research"}
	reg.Register(adp)
	e.SetChannelRegistry(reg)

	tool := builtinByName(t, e.buildBuiltins(), "channel.send")
	out, err := tool.Handler(context.Background(), map[string]any{
		"adapter":    "slack-research",
		"channel_id": "C123",
		"body":       "queued",
	})
	if err != nil {
		t.Fatalf("channel.send returned error: %v", err)
	}
	if !strings.Contains(out, `"ok":true`) {
		t.Fatalf("channel.send result should confirm success, got %s", out)
	}
	if adp.sent.Channel != "slack-research" || adp.sent.ThreadID != "C123" || firstText(adp.sent) != "queued" {
		t.Fatalf("sent message mismatch: %+v", adp.sent)
	}
}

func TestChannelSendBuiltinAcceptsNaturalAliases(t *testing.T) {
	e := newMinimalEngine(t)
	reg := channels.NewRegistry(1)
	adp := &channelSendCaptureAdapter{id: "discord"}
	reg.Register(adp)
	e.SetChannelRegistry(reg)

	tool := builtinByName(t, e.buildBuiltins(), "channel.send")
	out, err := tool.Handler(context.Background(), map[string]any{
		"platform":  "discord",
		"recipient": "room-42",
		"msg":       "natural alias delivery",
	})
	if err != nil {
		t.Fatalf("channel.send returned error: %v", err)
	}
	if !strings.Contains(out, `"ok":true`) {
		t.Fatalf("channel.send result should confirm success, got %s", out)
	}
	if adp.sent.Channel != "discord" || adp.sent.ThreadID != "room-42" || firstText(adp.sent) != "natural alias delivery" {
		t.Fatalf("sent message mismatch: %+v", adp.sent)
	}
}

func TestChannelSendBuiltinUsesConfiguredDefaultDestination(t *testing.T) {
	e := newMinimalEngine(t)
	reg := channels.NewRegistry(1)
	adp := &channelSendCaptureAdapter{id: "telegram-research-librarian"}
	reg.Register(adp)
	e.SetChannelRegistry(reg)
	e.SetChannelDefaultOutputs(map[string]agent.ScheduleOutput{
		"telegram": {
			Channel: "telegram-research-librarian",
			To:      "8546291328",
		},
	})

	tool := builtinByName(t, e.buildBuiltins(), "channel.send")
	out, err := tool.Handler(context.Background(), map[string]any{
		"channel": "telegram",
		"text":    "queued",
	})
	if err != nil {
		t.Fatalf("channel.send returned error: %v", err)
	}
	if !strings.Contains(out, `"channel":"telegram-research-librarian"`) {
		t.Fatalf("channel.send result should resolve concrete adapter, got %s", out)
	}
	if !strings.Contains(out, `"route_source":"default"`) {
		t.Fatalf("channel.send result should identify default routing, got %s", out)
	}
	if adp.sent.Channel != "telegram-research-librarian" || adp.sent.ThreadID != "8546291328" {
		t.Fatalf("sent route mismatch: %+v", adp.sent)
	}
}

func TestChannelSendBuiltinUsesOnlyDefaultChannelWhenChannelOmitted(t *testing.T) {
	e := newMinimalEngine(t)
	reg := channels.NewRegistry(1)
	adp := &channelSendCaptureAdapter{id: "slack"}
	reg.Register(adp)
	e.SetChannelRegistry(reg)
	e.SetChannelDefaultOutputs(map[string]agent.ScheduleOutput{
		"slack": {Channel: "slack", To: "C123"},
	})

	tool := builtinByName(t, e.buildBuiltins(), "channel.send")
	_, err := tool.Handler(context.Background(), map[string]any{
		"text": "hello default",
	})
	if err != nil {
		t.Fatalf("channel.send returned error: %v", err)
	}
	if adp.sent.Channel != "slack" || adp.sent.ThreadID != "C123" || firstText(adp.sent) != "hello default" {
		t.Fatalf("sent message mismatch: %+v", adp.sent)
	}
}

func TestChannelSendBuiltinRequiresRegistryAndFields(t *testing.T) {
	e := newMinimalEngine(t)
	tool := builtinByName(t, e.buildBuiltins(), "channel.send")

	tests := []struct {
		name string
		args map[string]any
		want string
	}{
		{name: "channel", args: map[string]any{"to": "x", "text": "x"}, want: "channel is required"},
		{name: "to", args: map[string]any{"channel": "x", "text": "x"}, want: "to is required"},
		{name: "text", args: map[string]any{"channel": "x", "to": "x"}, want: "text is required"},
		{name: "registry", args: map[string]any{"channel": "x", "to": "x", "text": "x"}, want: "channel registry is unavailable"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tool.Handler(context.Background(), tc.args)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want contains %q", err, tc.want)
			}
		})
	}
}

func TestChannelSendBuiltinRejectsUnknownChannel(t *testing.T) {
	e := newMinimalEngine(t)
	e.SetChannelRegistry(channels.NewRegistry(1))
	tool := builtinByName(t, e.buildBuiltins(), "channel.send")

	_, err := tool.Handler(context.Background(), map[string]any{
		"channel": "missing",
		"to":      "dest",
		"text":    "hello",
	})
	if err == nil || !strings.Contains(err.Error(), `channel "missing" is not registered`) {
		t.Fatalf("err = %v, want unknown channel error", err)
	}
}

func TestChannelSendBuiltinErrorIncludesDeliveryDiagnosis(t *testing.T) {
	e := newMinimalEngine(t)
	reg := channels.NewRegistry(1)
	adp := &channelSendCaptureAdapter{
		id:      "telegram",
		sendErr: errors.New("telegram: send: API returned status 400: Bad Request: chat not found"),
	}
	reg.Register(adp)
	e.SetChannelRegistry(reg)

	tool := builtinByName(t, e.buildBuiltins(), "channel.send")
	_, err := tool.Handler(context.Background(), map[string]any{
		"channel": "telegram",
		"to":      "8546291328",
		"text":    "hello",
	})
	if err == nil {
		t.Fatal("expected channel.send failure")
	}
	for _, want := range []string{
		`diagnosis category=invalid_destination`,
		`Telegram could not find this chat`,
		`-100`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error missing %q: %v", want, err)
		}
	}
}

func TestChannelStatusBuiltinDiagnosesConfiguredDefault(t *testing.T) {
	e := newMinimalEngine(t)
	reg := channels.NewRegistry(1)
	reg.Register(&channelSendCaptureAdapter{id: "telegram-research-librarian"})
	e.SetChannelRegistry(reg)
	e.SetChannelDefaultOutputs(map[string]agent.ScheduleOutput{
		"telegram": {
			Channel: "telegram-research-librarian",
			To:      "8546291328",
			BotName: "Notebook Bot",
		},
	})

	tool := builtinByName(t, e.buildBuiltins(), "channel.status")
	out, err := tool.Handler(context.Background(), map[string]any{
		"channel":          "telegram",
		"include_channels": true,
	})
	if err != nil {
		t.Fatalf("channel.status returned error: %v", err)
	}
	for _, want := range []string{
		`"ok":true`,
		`"channel":"telegram-research-librarian"`,
		`"to":"8546291328"`,
		`"route_source":"default"`,
		`"registered_channels":["telegram-research-librarian"]`,
		`"bot_name":"Notebook Bot"`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("channel.status result missing %s: %s", want, out)
		}
	}
}

func TestChannelStatusBuiltinUsesInboundContext(t *testing.T) {
	e := newMinimalEngine(t)
	reg := channels.NewRegistry(1)
	reg.Register(&channelSendCaptureAdapter{id: "slack-research"})
	e.SetChannelRegistry(reg)

	ctx := context.WithValue(context.Background(), inboundMsgKey{}, message.Message{
		Channel:  "slack-research",
		ThreadID: "C123",
	})
	tool := builtinByName(t, e.buildBuiltins(), "channel.status")
	out, err := tool.Handler(ctx, map[string]any{})
	if err != nil {
		t.Fatalf("channel.status returned error: %v", err)
	}
	if !strings.Contains(out, `"ok":true`) || !strings.Contains(out, `"route_source":"inbound"`) || !strings.Contains(out, `"to":"C123"`) {
		t.Fatalf("channel.status should diagnose inbound route, got %s", out)
	}
}

func TestChannelStatusBuiltinReportsMissingDestination(t *testing.T) {
	e := newMinimalEngine(t)
	reg := channels.NewRegistry(1)
	reg.Register(&channelSendCaptureAdapter{id: "discord"})
	e.SetChannelRegistry(reg)

	tool := builtinByName(t, e.buildBuiltins(), "channel.status")
	out, err := tool.Handler(context.Background(), map[string]any{
		"channel": "discord",
	})
	if err != nil {
		t.Fatalf("channel.status returned error: %v", err)
	}
	if !strings.Contains(out, `"ok":false`) || !strings.Contains(out, `"category":"missing_destination"`) {
		t.Fatalf("channel.status should report missing destination, got %s", out)
	}
}

func TestChannelStatusAliasIsNormalized(t *testing.T) {
	call := normalizeToolCall(message.ToolCall{
		Name: "channel.diagnose",
		Arguments: map[string]any{
			"adapter":     "telegram",
			"destination": "123",
		},
	})
	if call.Name != "channel.status" || call.Arguments["channel"] != "telegram" || call.Arguments["to"] != "123" {
		t.Fatalf("normalized call = %#v", call)
	}
}

func builtinByName(t *testing.T, tools []BuiltinTool, name string) BuiltinTool {
	t.Helper()
	for _, tool := range tools {
		if tool.Name == name {
			return tool
		}
	}
	t.Fatalf("builtin %q not found", name)
	return BuiltinTool{}
}

type channelSendCaptureAdapter struct {
	id      string
	sendErr error
	sent    message.Message
}

func (a *channelSendCaptureAdapter) ID() string { return a.id }

func (a *channelSendCaptureAdapter) Name() string { return "Channel Send Capture" }

func (a *channelSendCaptureAdapter) Start(context.Context, chan<- message.Message) error { return nil }

func (a *channelSendCaptureAdapter) Send(_ context.Context, msg message.Message) error {
	a.sent = msg
	return a.sendErr
}

func (a *channelSendCaptureAdapter) Stop() error { return nil }

func (a *channelSendCaptureAdapter) Status() channels.AdapterStatus {
	return channels.AdapterStatus{Connected: true}
}

func firstText(msg message.Message) string {
	if len(msg.Parts) == 0 {
		return ""
	}
	return msg.Parts[0].Text
}
