package runtime

import (
	"reflect"
	"testing"

	"github.com/soulacy/soulacy/internal/llm"
)

func TestRecoverXMLToolCalls(t *testing.T) {
	tools := []llm.ToolSchema{{Name: "web_search"}, {Name: "lookup"}}
	tests := []struct {
		name    string
		content string
		wantOK  bool
		want    map[string]any
	}{
		{
			name:    "model emitted direct tool XML",
			content: "<web_search>\n<query>current weather Buffalo Grove, IL</query>\n</web_search>",
			wantOK:  true,
			want:    map[string]any{"query": "current weather Buffalo Grove, IL"},
		},
		{
			name:    "JSON arguments envelope",
			content: `<lookup><arguments>{"city":"Chicago","days":2}</arguments></lookup>`,
			wantOK:  true,
			want:    map[string]any{"city": "Chicago", "days": float64(2)},
		},
		{name: "prose around call is never executable", content: `Try <web_search><query>x</query></web_search> next.`, wantOK: false},
		{name: "unoffered tool is never executable", content: `<shell_exec><command>id</command></shell_exec>`, wantOK: false},
		{name: "attributes are rejected", content: `<web_search source="model"><query>x</query></web_search>`, wantOK: false},
		{name: "nested markup is rejected", content: `<web_search><query><city>x</city></query></web_search>`, wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls, ok := recoverXMLToolCalls(tt.content, tools)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v; calls=%+v", ok, tt.wantOK, calls)
			}
			if !tt.wantOK {
				return
			}
			if len(calls) != 1 || calls[0].Name == "" || !reflect.DeepEqual(calls[0].Arguments, tt.want) {
				t.Fatalf("calls = %+v, want args %#v", calls, tt.want)
			}
		})
	}
}
