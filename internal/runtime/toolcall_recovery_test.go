package runtime

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/internal/llm"
	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
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

func TestRecoverGLMToolCallsIsStrictAndPreservesArgumentTypes(t *testing.T) {
	tools := []llm.ToolSchema{{Name: "advance", Parameters: map[string]any{"properties": map[string]any{
		"cursor": map[string]any{"type": "string"}, "count": map[string]any{"type": "integer"},
	}}}, {Name: "verify_batch"}}
	markup := `<tool_call>advance<arg_key>cursor</arg_key><arg_value>00123</arg_value><arg_key>count</arg_key><arg_value>2</arg_value></tool_call>`
	calls, ok := recoverXMLToolCalls(markup, tools)
	if !ok || len(calls) != 1 || calls[0].Arguments["cursor"] != "00123" || calls[0].Arguments["count"] != float64(2) {
		t.Fatalf("recovered=%+v ok=%v", calls, ok)
	}
	again, _ := recoverXMLToolCalls(markup, tools)
	if calls[0].ID == again[0].ID {
		t.Fatal("new turn reused an earlier tool call id")
	}
	if calls, ok := recoverXMLToolCalls(`<tool_call>verify_batch</tool_call>`, tools); !ok || len(calls[0].Arguments) != 0 {
		t.Fatalf("zero-argument verifier: %+v %v", calls, ok)
	}
	for _, text := range []string{
		"Continuing with the next cursor." + markup,
		"Example: " + markup,
		`<tool_call>shell_exec<arg_key>command</arg_key><arg_value>id</arg_value></tool_call>`,
		`<tool_call>advance<arg_key>cursor</arg_key></tool_call>`,
		`<tool_call>advance<arg_key>cursor</arg_key><arg_value>a</arg_value><arg_key>cursor</arg_key><arg_value>b</arg_value></tool_call>`,
	} {
		if calls, ok := recoverXMLToolCalls(text, tools); ok {
			t.Fatalf("unsafe/ambiguous text executed: %s => %+v", text, calls)
		}
	}
}

func TestUnrecoveredGLMPreambleContinuesInsteadOfCompleting(t *testing.T) {
	def := &agent.Definition{ID: "glm", Enabled: true, LLM: agent.LLMConfig{Provider: "test", Model: "glm"}, Builtins: strListPtr("advance")}
	e, provider := newHandleTestEngine(t, def)
	effects := 0
	e.builtins = []BuiltinTool{{Name: "advance", Handler: func(context.Context, map[string]any) (string, error) {
		effects++
		return `{"complete":true}`, nil
	}}}
	markup := `<tool_call>advance<arg_key>cursor</arg_key><arg_value>ef2d127de37b942b</arg_value></tool_call>`
	provider.responses = []llm.CompletionResponse{{Content: "Continuing with the next cursor." + markup}, {Content: markup}, {Content: "Completed the batch."}}
	reply, err := e.Handle(context.Background(), testUserMessage(def.ID, "session", "Process the next page"))
	if err != nil || effects != 1 || flattenParts(reply.Parts) != "Completed the batch." {
		t.Fatalf("reply=%+v effects=%d err=%v", reply, effects, err)
	}
}

func TestPersistentInvalidToolTextReportsIncomplete(t *testing.T) {
	def := &agent.Definition{ID: "glm", Enabled: true, LLM: agent.LLMConfig{Provider: "test", Model: "glm"}, Builtins: strListPtr("advance")}
	e, provider := newHandleTestEngine(t, def)
	e.builtins = []BuiltinTool{{Name: "advance", Handler: func(context.Context, map[string]any) (string, error) {
		t.Fatal("ambiguous text must not execute")
		return "", nil
	}}}
	for i := 0; i < 4; i++ {
		provider.responses = append(provider.responses, llm.CompletionResponse{Content: "Continuing with the next cursor.<tool_call>advance</tool_call>"})
	}
	reply, err := e.Handle(context.Background(), testUserMessage(def.ID, "session", "Process the batch"))
	if err != nil || reply.Metadata[message.MetaOutcome] != "incomplete" || strings.Contains(flattenParts(reply.Parts), "<tool_call>") {
		t.Fatalf("reply=%+v err=%v", reply, err)
	}
}
