package studio

import (
	"sort"
	"strings"
)

// builtinToolParams defines the Studio-side contracts for Soulacy built-ins.
// Runtime tools remain the source of execution truth; this table gives Studio
// enough shape to validate and repair workflows before a live run.
func builtinToolParams() map[string][]ToolParam {
	return map[string][]ToolParam{
		"fetch_url": {
			{Name: "url", Type: "string", Required: true},
			{Name: "max_bytes", Type: "integer"},
		},
		"http_request": {
			{Name: "url", Type: "string", Required: true},
			{Name: "method", Type: "string"},
			{Name: "headers", Type: "object"},
			{Name: "body", Type: "string"},
		},
		"web_search": {
			{Name: "query", Type: "string", Required: true},
			{Name: "num_results", Type: "integer"},
		},
		"kb_search": {
			{Name: "kb", Type: "string", Required: true},
			{Name: "query", Type: "string", Required: true},
			{Name: "top_k", Type: "integer"},
		},
		"kb_write": {
			{Name: "kb", Type: "string", Required: true},
			{Name: "content", Type: "string|object|array", Required: true},
			{Name: "title", Type: "string"},
			{Name: "source", Type: "string"},
			{Name: "mime_type", Type: "string"},
		},
		"queue_create": {
			{Name: "queue", Type: "string"},
		},
		"queue_names": {},
		"queue_put": {
			{Name: "queue", Type: "string"},
			{Name: "item", Type: "object|array|string|number|boolean", Required: true},
			{Name: "ttl_seconds", Type: "integer"},
		},
		"queue_take": {
			{Name: "queue", Type: "string"},
		},
		"queue_list": {
			{Name: "queue", Type: "string"},
			{Name: "limit", Type: "integer"},
		},
		"queue_clear": {
			{Name: "queue", Type: "string"},
		},
		"channel.send": {
			{Name: "channel", Type: "string"},
			{Name: "to", Type: "string"},
			{Name: "text", Type: "string", Required: true},
			{Name: "message", Type: "string"},
			{Name: "adapter", Type: "string"},
			{Name: "destination", Type: "string"},
			{Name: "target", Type: "string"},
			{Name: "recipient", Type: "string"},
			{Name: "chat_id", Type: "string"},
			{Name: "channel_id", Type: "string"},
		},
		"channel.status": {
			{Name: "channel", Type: "string"},
			{Name: "to", Type: "string"},
			{Name: "include_channels", Type: "boolean"},
			{Name: "adapter", Type: "string"},
			{Name: "destination", Type: "string"},
		},
	}
}

func builtinRequiredToolArgs() map[string][]string {
	out := map[string][]string{}
	for tool, params := range builtinToolParams() {
		for _, p := range params {
			if p.Required {
				out[tool] = append(out[tool], p.Name)
			}
		}
	}
	return out
}

func isBuiltinContractTool(name string) bool {
	_, ok := builtinToolParams()[strings.TrimSpace(name)]
	return ok
}

func sortedBuiltinToolNames() []string {
	names := make([]string, 0, len(builtinToolParams()))
	for name := range builtinToolParams() {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func toolParamPrompt(params []ToolParam) string {
	parts := make([]string, 0, len(params))
	for _, p := range params {
		s := p.Name
		if p.Required {
			s += "*"
		}
		if p.Type != "" {
			s += ":" + p.Type
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, ", ")
}
