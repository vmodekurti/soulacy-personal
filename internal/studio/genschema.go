package studio

import "context"

// genschema.go implements schema-constrained generation: instead of relying only
// on the prompt (and repairing whatever the model emits), we hand the builder
// model a JSON Schema that pins down the DRAFT shape at the source. The single
// highest-value constraint is node.kind — the model can no longer invent a
// "start"/"end"/"sqlquery" node, which was the biggest class of Studio flakiness.
// The schema is a strong GUIDE, not a strict contract: it intentionally allows
// freeform sub-objects (trigger.config, per-node params) so it works across
// providers (Anthropic forced-tool input_schema, Gemini responseSchema, OpenAI
// non-strict json_schema) without a strict-mode rejection. Whatever the model
// still gets wrong is caught by the deterministic normalizer + validation.

// ValidNodeKinds is the closed set of flow node kinds the engine understands.
// Kept here so the generation schema and any future validation share one source.
//
// "parallel" belongs here because the engine executes it (reasoning.FlowNodeParallel,
// with fan-out and all/any/quorum/best_effort joins) and both the plan view and
// the test bench are written to render it. Omitting it from the schema enum meant
// that on providers which enforce enums natively — Gemini responseSchema,
// Anthropic forced-tool input_schema — the builder was structurally incapable of
// emitting a fan-out node, so every generated graph was serial no matter how
// parallel the work actually was.
var ValidNodeKinds = []string{"tool", "agent", "python", "llm", "branch", "parallel"}

// ValidTriggerTypes is the closed set of trigger types a draft may declare.
var ValidTriggerTypes = []string{"chat", "schedule", "channel", "webhook", "manual"}

// SchemaLLM is an OPTIONAL extension of LLM: a client that can constrain its
// completion to a JSON Schema. When the compiler's LLM implements it, generation
// goes through the schema-constrained path; otherwise it falls back to Complete.
type SchemaLLM interface {
	LLM
	// CompleteSchema returns a completion constrained (best-effort) to schema.
	CompleteSchema(ctx context.Context, prompt string, schema map[string]any) (string, error)
}

// completeDraft runs the builder model, preferring the schema-constrained path
// when the client supports it. Returning the raw JSON keeps the rest of the
// pipeline (ParseDraft → normalize → validate) identical either way.
func completeDraft(ctx context.Context, model LLM, prompt string) (string, error) {
	if sm, ok := model.(SchemaLLM); ok {
		return sm.CompleteSchema(ctx, prompt, DraftSchema())
	}
	return model.Complete(ctx, prompt)
}

// DraftSchema returns the JSON Schema for a generated Draft. It is deliberately
// lenient about OPTIONAL fields (so the model can omit layout, ports, etc.) and
// about freeform sub-objects, while being strict about the two things that cause
// the most breakage: node.kind must be one of ValidNodeKinds, and trigger.type
// must be one of ValidTriggerTypes.
func DraftSchema() map[string]any {
	// A freeform object (trigger.config, arbitrary maps) the model may fill in.
	freeform := map[string]any{"type": "object"}

	port := map[string]any{
		"type":                 "object",
		"additionalProperties": true,
		"properties": map[string]any{
			"name": map[string]any{"type": "string"},
			"type": map[string]any{"type": "string"},
		},
		"required": []any{"name"},
	}

	node := map[string]any{
		"type":                 "object",
		"additionalProperties": true,
		"properties": map[string]any{
			"id":   map[string]any{"type": "string"},
			"kind": map[string]any{"type": "string", "enum": toAnySlice(ValidNodeKinds)},
			"tool": map[string]any{"type": "string"},
			// agent names the peer agent to invoke (kind=agent).
			"agent":       map[string]any{"type": "string"},
			"code":        map[string]any{"type": "string"},
			"description": map[string]any{"type": "string"},
			"input":       map[string]any{"type": "string"},
			"output":      map[string]any{"type": "string"},
			"x":           map[string]any{"type": "number"},
			"y":           map[string]any{"type": "number"},
			"inputs":      map[string]any{"type": "array", "items": port},
			"outputs":     map[string]any{"type": "array", "items": port},
		},
		"required": []any{"id", "kind"},
	}

	edge := map[string]any{
		"type":                 "object",
		"additionalProperties": true,
		"properties": map[string]any{
			"from":      map[string]any{"type": "string"},
			"to":        map[string]any{"type": "string"},
			"from_port": map[string]any{"type": "string"},
			"to_port":   map[string]any{"type": "string"},
			"if":        map[string]any{"type": "string"},
		},
		"required": []any{"from"},
	}

	flow := map[string]any{
		"type":                 "object",
		"additionalProperties": true,
		"properties": map[string]any{
			"nodes":  map[string]any{"type": "array", "items": node},
			"edges":  map[string]any{"type": "array", "items": edge},
			"entry":  map[string]any{"type": "string"},
			"output": map[string]any{"type": "string"},
		},
		"required": []any{"nodes", "entry"},
	}

	trigger := map[string]any{
		"type":                 "object",
		"additionalProperties": true,
		"properties": map[string]any{
			"type":   map[string]any{"type": "string", "enum": toAnySlice(ValidTriggerTypes)},
			"config": freeform,
		},
		"required": []any{"type"},
	}

	return map[string]any{
		"type":                 "object",
		"additionalProperties": true,
		"properties": map[string]any{
			"name":          map[string]any{"type": "string"},
			"system_prompt": map[string]any{"type": "string"},
			"trigger":       trigger,
			"channels":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"flow":          flow,
		},
		"required": []any{"name", "flow"},
	}
}

func toAnySlice(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}
