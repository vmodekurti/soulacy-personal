package slack

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/pkg/message"
)

func TestStripLeadingSlackMention(t *testing.T) {
	got := stripLeadingSlackMention("<@U123ABC> weather in Chicago?")
	if got != "weather in Chicago?" {
		t.Fatalf("got %q", got)
	}

	got = stripLeadingSlackMention("<@U123ABC|weather>   <@U456> forecast")
	if got != "forecast" {
		t.Fatalf("got %q", got)
	}
}

func TestSlackEventIsGroupUsesChannelType(t *testing.T) {
	tests := []struct {
		name        string
		eventType   string
		channelType string
		want        bool
	}{
		{name: "direct message", eventType: "message", channelType: "im", want: false},
		{name: "public channel", eventType: "message", channelType: "channel", want: true},
		{name: "private channel", eventType: "message", channelType: "group", want: true},
		{name: "multi person dm", eventType: "message", channelType: "mpim", want: true},
		{name: "app mention activates directly", eventType: "app_mention", channelType: "channel", want: false},
		{name: "unknown is conservative", eventType: "message", channelType: "", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := slackEventIsGroup(tt.eventType, tt.channelType); got != tt.want {
				t.Fatalf("slackEventIsGroup(%q, %q) = %v, want %v", tt.eventType, tt.channelType, got, tt.want)
			}
		})
	}
}

func TestSlackSendSurfacesAPIError(t *testing.T) {
	oldAPI := slackAPI
	defer func() { slackAPI = oldAPI }()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat.postMessage" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":false,"error":"channel_not_found"}`))
	}))
	defer srv.Close()
	slackAPI = srv.URL

	adp := NewWithID("slack", "xoxb-test", "xapp-test", "weather")
	err := adp.Send(context.Background(), message.Message{
		ThreadID: "C123",
		Parts:    message.Text("hello"),
	})
	if err == nil || !strings.Contains(err.Error(), "channel_not_found") {
		t.Fatalf("err = %v, want channel_not_found", err)
	}
}
