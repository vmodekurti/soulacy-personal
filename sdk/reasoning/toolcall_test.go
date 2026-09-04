package reasoning

import (
	"encoding/json"
	"testing"
)

func TestToolCallUnmarshalAcceptsToolNameAndParametersAliases(t *testing.T) {
	var call ToolCall
	if err := json.Unmarshal([]byte(`{"tool_name":"fetch_url","parameters":{"url":"https://example.com","depth":2}}`), &call); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if call.Tool != "fetch_url" {
		t.Fatalf("Tool = %q, want fetch_url", call.Tool)
	}
	if call.Input["url"] != "https://example.com" || call.Input["depth"] != "2" {
		t.Fatalf("Input aliases not normalized: %#v", call.Input)
	}
	if call.Arguments["depth"] != float64(2) {
		t.Fatalf("Arguments lost numeric value: %#v", call.Arguments)
	}
}

func TestToolCallUnmarshalAcceptsNameParamsAndActionInputAliases(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"name+params", `{"name":"queue_list","params":{"queue":"pending_resources"}}`},
		{"tool+action_input", `{"tool":"queue_list","action_input":{"queue":"pending_resources"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var call ToolCall
			if err := json.Unmarshal([]byte(tc.raw), &call); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if call.Tool != "queue_list" || call.Input["queue"] != "pending_resources" {
				t.Fatalf("unexpected call: %#v", call)
			}
		})
	}
}

func TestPlannedStepUnmarshalAcceptsArgumentAliases(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"arguments", `{"id":"s1","description":"send","tool":"channel.send","arguments":{"channel":"telegram","to":123,"text":"hi"}}`},
		{"input", `{"id":"s1","description":"send","tool":"channel.send","input":{"channel":"telegram","to":123,"text":"hi"}}`},
		{"params", `{"id":"s1","description":"send","tool":"channel.send","params":{"channel":"telegram","to":123,"text":"hi"}}`},
		{"parameters", `{"id":"s1","description":"send","tool":"channel.send","parameters":{"channel":"telegram","to":123,"text":"hi"}}`},
		{"action_input", `{"id":"s1","description":"send","tool":"channel.send","action_input":{"channel":"telegram","to":123,"text":"hi"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var step PlannedStep
			if err := json.Unmarshal([]byte(tc.raw), &step); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if step.Tool != "channel.send" || step.Arguments["channel"] != "telegram" || step.Input["to"] != "123" {
				t.Fatalf("step arguments not normalized: %#v", step)
			}
		})
	}
}

func TestThinkResponseUnmarshalAcceptsTopLevelActionAliases(t *testing.T) {
	var resp ThinkResponse
	raw := `{
		"reasoning":"need pending resources",
		"done":false,
		"tool_name":"queue_list",
		"action_input":{"queue":"pending_resources","limit":2}
	}`
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if resp.Thought != "need pending resources" {
		t.Fatalf("Thought = %q", resp.Thought)
	}
	if resp.IsDone {
		t.Fatalf("IsDone = true, want false")
	}
	if resp.Action.Tool != "queue_list" || resp.Action.Input["queue"] != "pending_resources" || resp.Action.Input["limit"] != "2" {
		t.Fatalf("action aliases not normalized: %#v", resp.Action)
	}
	if resp.Action.Arguments["limit"] != float64(2) {
		t.Fatalf("numeric argument not preserved: %#v", resp.Action.Arguments)
	}
}

func TestThinkResponseUnmarshalAcceptsFinalAnswerAliases(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"output", `{"thought":"done","final":true,"output":"finished"}`},
		{"answer", `{"thought":"done","is_done":true,"answer":"finished"}`},
		{"response", `{"thought":"done","done":true,"response":"finished"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var resp ThinkResponse
			if err := json.Unmarshal([]byte(tc.raw), &resp); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if !resp.IsDone || resp.FinalAnswer != "finished" {
				t.Fatalf("final answer aliases not normalized: %#v", resp)
			}
		})
	}
}
