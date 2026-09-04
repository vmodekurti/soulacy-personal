package studio

import (
	"encoding/json"
	"strings"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

func normalizePlatformToolChoices(draft *Draft, cat Catalog) int {
	if draft == nil {
		return 0
	}
	fixed := 0
	for i := range draft.Flow.Nodes {
		n := &draft.Flow.Nodes[i]
		if strings.TrimSpace(n.Kind) != sdkr.FlowNodeTool || strings.TrimSpace(n.Tool) != "write_file" {
			continue
		}
		if isKnowledgeIngestionContext(*draft, *n) {
			if rewriteWriteFileAsKBWrite(draft, n, i, cat) {
				fixed++
			}
			continue
		}
		if isTransientStateContext(*draft, *n) {
			if rewriteWriteFileAsQueuePut(draft, n, i) {
				fixed++
			}
		}
	}
	return fixed
}

func checkPlatformWorkflowPolicies(draft Draft, add func(sev, kind, node, msg, fix string)) {
	for _, n := range draft.Flow.Nodes {
		if strings.TrimSpace(n.Kind) != sdkr.FlowNodeTool || strings.TrimSpace(n.Tool) != "write_file" {
			continue
		}
		switch {
		case isKnowledgeIngestionContext(draft, n):
			add("block", "policy", n.ID,
				`This workflow uses "write_file" for a knowledge/document ingestion task.`,
				`Use kb_write instead: {"kb":"<KB name>","content":{{ toJson .artifact }},"title":"...","source":"..."}. write_file requires system authorization and stores host files, not searchable knowledge.`)
		case isTransientStateContext(draft, n):
			add("block", "policy", n.ID,
				`This workflow uses "write_file" for temporary queue/state handoff.`,
				`Use queue_put/queue_take/queue_list instead. Queue tools are in-memory, safe for interactive agents, and do not require system authorization.`)
		}
	}
}

func rewriteWriteFileAsKBWrite(draft *Draft, n *sdkr.FlowNode, idx int, cat Catalog) bool {
	raw := strings.TrimSpace(n.Input)
	obj, _ := decodeInputObject(raw)
	if obj == nil {
		obj = map[string]any{}
	}
	content := firstNonEmptyTemplateValue(obj, "content", "text", "body", "data", "value")
	if content == "" {
		content = bestUpstreamContentRef(*draft, idx)
	}
	if content == "" {
		content = "{{ .trigger.text }}"
	}
	kb := firstNonEmptyString(defaultKnowledgeBase(*draft, cat), stringish(obj["kb"]), "AI Docs")
	title := firstNonEmptyString(stringish(obj["title"]), stringish(obj["filename"]), "Stored artifact")
	source := firstNonEmptyString(stringish(obj["source"]), "{{ .trigger.text }}")
	n.Tool = "kb_write"
	n.Input = `{"kb":` + jsonString(kb) + `,"content":` + jsonSafeTemplate(content) + `,"title":` + jsonString(title) + `,"source":` + jsonString(source) + `}`
	if strings.TrimSpace(n.Description) == "" {
		n.Description = "Store the tagged artifact in the knowledge base"
	}
	return true
}

func rewriteWriteFileAsQueuePut(draft *Draft, n *sdkr.FlowNode, idx int) bool {
	raw := strings.TrimSpace(n.Input)
	obj, _ := decodeInputObject(raw)
	if obj == nil {
		obj = map[string]any{}
	}
	item := firstNonEmptyTemplateValue(obj, "item", "content", "text", "body", "data", "value")
	if item == "" {
		item = bestUpstreamContentRef(*draft, idx)
	}
	if item == "" {
		item = "{{ .trigger.text }}"
	}
	queue := firstNonEmptyString(stringish(obj["queue"]), inferQueueName(draftText(*draft)+" "+nodeText(*n)), "default")
	n.Tool = "queue_put"
	n.Input = `{"queue":` + jsonString(queue) + `,"item":` + jsonSafeTemplate(item) + `}`
	if strings.TrimSpace(n.Description) == "" {
		n.Description = "Put the item into a safe in-memory queue"
	}
	return true
}

func isKnowledgeIngestionContext(draft Draft, n sdkr.FlowNode) bool {
	text := draftText(draft) + " " + nodeText(n)
	return containsAny(text,
		"knowledge base", "knowledge store", "kb", "rag", "vector",
		"ingest", "ingestion", "document", "documents", "docx", "pdf",
		"artifact", "artifacts", "tag", "tags", "classify", "metadata",
	)
}

func isTransientStateContext(draft Draft, n sdkr.FlowNode) bool {
	text := draftText(draft) + " " + nodeText(n)
	return containsAny(text,
		"queue", "queued", "pending_resources", "pending resource",
		"temporary", "temp", "buffer", "handoff", "later", "next daily",
		"next run", "cross-step", "scratch",
	)
}

func draftText(d Draft) string {
	return strings.ToLower(strings.Join([]string{
		d.Name,
		d.Intent,
		d.RawIntent,
		d.SystemPrompt,
	}, " "))
}

func nodeText(n sdkr.FlowNode) string {
	return strings.ToLower(strings.Join([]string{
		n.ID,
		n.Description,
		n.Intent,
		n.Input,
		n.Output,
	}, " "))
}

func firstNonEmptyTemplateValue(obj map[string]any, keys ...string) string {
	for _, key := range keys {
		if v := strings.TrimSpace(stringish(obj[key])); v != "" {
			return v
		}
	}
	return ""
}

func firstNonEmptyString(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func jsonString(s string) string {
	b, err := json.Marshal(strings.TrimSpace(s))
	if err != nil {
		return `""`
	}
	return string(b)
}

func inferQueueName(text string) string {
	text = strings.ToLower(text)
	if strings.Contains(text, "pending_resources") || strings.Contains(text, "pending resource") {
		return "pending_resources"
	}
	if strings.Contains(text, "default") {
		return "default"
	}
	return ""
}
