// templates.go — Studio starter templates (Story S6.1). A small set of
// built-in, ready-to-edit Studio Drafts the canvas can offer as one-click
// starting points ("New from template"). Each template's workflow is a real
// Draft that MUST pass reasoning.CompileFlow, so a user who picks one lands on
// a valid, immediately testable graph.
//
// Why built-in Drafts (not internal/templates): the internal/templates package
// serves agent SOUL.yaml Definitions, whose workflow block is optional and not
// shaped as a Studio Draft (different trigger vocabulary, no clarify/notes
// surface, and many starters have no flow graph at all). Converting them would
// be lossy and several would not compile as a flow. Defining the Studio
// starters here keeps the contract crisp and the compile guarantee testable.
package studio

import (
	reasoning "github.com/soulacy/soulacy/internal/reasoning"
	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

// Template is one Studio starter: display metadata plus a ready-to-edit Draft.
// Workflow always satisfies reasoning.CompileFlow (covered by tests).
type Template struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Workflow    Draft  `json:"workflow"`
}

// Templates returns the built-in Studio starter templates, in a stable order.
// It is a pure function with no I/O so it is trivially testable and cheap to
// call per request. Every returned Workflow compiles via reasoning.CompileFlow
// (asserted by TestTemplatesCompile).
func Templates() []Template {
	return []Template{
		scheduledDigestTemplate(),
		inboundQATemplate(),
		webhookRespondTemplate(),
	}
}

// scheduledDigestTemplate: a scheduled HN digest — fetch (tool) -> summarize
// (agent) -> deliver to telegram. The classic "every weekday morning, send me
// a digest" automation.
func scheduledDigestTemplate() Template {
	return Template{
		ID:          "scheduled-digest",
		Name:        "Scheduled HN Digest",
		Description: "Every weekday morning, fetch the top Hacker News stories, summarize them, and deliver the digest to Telegram.",
		Workflow: Draft{
			Name:     "Weekday HN Digest",
			Trigger:  Trigger{Type: "schedule", Config: map[string]any{"cron": "0 8 * * 1-5"}},
			Channels: []string{"telegram"},
			Flow: Flow{
				Nodes: []sdkr.FlowNode{
					{
						ID:   "fetch",
						Kind: sdkr.FlowNodeTool,
						// fetch_url is the engine's GET builtin (see
						// internal/runtime/engine_tools_http.go). It takes `url`.
						Tool:   "fetch_url",
						Input:  `{"url":"https://hacker-news.firebaseio.com/v0/topstories.json"}`,
						Output: "stories",
						X:      0, Y: 0,
					},
					{
						ID:     "summarize",
						Kind:   sdkr.FlowNodeAgent,
						Agent:  "summarizer",
						Input:  "Summarize the top 5 stories: {{.stories}}",
						Output: "summary",
						X:      220, Y: 0,
					},
				},
				Edges: []sdkr.FlowEdge{
					{From: "fetch", To: "summarize"},
					{From: "summarize", To: "end"},
				},
				Entry: "fetch",
			},
		},
	}
}

// inboundQATemplate: an inbound channel Q&A — channel trigger -> agent answers
// -> reply. The "reply to messages on Telegram" assistant pattern.
func inboundQATemplate() Template {
	return Template{
		ID:          "inbound-qa",
		Name:        "Inbound Channel Q&A",
		Description: "Answer questions that arrive on a chat channel: a message comes in, an agent reasons over it, and the reply is sent back.",
		Workflow: Draft{
			Name:     "Channel Q&A Assistant",
			Trigger:  Trigger{Type: "channel"},
			Channels: []string{"telegram"},
			Flow: Flow{
				Nodes: []sdkr.FlowNode{
					{
						ID:     "answer",
						Kind:   sdkr.FlowNodeAgent,
						Agent:  "assistant",
						Input:  "Answer the user's message: {{.trigger}}",
						Output: "reply",
						X:      0, Y: 0,
					},
				},
				Edges: []sdkr.FlowEdge{
					{From: "answer", To: "end"},
				},
				Entry: "answer",
			},
		},
	}
}

// webhookRespondTemplate: a webhook -> tool -> respond pipeline. An inbound
// HTTP callback triggers a tool action and the result is summarized for the
// response.
func webhookRespondTemplate() Template {
	return Template{
		ID:          "webhook-respond",
		Name:        "Webhook to Action",
		Description: "Receive a webhook, run a tool to act on the payload, then summarize the outcome for the response.",
		Workflow: Draft{
			Name:    "Webhook Action",
			Trigger: Trigger{Type: "webhook"},
			Flow: Flow{
				Nodes: []sdkr.FlowNode{
					{
						ID:   "act",
						Kind: sdkr.FlowNodeTool,
						// http_request is the engine's general HTTP builtin; it
						// requires `method` and `url` (engine_tools_http.go).
						// There is no `http_post` builtin.
						Tool:   "http_request",
						Input:  `{"method":"POST","url":"https://example.com/act","body":"{{.trigger}}"}`,
						Output: "actionResult",
						X:      0, Y: 0,
					},
					{
						ID:     "respond",
						Kind:   sdkr.FlowNodeAgent,
						Agent:  "responder",
						Input:  "Summarize the action result for the caller: {{.actionResult}}",
						Output: "response",
						X:      220, Y: 0,
					},
				},
				Edges: []sdkr.FlowEdge{
					{From: "act", To: "respond"},
					{From: "respond", To: "end"},
				},
				Entry: "act",
			},
		},
	}
}

// compiles reports whether a template's workflow passes reasoning.CompileFlow.
// Kept as a tiny helper so both the package and its tests can assert the
// invariant without duplicating the spec() projection.
func (t Template) compiles() error {
	_, err := reasoning.CompileFlow(t.Workflow.spec())
	return err
}
