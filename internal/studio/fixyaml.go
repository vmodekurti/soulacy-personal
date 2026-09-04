package studio

import (
	"encoding/json"
	"strings"
)

// BuildYAMLFixInstruction builds the prompt that asks the framework LLM to
// repair a SOUL.yaml given the validation problems found in it. It is pure (no
// I/O) so it is unit-testable. The model is told to return ONLY the corrected
// YAML, with concrete guidance on the template-reference class of bug so the fix
// is correct rather than a blind string edit.
func BuildYAMLFixInstruction(yamlText string, issues []string, rules string) string {
	var sb strings.Builder
	sb.WriteString("You are repairing a Soulacy agent definition file (SOUL.yaml). ")
	sb.WriteString("Below is the current file and the validation problems found in it. ")
	sb.WriteString("Rewrite the file so EVERY listed problem is fixed, changing as little as possible and preserving all other fields and structure exactly.\n\n")

	sb.WriteString("How the file works, so you fix it correctly:\n")
	sb.WriteString("- A workflow node's `input` is a Go template. {{ .x }} inserts the value of flow variable x — the `output` of an EARLIER node. Every referenced variable must be produced by an earlier node's `output`.\n")
	sb.WriteString("- If an earlier node outputs an OBJECT, reference the EXACT scalar field you need, e.g. {{ .notebook.id }}. Never interpolate the whole object ({{ .notebook }}) and never a wrong/repeated nested path ({{ .notebook.notebook }}) — those render as Go \"map[...]\" text and break the receiving step.\n")
	sb.WriteString("- Choose the field that actually holds the needed value (commonly `id` for an identifier). To pass an entire object on purpose, use {{ toJson .x }}.\n")
	sb.WriteString("- Keep all valid YAML structure: ids, kinds, edges, schedule, channels, system_prompt, etc. Do not drop fields you are not fixing.\n\n")

	sb.WriteString(RulesPromptBlock(rules))
	sb.WriteString("\nReturn ONLY the corrected SOUL.yaml document. No commentary, no explanation, no markdown, no code fences.\n\n")

	sb.WriteString("PROBLEMS TO FIX:\n")
	for _, p := range issues {
		if p = strings.TrimSpace(p); p != "" {
			sb.WriteString("- ")
			sb.WriteString(p)
			sb.WriteString("\n")
		}
	}

	sb.WriteString("\nCURRENT SOUL.yaml:\n")
	sb.WriteString(yamlText)
	sb.WriteString("\n")
	return sb.String()
}

// CleanYAMLOutput strips code fences and stray surrounding whitespace a model
// might wrap around the corrected document, returning the bare YAML.
func CleanYAMLOutput(raw string) string {
	return strings.TrimSpace(stripFences(strings.TrimSpace(raw)))
}

// ReviewFinding is one rule violation / correctness issue the LLM review found —
// the semantic checks the deterministic validator can't make (wrong field, wrong
// id, broken logic). Rendered alongside the deterministic findings.
type ReviewFinding struct {
	Severity string `json:"severity"`
	NodeID   string `json:"nodeId"`
	Message  string `json:"message"`
	Fix      string `json:"fix"`
}

// BuildYAMLReviewInstruction asks the model to review a SOUL.yaml AGAINST the
// authoring rules and report violations a deterministic linter would miss
// (judgment calls: the right field, the right id, sound logic). It must return
// ONLY a JSON array so the result merges into the validation panel.
func BuildYAMLReviewInstruction(yamlText string, rules string) string {
	var sb strings.Builder
	sb.WriteString("You are reviewing a Soulacy agent definition (SOUL.yaml) against the authoring rules below. ")
	sb.WriteString("Report ONLY genuine rule violations or correctness problems that a deterministic linter would MISS — judgment calls like: a step uses the wrong field or the wrong id, a template references a value that exists but is semantically wrong, a poll/wait loop can't actually complete, a branch's logic is inverted, or a step's purpose doesn't match its inputs. ")
	sb.WriteString("Do NOT report pure syntax errors or restate obvious structural checks; focus on what needs human judgment.\n\n")
	sb.WriteString(RulesPromptBlock(rules))
	sb.WriteString("\nReturn ONLY a JSON array, no prose, no markdown, no code fences, in exactly this shape:\n")
	sb.WriteString("[{\"severity\":\"error|warning\",\"nodeId\":\"<the node id, or empty>\",\"message\":\"<plain-language problem>\",\"fix\":\"<concrete fix>\"}]\n")
	sb.WriteString("Return [] (an empty array) if you find nothing worth flagging.\n\n")
	sb.WriteString("SOUL.yaml to review:\n")
	sb.WriteString(yamlText)
	sb.WriteString("\n")
	return sb.String()
}

// ParseReviewFindings tolerantly extracts the JSON array of findings from raw
// model output (handles code fences and surrounding prose). Severity is
// normalized to "error" | "warning" (defaulting to warning). Returns nil on no
// parseable array — a review that returns junk simply yields no findings.
func ParseReviewFindings(raw string) []ReviewFinding {
	s := stripFences(strings.TrimSpace(raw))
	start := strings.IndexByte(s, '[')
	end := strings.LastIndexByte(s, ']')
	if start < 0 || end < 0 || end < start {
		return nil
	}
	var out []ReviewFinding
	if err := json.Unmarshal([]byte(s[start:end+1]), &out); err != nil {
		return nil
	}
	cleaned := out[:0]
	for _, f := range out {
		if strings.TrimSpace(f.Message) == "" {
			continue
		}
		if strings.ToLower(strings.TrimSpace(f.Severity)) == "error" {
			f.Severity = "error"
		} else {
			f.Severity = "warning"
		}
		cleaned = append(cleaned, f)
	}
	return cleaned
}
