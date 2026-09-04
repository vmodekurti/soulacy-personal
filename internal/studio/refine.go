// refine.go — Studio per-node re-describe (Story S6.3). The canvas lets a user
// point at one node and describe, in plain language, how it should change
// ("make this summarize in bullet points", "fetch from this URL instead").
// Refine builds a focused prompt around the WHOLE current workflow plus the
// target node id and the instruction, asks the model to return the full updated
// Draft, parses it, and validates the result via reasoning.CompileFlow.
//
// The core logic lives here (not in the gateway) so it is unit-testable with a
// fake LLM: Refine(ctx, llm, draft, nodeId, instruction) (Draft, error). It
// reuses the compiler's ParseDraft and normalizeFlow so refined drafts get the
// same tolerant parsing and kind-normalization as a fresh compile. An unknown
// nodeId, an unparseable response, or a result that fails CompileFlow are all
// clear errors — Refine NEVER returns a broken draft.
package studio

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	reasoning "github.com/soulacy/soulacy/internal/reasoning"
)

// Refine asks the model to apply a plain-language instruction to one node of an
// existing workflow and returns the updated Draft. It is deterministic in its
// guards: the nodeId must name a node present in the draft, the model's output
// must parse as a Draft, and the result must compile via reasoning.CompileFlow.
// Any of those failing yields an error and the ORIGINAL draft is left untouched
// for the caller.
func Refine(ctx context.Context, llm LLM, draft Draft, nodeID, instruction string) (Draft, error) {
	if llm == nil {
		return Draft{}, fmt.Errorf("studio: no LLM configured")
	}
	if strings.TrimSpace(nodeID) == "" {
		return Draft{}, fmt.Errorf("studio: nodeId is required")
	}
	if strings.TrimSpace(instruction) == "" {
		return Draft{}, fmt.Errorf("studio: instruction is required")
	}
	if !draftHasNode(draft, nodeID) {
		return Draft{}, fmt.Errorf("studio: node %q not found in workflow", nodeID)
	}

	prompt, err := buildRefinePrompt(draft, nodeID, instruction)
	if err != nil {
		return Draft{}, err
	}

	raw, err := llm.Complete(ctx, prompt)
	if err != nil {
		return Draft{}, fmt.Errorf("studio: llm complete: %w", err)
	}

	updated, err := ParseDraft(raw)
	if err != nil {
		return Draft{}, err
	}

	// Normalize node kinds exactly like a fresh compile so the refined draft
	// carries the same explicit kinds the engine will execute.
	normalizeFlow(&updated)

	// Hard contract: a refine that produces an invalid graph is rejected and
	// NOT returned. The caller keeps the original draft.
	if !updated.IsAgent() {
		if _, err := reasoning.CompileFlow(updated.spec()); err != nil {
			return Draft{}, fmt.Errorf("studio: refined workflow is invalid: %w", err)
		}
	}

	return updated, nil
}

// draftHasNode reports whether nodeID names a node in the draft's flow.
func draftHasNode(draft Draft, nodeID string) bool {
	for _, n := range draft.Flow.Nodes {
		if n.ID == nodeID {
			return true
		}
	}
	return false
}

// buildRefinePrompt builds the focused instruction: the current workflow JSON,
// the target node id, and the plain-language change, with a strict demand to
// return the FULL updated Draft (same schema) as JSON only.
func buildRefinePrompt(draft Draft, nodeID, instruction string) (string, error) {
	current, err := json.MarshalIndent(draft, "", "  ")
	if err != nil {
		return "", fmt.Errorf("studio: marshal current workflow: %w", err)
	}

	var sb strings.Builder
	sb.WriteString("You are the Soulacy Studio workflow editor. ")
	sb.WriteString("Apply a plain-language change to ONE node of an existing workflow.\n\n")
	sb.WriteString("Output RULES:\n")
	sb.WriteString("- Respond with ONLY a single JSON object: the FULL updated workflow. No prose, no markdown, no code fences.\n")
	sb.WriteString("- The JSON MUST match the exact same Draft schema as the input (same field names and nesting).\n")
	sb.WriteString("- Change ONLY what the instruction asks for on the target node. Leave every other node, edge, the trigger, channels, entry, and ports unchanged.\n")
	sb.WriteString("- Keep the target node's id the same so existing edges still connect.\n")
	sb.WriteString("- The result MUST remain a valid flow: every edge must reference real nodes (or \"end\"), and the entry node must exist.\n\n")

	sb.WriteString("Target node id: ")
	sb.WriteString(nodeID)
	sb.WriteString("\n\n")
	sb.WriteString("Instruction:\n")
	sb.WriteString(instruction)
	sb.WriteString("\n\n")
	sb.WriteString("Current workflow JSON:\n")
	sb.Write(current)
	sb.WriteString("\n")
	return sb.String(), nil
}
