package runtime

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"strings"

	"github.com/soulacy/soulacy/internal/llm"
	"github.com/soulacy/soulacy/pkg/message"
)

// xmlToolNode is the deliberately small XML subset accepted by
// recoverXMLToolCalls. Some open-weight models ignore the provider's native
// function-call envelope and emit a tool invocation as:
//
//	<web_search><query>weather</query></web_search>
//
// Recovery belongs at the runtime boundary so it works consistently across
// providers. It is intentionally strict: the whole response must consist only
// of calls to tools offered in this request. Ordinary prose containing an XML
// example can therefore never turn into an executable action.
type xmlToolNode struct {
	XMLName  xml.Name
	Attrs    []xml.Attr    `xml:",any,attr"`
	Text     string        `xml:",chardata"`
	Children []xmlToolNode `xml:",any"`
}

func recoverXMLToolCalls(content string, tools []llm.ToolSchema) ([]message.ToolCall, bool) {
	content = strings.TrimSpace(content)
	if content == "" || !strings.HasPrefix(content, "<") || len(tools) == 0 {
		return nil, false
	}
	allowed := make(map[string]struct{}, len(tools))
	for _, tool := range tools {
		if name := strings.TrimSpace(tool.Name); name != "" {
			allowed[name] = struct{}{}
		}
	}

	var envelope struct {
		Text     string        `xml:",chardata"`
		Children []xmlToolNode `xml:",any"`
	}
	if err := xml.Unmarshal([]byte("<soulacy_calls>"+content+"</soulacy_calls>"), &envelope); err != nil || strings.TrimSpace(envelope.Text) != "" || len(envelope.Children) == 0 {
		return nil, false
	}

	calls := make([]message.ToolCall, 0, len(envelope.Children))
	for i, node := range envelope.Children {
		name := node.XMLName.Local
		if name == "tool_call" {
			call, ok := recoverGLMToolNode(node, tools)
			if !ok {
				return nil, false
			}
			calls = append(calls, call)
			continue
		}
		if _, ok := allowed[name]; !ok || len(node.Attrs) != 0 || strings.TrimSpace(node.Text) != "" || len(node.Children) == 0 {
			return nil, false
		}
		args := make(map[string]any, len(node.Children))
		for _, child := range node.Children {
			if child.XMLName.Local == "" || len(child.Attrs) != 0 || len(child.Children) != 0 {
				return nil, false
			}
			key, value := child.XMLName.Local, strings.TrimSpace(child.Text)
			if (key == "arguments" || key == "parameters") && strings.HasPrefix(value, "{") {
				var decoded map[string]any
				if json.Unmarshal([]byte(value), &decoded) != nil {
					return nil, false
				}
				for k, v := range decoded {
					args[k] = v
				}
				continue
			}
			if _, duplicate := args[key]; duplicate {
				return nil, false
			}
			args[key] = value
		}
		calls = append(calls, message.ToolCall{
			ID:        fmt.Sprintf("xml-tool-%s-%d", uuidShort(), i+1),
			Name:      name,
			Arguments: args,
		})
	}
	return calls, true
}

// GLM may emit its native arg_key/arg_value format as assistant content.
// Recover only a complete, standalone invocation of an offered tool. Prose
// surrounding a call is never executed; the loop asks for a proper call.
func recoverGLMToolNode(node xmlToolNode, tools []llm.ToolSchema) (message.ToolCall, bool) {
	name := strings.TrimSpace(node.Text)
	var schema *llm.ToolSchema
	for i := range tools {
		if tools[i].Name == name {
			schema = &tools[i]
			break
		}
	}
	if schema == nil || len(node.Attrs) != 0 || len(node.Children)%2 != 0 {
		return message.ToolCall{}, false
	}
	args := map[string]any{}
	properties, _ := schema.Parameters["properties"].(map[string]any)
	for i := 0; i < len(node.Children); i += 2 {
		key, value := node.Children[i], node.Children[i+1]
		if key.XMLName.Local != "arg_key" || value.XMLName.Local != "arg_value" || len(key.Attrs)+len(value.Attrs)+len(key.Children)+len(value.Children) != 0 {
			return message.ToolCall{}, false
		}
		k, v := strings.TrimSpace(key.Text), strings.TrimSpace(value.Text)
		if k == "" {
			return message.ToolCall{}, false
		}
		if _, exists := args[k]; exists {
			return message.ToolCall{}, false
		}
		var argument any = v
		property, _ := properties[k].(map[string]any)
		if kind, _ := property["type"].(string); kind != "" && kind != "string" {
			if json.Unmarshal([]byte(v), &argument) != nil {
				return message.ToolCall{}, false
			}
		}
		args[k] = argument
	}
	return message.ToolCall{ID: "xml-tool-" + uuidShort(), Name: name, Arguments: args}, true
}

func isUnrecoveredToolInvocation(content string) bool {
	text := strings.TrimSpace(content)
	if !strings.HasSuffix(text, "</tool_call>") || strings.Contains(text, "```") {
		return false
	}
	return strings.HasPrefix(text, "<tool_call>") ||
		(strings.HasPrefix(strings.ToLower(text), "continuing with ") && strings.Contains(text, "<tool_call>"))
}
