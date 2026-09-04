package runtime

import (
	"encoding/json"
	"testing"
)

func TestIsChannelSendReceipt(t *testing.T) {
	receipts := []string{
		`{"ok":true,"channel":"http","to":"<no value>"}`,
		`{"ok":true,"channel":"telegram","to":"123"}`,
		`{"ok":false,"channel":"slack"}`,
		`{"ok":true,"delivered":true,"channel":"telegram","to":"123","route_source":"default","text_preview":"queued"}`,
	}
	for _, r := range receipts {
		if !isChannelSendReceipt(json.RawMessage(r)) {
			t.Fatalf("expected receipt for %s", r)
		}
	}

	notReceipts := []string{
		`{"ticker":"NVDA","price":123.45,"chart_url":"https://x"}`,
		`{"ok":true,"channel":"http","to":"","summary":"NVDA is $123","chart":"http://x"}`, // has real content ⇒ leave it
		`"NVDA is trading at $123 — chart: http://x"`,                                      // plain string content
		`{}`,
		``,
	}
	for _, r := range notReceipts {
		if isChannelSendReceipt(json.RawMessage(r)) {
			t.Fatalf("did NOT expect receipt for %s", r)
		}
	}
}
