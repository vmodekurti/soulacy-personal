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
			ID:        fmt.Sprintf("xml-tool-%d", i+1),
			Name:      name,
			Arguments: args,
		})
	}
	return calls, true
}
